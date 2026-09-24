package bus

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

// 这些校验都不碰数据库，CI 里直接跑。需要 PostGIS 的部分（几何能否解析、
// 站点是否存在）留给服务器上的端到端验收。

func apiErr(t *testing.T, err error) *httpx.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("期望报错，实际通过了")
	}
	he, ok := err.(*httpx.Error)
	if !ok {
		t.Fatalf("期望 *httpx.Error，实际 %T: %v", err, err)
	}
	return he
}

func TestPlanStopSeq(t *testing.T) {
	// 往返线：两站起步，中间站不得重复
	seq, err := planStopSeq([]int64{7, 3, 9}, false)
	if err != nil {
		t.Fatalf("普通线路被拒: %v", err)
	}
	if len(seq) != 3 || seq[0] != 0 || seq[2] != 2 {
		t.Fatalf("seq 应为 0..n-1，实际 %v", seq)
	}

	// 环线首末同站是正常写法
	if _, err := planStopSeq([]int64{7, 3, 9, 7}, true); err != nil {
		t.Fatalf("环线首末同站被拒: %v", err)
	}
	// 非环线首末同站是画错了
	if _, err := planStopSeq([]int64{7, 3, 9, 7}, false); err == nil {
		t.Fatal("非环线的首末同站应被拒绝")
	}
	// 中间站重复：无论是否环线都没有意义（环线只放行"首末同站"这一种重复）
	if _, err := planStopSeq([]int64{7, 3, 7, 9}, true); err == nil {
		t.Fatal("中间站重复应被拒绝（环线也一样）")
	}
	// 三站环线：起点即终点，同样是正常写法
	if _, err := planStopSeq([]int64{7, 3, 7}, true); err != nil {
		t.Fatalf("三站环线被拒: %v", err)
	}
	// 少于两站：表达不了"从哪到哪"
	if _, err := planStopSeq([]int64{7}, true); err == nil {
		t.Fatal("单站线路应被拒绝")
	}
	if _, err := planStopSeq([]int64{7, 0}, false); err == nil {
		t.Fatal("stop_id 非法应被拒绝")
	}
	// 空列表 = 清空站序（线路先建、站点后定）
	seq, err = planStopSeq(nil, false)
	if err != nil || len(seq) != 0 {
		t.Fatalf("空站序应被接受并返回空 seq，实际 (%v, %v)", seq, err)
	}
	if he := apiErr(t, err2(planStopSeq([]int64{1, 1, 1}, true))); he.Status != 422 {
		t.Fatalf("站序违规应返回 422，实际 %d", he.Status)
	}
}

// err2 丢掉首个返回值，只取错误，便于把校验函数塞进断言里。
func err2[T any](_ T, err error) error { return err }

func TestNormalizeColor(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"#0B7285":    "#0b7285", // 统一小写，App 侧按字符串比较颜色才稳
		"#0b7285":    "#0b7285",
		"  #ffffff ": "#ffffff",
	}
	for in, want := range cases {
		got, err := normalizeColor(in)
		if err != nil || got != want {
			t.Fatalf("normalizeColor(%q) = (%q, %v)，期望 %q", in, got, err, want)
		}
	}
	for _, in := range []string{"0b7285", "#fff", "#GGGGGG", "red", "#0b72855"} {
		if _, err := normalizeColor(in); err == nil {
			t.Fatalf("normalizeColor(%q) 应被拒绝", in)
		}
	}
}

func TestNormalizeStatusAndProps(t *testing.T) {
	if s, err := normalizeStatus(""); err != nil || s != "published" {
		t.Fatalf("空状态应归一为 published，实际 (%q, %v)", s, err)
	}
	if _, err := normalizeStatus("hidden"); err == nil {
		t.Fatal("未知状态应被拒绝")
	}
	for _, in := range []string{"[]", `"text"`, "123"} {
		if _, err := normalizeProps(json.RawMessage(in)); err == nil {
			t.Fatalf("normalizeProps(%q) 应被拒绝（只接受对象）", in)
		}
	}
	got, err := normalizeProps(nil)
	if err != nil || string(got) != "{}" {
		t.Fatalf("空 props -> (%s, %v)，期望 {}", got, err)
	}
}

func TestValidatePosition(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	now := time.Now()

	ok := positionInput{VehicleID: "BUS-01", Lng: 118.7175, Lat: 32.2058,
		HeadingDeg: f(359.9), SpeedKMH: f(0), AccuracyM: f(8), ReportedAt: &now}
	if err := validatePosition(ok); err != nil {
		t.Fatalf("合法上报被拒: %v", err)
	}

	bad := map[string]positionInput{
		"空车牌":    {Lng: 118.7, Lat: 32.2},
		"经度越界":   {VehicleID: "BUS-01", Lng: 200, Lat: 32.2},
		"纬度越界":   {VehicleID: "BUS-01", Lng: 118.7, Lat: -91},
		"朝向 360": {VehicleID: "BUS-01", Lng: 118.7, Lat: 32.2, HeadingDeg: f(360)},
		"负速度":    {VehicleID: "BUS-01", Lng: 118.7, Lat: 32.2, SpeedKMH: f(-1)},
		"超速":     {VehicleID: "BUS-01", Lng: 118.7, Lat: 32.2, SpeedKMH: f(201)},
		"时钟超前":   {VehicleID: "BUS-01", Lng: 118.7, Lat: 32.2, ReportedAt: ptrTime(now.Add(5 * time.Minute))},
	}
	for name, in := range bad {
		if err := validatePosition(in); err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
	// 客户端时钟略超前（1 分钟内）照收：设备时钟普遍不准，不该因此丢点
	if err := validatePosition(positionInput{VehicleID: "BUS-01", Lng: 118.7, Lat: 32.2,
		ReportedAt: ptrTime(now.Add(30 * time.Second))}); err != nil {
		t.Fatalf("轻微时钟超前应被接受: %v", err)
	}
}

func TestNormalizeMaxAgeAndAgeSeconds(t *testing.T) {
	if v, err := normalizeMaxAge(0); err != nil || v != 0 {
		t.Fatalf("0（不过滤）应被接受，实际 (%d, %v)", v, err)
	}
	if _, err := normalizeMaxAge(-1); err == nil {
		t.Fatal("负数应被拒绝")
	}
	if _, err := normalizeMaxAge(maxMaxAgeS + 1); err == nil {
		t.Fatal("超过上限应被拒绝")
	}

	if age := ageSeconds(nil); age != nil {
		t.Fatalf("从未上报 -> nil，实际 %v", *age)
	}
	if age := ageSeconds(ptrTime(time.Now().Add(-30 * time.Second))); age == nil || *age < 29 || *age > 31 {
		t.Fatalf("30 秒前的点 -> 约 30，实际 %v", age)
	}
	// 设备时钟比服务器快时，age 不该是负数
	if age := ageSeconds(ptrTime(time.Now().Add(time.Minute))); age == nil || *age != 0 {
		t.Fatalf("未来时间戳 -> 0，实际 %v", age)
	}
}

func TestGeometryEmptiness(t *testing.T) {
	for _, raw := range []string{"", "null", "  null "} {
		if !isEmptyGeom(json.RawMessage(raw)) {
			t.Fatalf("isEmptyGeom(%q) 应为真", raw)
		}
		if got := rawOrNil(json.RawMessage(raw)); got != nil {
			t.Fatalf("rawOrNil(%q) 应为 nil，实际 %s", raw, got)
		}
	}
	if isEmptyGeom(json.RawMessage(`{"type":"Point","coordinates":[1,2]}`)) {
		t.Fatal("真几何不该被判为空")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
