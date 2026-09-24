package bus

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

// 本文件只放纯函数校验：不碰数据库，CI 里直接跑（与 admin/features.go 同一分工）。
// 有意不复用 admin.checkGeometry：线路的规则不一样——环线会绕回自己（允许自相交）、
// 线路没有面积概念，这里只查类型、顶点数、坐标范围，可解析性交给 PostGIS。

const (
	maxVertices        = 5000 // 与 admin 的顶点上限一致
	maxVehicleIDLen    = 64
	defaultMaxAgeS     = 120   // 实时位置默认新鲜度窗口（秒）
	maxMaxAgeS         = 86400 // 上限 1 天，再长就不叫实时了
	clockSkewAllowance = time.Minute
)

// statuses 与迁移里的 CHECK 约束一致：draft 只在管理台可见，published 才下发 App。
var statuses = []string{"published", "draft"}

// lineGeometryTypes 线路走向允许的几何类型；入库前统一 ST_Multi 成 MultiLineString。
var lineGeometryTypes = []string{"LineString", "MultiLineString"}

var pointGeometryTypes = []string{"Point"}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func normalizeStatus(status string) (string, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return "published", nil
	}
	if !slices.Contains(statuses, status) {
		return "", httpx.Unprocessable("status 只能是 published / draft")
	}
	return status, nil
}

// normalizeProps 校验扩展属性：只接受对象，空值归一为 {}（避免入库 NULL 撞 NOT NULL 默认值）。
func normalizeProps(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`), nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, httpx.Unprocessable("props 必须是 JSON 对象")
	}
	return raw, nil
}

// normalizeColor 校验线路颜色：#RRGGBB 或留空（留空表示用客户端默认色）。
// 统一小写存储，避免 App 侧按字符串比较颜色时大小写不一致。
func normalizeColor(color string) (string, error) {
	color = strings.TrimSpace(color)
	if color == "" {
		return "", nil
	}
	if !colorRe.MatchString(color) {
		return "", httpx.Unprocessable("color 应形如 #1a7f37（六位十六进制）")
	}
	return strings.ToLower(color), nil
}

// planStopSeq 把"提交的站点顺序"翻译成 seq 列表，并挡住无意义的站序。
//
// 规则：
//   - 空列表 = 清空该线路的站序（线路先画、站点后定是允许的）；
//   - 少于 2 站 = 拒绝：一条线至少要能表达"从哪到哪"；
//   - 中间站重复 = 拒绝：同一站出现两次，"下一站"就无从判断；
//   - 首末站相同 = 只有环线允许（起点即终点是环线的正常写法）。
func planStopSeq(stopIDs []int64, isLoop bool) ([]int, error) {
	if len(stopIDs) == 0 {
		return nil, nil
	}
	if len(stopIDs) < 2 {
		return nil, httpx.Unprocessable("线路至少要有 2 个站点（起点与终点）")
	}
	seen := map[int64]int{}
	last := len(stopIDs) - 1
	for i, id := range stopIDs {
		if id <= 0 {
			return nil, httpx.Unprocessable(fmt.Sprintf("第 %d 站的 stop_id 非法", i+1))
		}
		if prev, dup := seen[id]; dup {
			if !(isLoop && prev == 0 && i == last) {
				return nil, httpx.Unprocessable(fmt.Sprintf(
					"站点 %d 重复出现（第 %d 站与第 %d 站）；只有环线的首末站可以同站", id, prev+1, i+1))
			}
			continue
		}
		seen[id] = i
	}
	seq := make([]int, len(stopIDs))
	for i := range stopIDs {
		seq[i] = i
	}
	return seq, nil
}

// validatePosition 校验一次位置上报。
func validatePosition(p positionInput) error {
	p.VehicleID = strings.TrimSpace(p.VehicleID)
	if p.VehicleID == "" {
		return httpx.Unprocessable("vehicle_id 不能为空")
	}
	if len(p.VehicleID) > maxVehicleIDLen {
		return httpx.Unprocessable(fmt.Sprintf("vehicle_id 过长（上限 %d 字符）", maxVehicleIDLen))
	}
	if p.Lng < -180 || p.Lng > 180 || p.Lat < -90 || p.Lat > 90 {
		return httpx.Unprocessable("经纬度超出 WGS84 范围")
	}
	if p.HeadingDeg != nil && (*p.HeadingDeg < 0 || *p.HeadingDeg >= 360) {
		return httpx.Unprocessable("heading_deg 取值应为 [0,360)，0 表示正北")
	}
	if p.SpeedKMH != nil && (*p.SpeedKMH < 0 || *p.SpeedKMH > 200) {
		return httpx.Unprocessable("speed_kmh 取值应为 0..200")
	}
	if p.AccuracyM != nil && (*p.AccuracyM < 0 || *p.AccuracyM > 10000) {
		return httpx.Unprocessable("accuracy_m 取值应为 0..10000")
	}
	// 设备时钟普遍不准：晚于服务器 1 分钟以内照收，再往后就是错的时间戳了。
	if p.ReportedAt != nil && p.ReportedAt.After(time.Now().Add(clockSkewAllowance)) {
		return httpx.Unprocessable("reported_at 晚于服务器时间，请检查设备时钟")
	}
	return nil
}

// normalizeMaxAge 解析实时接口的 max_age_s。0 表示不过滤（全量下发车辆，由客户端用 age_s 判断）。
func normalizeMaxAge(v int) (int, error) {
	if v < 0 {
		return 0, httpx.Unprocessable("max_age_s 不能为负（0 表示不过滤）")
	}
	if v > maxMaxAgeS {
		return 0, httpx.Unprocessable(fmt.Sprintf("max_age_s 上限 %d 秒", maxMaxAgeS))
	}
	return v, nil
}
