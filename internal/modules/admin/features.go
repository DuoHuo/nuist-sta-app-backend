package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

// ---------------------------------------------------------------------------
// 通用地物（map_features）：建筑与 POI 之外、没有室内结构的校园地物。
// 建筑/POI 保留各自的强类型表与既有接口；本表只负责"让每个地物都有稳定编号"，
// 从而能被 App 点到、搜到、打开自己的详情页。
// ---------------------------------------------------------------------------

// featureKinds 与 migrations/000004 的 CHECK 约束一致。
var featureKinds = []string{
	"road", "path", "green", "water", "square", "sports",
	"gate", "bus_stop", "parking", "food", "shop", "study",
	"service", "sculpture", "facility", "other",
}

// featureStatuses 复用同一套状态：draft 只在管理台可见，published 才下发 App。
var featureStatuses = []string{"published", "draft"}

// 几何规模上限：校园地物手绘足够，同时防止误传大范围数据拖垮 App 渲染。
const (
	maxVertices     = 5000
	maxPolygonAreaM = 5_000_000 // 5 km²（主校区约 2 km²）
)

// featureGeometryTypes 允许点/线/面及其 Multi 形式。
var featureGeometryTypes = []string{
	"Point", "LineString", "Polygon",
	"MultiPoint", "MultiLineString", "MultiPolygon",
}

// MapFeature 地物完整记录（管理台读写用；App 侧走 mapdata 的只读接口）。
type MapFeature struct {
	FeatureID   int64           `json:"feature_id"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ShowName    bool            `json:"show_name"`
	Geometry    json.RawMessage `json:"geometry"`
	Props       json.RawMessage `json:"props"`
	Status      string          `json:"status"`
	CreatedBy   string          `json:"created_by"`
	Source      string          `json:"source"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// FeatureCreate 新建地物。status 留空按 published 处理，show_name 留空按 true 处理。
type FeatureCreate struct {
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ShowName    *bool           `json:"show_name"`
	Geometry    json.RawMessage `json:"geometry"`
	Props       json.RawMessage `json:"props"`
	Status      string          `json:"status"`
}

// FeaturePatch 部分更新；nil 表示不改该字段。
type FeaturePatch struct {
	Kind        *string          `json:"kind"`
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	ShowName    *bool            `json:"show_name"`
	Geometry    *json.RawMessage `json:"geometry"`
	Props       *json.RawMessage `json:"props"`
	Status      *string          `json:"status"`
}

func (r *Repo) ListFeatures(ctx context.Context, kind, status, q string) ([]MapFeature, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT feature_id, kind, name, description, show_name,
		       ST_AsGeoJSON(geom)::jsonb, props, status, created_by, source,
		       created_at, updated_at
		FROM map_features
		WHERE ($1 = '' OR kind = $1)
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR name ILIKE '%' || $3 || '%' OR description ILIKE '%' || $3 || '%')
		ORDER BY feature_id DESC`, kind, status, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []MapFeature{}
	for rows.Next() {
		var f MapFeature
		if err := rows.Scan(&f.FeatureID, &f.Kind, &f.Name, &f.Description, &f.ShowName,
			&f.Geometry, &f.Props, &f.Status, &f.CreatedBy, &f.Source,
			&f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

func (r *Repo) CreateFeature(ctx context.Context, p FeatureCreate, editor string) (int64, error) {
	if err := validateKind(p.Kind); err != nil {
		return 0, err
	}
	if strings.TrimSpace(p.Name) == "" {
		return 0, httpx.Unprocessable("地物名称不能为空")
	}
	status, err := normalizeStatus(p.Status)
	if err != nil {
		return 0, err
	}
	props, err := normalizeProps(p.Props)
	if err != nil {
		return 0, err
	}
	if err := r.checkGeometry(ctx, p.Geometry, featureGeometryTypes); err != nil {
		return 0, err
	}
	showName := true
	if p.ShowName != nil {
		showName = *p.ShowName
	}

	var id int64
	err = r.pool.QueryRow(ctx, `
		INSERT INTO map_features (kind, name, description, show_name, geom, props, status, created_by, source)
		VALUES ($1, $2, $3, $4, feature_geom($5::text), $6::jsonb, $7, $8, 'admin')
		RETURNING feature_id`,
		p.Kind, strings.TrimSpace(p.Name), p.Description, showName, string(p.Geometry),
		string(props), status, editor).Scan(&id)
	return id, err
}

func (r *Repo) UpdateFeature(ctx context.Context, featureID int64, p FeaturePatch) error {
	if p.Kind != nil {
		if err := validateKind(*p.Kind); err != nil {
			return err
		}
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		return httpx.Unprocessable("地物名称不能为空")
	}
	var status *string
	if p.Status != nil {
		s, err := normalizeStatus(*p.Status)
		if err != nil {
			return err
		}
		status = &s
	}
	var props *string
	if p.Props != nil {
		normalized, err := normalizeProps(*p.Props)
		if err != nil {
			return err
		}
		s := string(normalized)
		props = &s
	}
	var geom *string
	if p.Geometry != nil {
		if err := r.checkGeometry(ctx, *p.Geometry, featureGeometryTypes); err != nil {
			return err
		}
		s := string(*p.Geometry)
		geom = &s
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE map_features SET
			kind        = COALESCE($1, kind),
			name        = COALESCE($2, name),
			description = COALESCE($3, description),
			geom        = CASE WHEN $4::text IS NULL THEN geom ELSE feature_geom($4::text) END,
			props       = CASE WHEN $5::text IS NULL THEN props ELSE $5::jsonb END,
			status      = COALESCE($6, status),
			show_name   = COALESCE($7, show_name),
			updated_at  = now()
		WHERE feature_id = $8`,
		p.Kind, p.Name, p.Description, geom, props, status, p.ShowName, featureID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("地物不存在")
	}
	return nil
}

func (r *Repo) DeleteFeature(ctx context.Context, featureID int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM map_features WHERE feature_id = $1`, featureID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("地物不存在")
	}
	return nil
}

// AllFeaturesGeometry 全部地物（含草稿）的 FeatureCollection，供管理台地图叠加。
func (r *Repo) AllFeaturesGeometry(ctx context.Context) (json.RawMessage, error) {
	return r.queryFeatureCollection(ctx, `
		SELECT jsonb_build_object(
			'type', 'FeatureCollection',
			'features', COALESCE(jsonb_agg(feature ORDER BY feature_id), '[]'::jsonb))
		FROM (
			SELECT jsonb_build_object(
				'type', 'Feature',
				'id', feature_id,
				'geometry', ST_AsGeoJSON(geom)::jsonb,
				'properties', jsonb_build_object(
					'layer', 'feature',
					'feature_id', feature_id,
					'kind', kind,
					'name', name,
					'show_name', show_name,
					'status', status)) AS feature,
				feature_id
			FROM map_features
		) merged`)
}

// ---------------------------------------------------------------------------
// 建筑：补齐管理台缺失的"新建"与"改轮廓"。此前建筑只能改元数据，
// 新楼要进库必须走 QGIS 或 SQL 脚本。
// ---------------------------------------------------------------------------

// BuildingCreate 管理台新建建筑；building_id 留空时自动生成 ADM-xxxxxxxx。
type BuildingCreate struct {
	BuildingID   string          `json:"building_id"`
	Name         string          `json:"name"`
	HeightM      *float64        `json:"height_m"`
	HeightSource string          `json:"height_source"`
	Geometry     json.RawMessage `json:"geometry"`
}

func (r *Repo) CreateBuilding(ctx context.Context, p BuildingCreate, editor string) (string, error) {
	if strings.TrimSpace(p.Name) == "" {
		return "", httpx.Unprocessable("建筑名称不能为空")
	}
	if p.HeightM != nil && *p.HeightM < 0 {
		return "", httpx.Unprocessable("height_m 不能为负")
	}
	if p.HeightM != nil && (p.HeightSource == "" || !allowedHeightSource[p.HeightSource]) {
		return "", httpx.Unprocessable("填写高度时必须同时指定 height_source（measured / estimated_floors / display_default）")
	}
	if err := r.checkGeometry(ctx, p.Geometry, []string{"Polygon"}); err != nil {
		return "", err
	}

	id := strings.TrimSpace(p.BuildingID)
	if id == "" {
		generated, err := newAdminBuildingID()
		if err != nil {
			return "", err
		}
		id = generated
	}

	err := r.pool.QueryRow(ctx, `
		INSERT INTO buildings (building_id, name, footprint, height_m, height_source, created_by, source)
		VALUES ($1, $2, feature_geom($3::text), $4, NULLIF($5,''), $6, 'admin')
		RETURNING building_id`,
		id, strings.TrimSpace(p.Name), string(p.Geometry), p.HeightM, p.HeightSource, editor).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", httpx.NewError(http.StatusConflict, "conflict", "建筑编号已存在："+id)
		}
		return "", err
	}
	return id, nil
}

func (r *Repo) UpdateBuildingGeometry(ctx context.Context, buildingID string, raw json.RawMessage) error {
	if err := r.checkGeometry(ctx, raw, []string{"Polygon"}); err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE buildings SET footprint = feature_geom($1::text), updated_at = now()
		WHERE building_id = $2`, string(raw), buildingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("建筑不存在")
	}
	return nil
}

// newAdminBuildingID 生成管理台建筑编号。既有编号来自 OSM（OSM-Way…），
// 人工新建的用 ADM- 前缀 + 32 位随机十六进制，避免与导入数据撞号。
func newAdminBuildingID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ADM-" + hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

func validateKind(kind string) error {
	if !slices.Contains(featureKinds, kind) {
		return httpx.Unprocessable("地物类别只能是：" + strings.Join(featureKinds, " / "))
	}
	return nil
}

func normalizeStatus(status string) (string, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return "published", nil
	}
	if !slices.Contains(featureStatuses, status) {
		return "", httpx.Unprocessable("status 只能是 published / draft")
	}
	return status, nil
}

// normalizeProps 校验扩展属性：只接受对象，空值归一为 {}。
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

// checkGeometry 校验提交的 GeoJSON 几何。类型、坐标范围、顶点数与面积在 Go 侧
// 先挡（无需连库）；自相交、环不闭合这类空间有效性交给 PostGIS 判定——它是入库后
// 几何的最终解释者，在 Go 里重写一套判断只会和它的结论不一致。
// 不做静默修复（ST_MakeValid）：提交错了就应该被拒绝并看到原因。
func (r *Repo) checkGeometry(ctx context.Context, raw json.RawMessage, allowed []string) error {
	if len(raw) == 0 {
		return httpx.Unprocessable("缺少 geometry")
	}
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return httpx.Unprocessable("geometry 不是合法的 GeoJSON：" + err.Error())
	}
	if !slices.Contains(allowed, g.Type) {
		return httpx.Unprocessable(fmt.Sprintf("几何类型只支持 %s，收到 %s",
			strings.Join(allowed, " / "), g.Type))
	}
	if len(g.Coordinates) == 0 {
		return httpx.Unprocessable("geometry 缺少 coordinates")
	}

	var coords any
	if err := json.Unmarshal(g.Coordinates, &coords); err != nil {
		return httpx.Unprocessable("coordinates 格式错误")
	}
	count := 0
	var walkErr error
	eachPosition(coords, func(pos []any) bool {
		count++
		if count > maxVertices {
			walkErr = httpx.Unprocessable(fmt.Sprintf("顶点数超过上限 %d", maxVertices))
			return false
		}
		if len(pos) < 2 {
			walkErr = httpx.Unprocessable("坐标应为 [经度, 纬度]")
			return false
		}
		lon, okLon := pos[0].(float64)
		lat, okLat := pos[1].(float64)
		if !okLon || !okLat {
			walkErr = httpx.Unprocessable("坐标必须是数字")
			return false
		}
		if lon < -180 || lon > 180 || lat < -90 || lat > 90 {
			walkErr = httpx.Unprocessable("坐标超出 WGS84 范围")
			return false
		}
		return true
	})
	if walkErr != nil {
		return walkErr
	}
	if count == 0 {
		return httpx.Unprocessable("几何没有任何坐标点")
	}

	var reason string
	var points int
	var area float64
	err := r.pool.QueryRow(ctx, `
		SELECT CASE WHEN ST_IsValid(g) THEN '' ELSE ST_IsValidReason(g) END,
		       ST_NPoints(g),
		       CASE WHEN GeometryType(g) IN ('POLYGON','MULTIPOLYGON')
		            THEN ST_Area(g::geography) ELSE 0 END
		FROM (SELECT ST_Force2D(ST_SetSRID(ST_GeomFromGeoJSON($1::text), 4326)) AS g) s`,
		string(raw)).Scan(&reason, &points, &area)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.Unprocessable("几何无法解析为空")
		}
		return httpx.Unprocessable("几何解析失败：" + err.Error())
	}
	if reason != "" {
		return httpx.Unprocessable("几何无效：" + reason)
	}
	if area > maxPolygonAreaM {
		return httpx.Unprocessable(fmt.Sprintf("面积 %.0f m² 超过上限 %.0f m²", area, float64(maxPolygonAreaM)))
	}
	return nil
}

// eachPosition 深度遍历 GeoJSON coordinates，对每个坐标位置调用 fn；
// fn 返回 false 表示提前结束（用于超限时立即短路）。
func eachPosition(v any, fn func(pos []any) bool) bool {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return true
	}
	if _, isNumber := arr[0].(float64); isNumber {
		return fn(arr)
	}
	for _, item := range arr {
		if !eachPosition(item, fn) {
			return false
		}
	}
	return true
}
