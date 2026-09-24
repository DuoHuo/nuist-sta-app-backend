// Package bus 提供校园公交（小公交/接驳车）的线路、站点与实时车辆位置接口。
//
// 表结构与取舍见 migrations/000005_campus_bus.up.sql。三条读取路径：
//   - /bus/routes、/bus/stops、/bus/routes/:id —— 列表与详情页；
//   - /bus/geometry —— FeatureCollection，App 自绘图层用（与通用地物同一条路，
//     不需要重切瓦片就能立刻可见）；
//   - /bus/vehicles —— 实时位置，App 轮询；位置上报走 /bus/positions。
//
// 底图（Martin 从 PostGIS 直发瓦片）与 App 自绘用同一批表、同一份字段名，
// 预留的样式片段见 configs/style.bus-layers.json。
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/geo"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// ---------------------------------------------------------------------------
// 线路
// ---------------------------------------------------------------------------

type Route struct {
	RouteID     int64           `json:"route_id"`
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Color       string          `json:"color,omitempty"`
	IsLoop      bool            `json:"is_loop"`
	Status      string          `json:"status"`
	StopCount   int             `json:"stop_count"`
	Geometry    json.RawMessage `json:"geometry,omitempty"`
	Props       json.RawMessage `json:"props,omitempty"`

	// 仅详情接口返回
	Stops []RouteStop `json:"stops,omitempty"`
}

// AdminRoute 管理台视角：多出提交人与时间戳（App 不需要这些）。
type AdminRoute struct {
	Route
	CreatedBy string    `json:"created_by"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RouteStop 线路上的一个停靠点（seq 从 0 开始，与提交顺序一致）。
type RouteStop struct {
	Seq         int             `json:"seq"`
	StopID      int64           `json:"stop_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Lng         float64         `json:"lng"`
	Lat         float64         `json:"lat"`
	Props       json.RawMessage `json:"props,omitempty"`
}

const routeColumns = `r.route_id, r.code, r.name, r.description, r.color, r.is_loop, r.status,
	(SELECT count(*) FROM bus_route_stops rs WHERE rs.route_id = r.route_id),
	COALESCE(CASE WHEN $1 THEN ST_AsGeoJSON(r.geom) END::jsonb, 'null'::jsonb), r.props`

// ListRoutes 线路列表。status 传 "published" 只出下发状态（App），传 "" 出全部（管理台）。
func (r *Repo) ListRoutes(ctx context.Context, q, status string, withGeometry bool) ([]Route, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+routeColumns+`
		FROM bus_routes r
		WHERE ($2 = '' OR r.status = $2)
		  AND ($3 = '' OR r.code ILIKE '%'||$3||'%'
		                OR r.name ILIKE '%'||$3||'%'
		                OR r.description ILIKE '%'||$3||'%')
		ORDER BY (r.code ILIKE $4) DESC, r.code, r.route_id`,
		withGeometry, status, q, q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Route{}
	for rows.Next() {
		var rt Route
		var geom json.RawMessage
		if err := rows.Scan(&rt.RouteID, &rt.Code, &rt.Name, &rt.Description, &rt.Color,
			&rt.IsLoop, &rt.Status, &rt.StopCount, &geom, &rt.Props); err != nil {
			return nil, err
		}
		rt.Geometry = rawOrNil(geom)
		out = append(out, rt)
	}
	return out, rows.Err()
}

// GetRoute 线路详情（含站序）。includeDraft=false 时草稿线路与草稿站点都不返回。
func (r *Repo) GetRoute(ctx context.Context, routeID int64, includeDraft bool) (*Route, error) {
	status := "published"
	if includeDraft {
		status = ""
	}
	var rt Route
	var geom json.RawMessage
	// withGeometry 恒为 true：详情页总要用到走向。
	err := r.pool.QueryRow(ctx, `
		SELECT `+routeColumns+`
		FROM bus_routes r
		WHERE r.route_id = $2 AND ($3 = '' OR r.status = $3)`,
		true, routeID, status).
		Scan(&rt.RouteID, &rt.Code, &rt.Name, &rt.Description, &rt.Color,
			&rt.IsLoop, &rt.Status, &rt.StopCount, &geom, &rt.Props)
	if err == pgx.ErrNoRows {
		return nil, httpx.NotFound(fmt.Sprintf("线路 %d 不存在", routeID))
	}
	if err != nil {
		return nil, err
	}
	rt.Geometry = rawOrNil(geom)
	if rt.Stops, err = r.RouteStops(ctx, routeID, includeDraft); err != nil {
		return nil, err
	}
	return &rt, nil
}

// RouteStops 按站序返回线路的停靠点。
func (r *Repo) RouteStops(ctx context.Context, routeID int64, includeDraft bool) ([]RouteStop, error) {
	status := "published"
	if includeDraft {
		status = ""
	}
	rows, err := r.pool.Query(ctx, `
		SELECT rs.seq, s.stop_id, s.name, s.description, ST_X(s.geom), ST_Y(s.geom), rs.props
		FROM bus_route_stops rs
		JOIN bus_stops s ON s.stop_id = rs.stop_id
		WHERE rs.route_id = $1 AND ($2 = '' OR s.status = $2)
		ORDER BY rs.seq`, routeID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RouteStop{}
	for rows.Next() {
		var st RouteStop
		if err := rows.Scan(&st.Seq, &st.StopID, &st.Name, &st.Description,
			&st.Lng, &st.Lat, &st.Props); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 站点
// ---------------------------------------------------------------------------

type Stop struct {
	StopID      int64           `json:"stop_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Lng         float64         `json:"lng"`
	Lat         float64         `json:"lat"`
	Status      string          `json:"status"`
	RouteCodes  []string        `json:"route_codes"` // 停靠此站的所有线路编号
	DistanceM   *float64        `json:"distance_m,omitempty"`
	Props       json.RawMessage `json:"props,omitempty"`
}

// AdminStop 管理台视角：多出提交人与时间戳。
type AdminStop struct {
	Stop
	CreatedBy string    `json:"created_by"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type stopFilter struct {
	Q       string
	Status  string
	RouteID *int64
	Near    *[2]float64 // {lng, lat}：给了就按距离升序，并带上 distance_m
	Limit   int
}

func (r *Repo) ListStops(ctx context.Context, f stopFilter) ([]Stop, error) {
	if f.Limit <= 0 || f.Limit > 2000 {
		f.Limit = 500
	}
	var lng, lat any
	if f.Near != nil {
		lng, lat = f.Near[0], f.Near[1]
	}
	rows, err := r.pool.Query(ctx, `
		SELECT t.stop_id, t.name, t.description, t.lng, t.lat, t.status,
		       t.route_codes, t.distance_m, t.props
		FROM (
			SELECT s.stop_id,
			       s.name,
			       s.description,
			       ST_X(s.geom) AS lng,
			       ST_Y(s.geom) AS lat,
			       s.status,
			       s.props,
			       COALESCE((SELECT array_agg(DISTINCT rt.code ORDER BY rt.code)
			                 FROM bus_route_stops rs
			                 JOIN bus_routes rt ON rt.route_id = rs.route_id
			                 WHERE rs.stop_id = s.stop_id), '{}'::text[]) AS route_codes,
			       CASE WHEN $4::float8 IS NULL THEN NULL
			            ELSE ST_Distance(s.geom::geography,
			                             ST_SetSRID(ST_MakePoint($4, $5), 4326)::geography) END AS distance_m
			FROM bus_stops s
			WHERE ($1 = '' OR s.status = $1)
			  AND ($2 = '' OR s.name ILIKE '%'||$2||'%' OR s.description ILIKE '%'||$2||'%')
			  AND ($3::bigint IS NULL OR EXISTS (
			          SELECT 1 FROM bus_route_stops rs
			          WHERE rs.stop_id = s.stop_id AND rs.route_id = $3))
		) t
		ORDER BY t.distance_m NULLS LAST, t.name, t.stop_id
		LIMIT $6`, f.Status, f.Q, f.RouteID, lng, lat, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Stop{}
	for rows.Next() {
		var s Stop
		if err := rows.Scan(&s.StopID, &s.Name, &s.Description, &s.Lng, &s.Lat,
			&s.Status, &s.RouteCodes, &s.DistanceM, &s.Props); err != nil {
			return nil, err
		}
		if s.DistanceM != nil {
			d := round1(*s.DistanceM)
			s.DistanceM = &d
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AdminStops 管理台列表：带提交人与时间戳（含草稿）。
func (r *Repo) AdminStops(ctx context.Context, f stopFilter) ([]AdminStop, error) {
	if f.Limit <= 0 || f.Limit > 2000 {
		f.Limit = 500
	}
	var routeID any
	if f.RouteID != nil {
		routeID = *f.RouteID
	}
	rows, err := r.pool.Query(ctx, `
		SELECT s.stop_id, s.name, s.description, ST_X(s.geom), ST_Y(s.geom), s.status,
		       COALESCE((SELECT array_agg(DISTINCT rt.code ORDER BY rt.code)
		                 FROM bus_route_stops rs
		                 JOIN bus_routes rt ON rt.route_id = rs.route_id
		                 WHERE rs.stop_id = s.stop_id), '{}'::text[]),
		       s.props, s.created_by, s.source, s.created_at, s.updated_at
		FROM bus_stops s
		WHERE ($1 = '' OR s.status = $1)
		  AND ($2 = '' OR s.name ILIKE '%'||$2||'%' OR s.description ILIKE '%'||$2||'%')
		  AND ($3::bigint IS NULL OR EXISTS (
		          SELECT 1 FROM bus_route_stops rs
		          WHERE rs.stop_id = s.stop_id AND rs.route_id = $3))
		ORDER BY s.name, s.stop_id
		LIMIT $4`, f.Status, f.Q, routeID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AdminStop{}
	for rows.Next() {
		var s AdminStop
		if err := rows.Scan(&s.StopID, &s.Name, &s.Description, &s.Lng, &s.Lat, &s.Status,
			&s.RouteCodes, &s.Props, &s.CreatedBy, &s.Source, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AdminRoutes 管理台线路列表：带提交人与时间戳（含草稿）。
func (r *Repo) AdminRoutes(ctx context.Context, q, status string, withGeometry bool) ([]AdminRoute, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+routeColumns+`, r.created_by, r.source, r.created_at, r.updated_at
		FROM bus_routes r
		WHERE ($2 = '' OR r.status = $2)
		  AND ($3 = '' OR r.code ILIKE '%'||$3||'%'
		                OR r.name ILIKE '%'||$3||'%'
		                OR r.description ILIKE '%'||$3||'%')
		ORDER BY r.route_id DESC`,
		withGeometry, status, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AdminRoute{}
	for rows.Next() {
		var rt AdminRoute
		var geom json.RawMessage
		if err := rows.Scan(&rt.RouteID, &rt.Code, &rt.Name, &rt.Description, &rt.Color,
			&rt.IsLoop, &rt.Status, &rt.StopCount, &geom, &rt.Props,
			&rt.CreatedBy, &rt.Source, &rt.CreatedAt, &rt.UpdatedAt); err != nil {
			return nil, err
		}
		rt.Geometry = rawOrNil(geom)
		out = append(out, rt)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 底图/自绘用的 FeatureCollection：线路（线）+ 站点（点）
// ---------------------------------------------------------------------------

// Geometry 输出线路与站点的 FeatureCollection。App 直接把它塞进一个 GeoJSON source
// 就能画出来（与通用地物同样的路子，不需要重切瓦片）；Martin 直发瓦片时用的也是
// 这两张表，图层名即表名——见 configs/style.bus-layers.json。
func (r *Repo) Geometry(ctx context.Context, includeDraft bool) (json.RawMessage, error) {
	status := "published"
	if includeDraft {
		status = ""
	}
	fc := geo.NewFeatureCollection()

	rows, err := r.pool.Query(ctx, `
		SELECT r.route_id, r.code, r.name, r.color, r.is_loop, ST_AsGeoJSON(r.geom)::text
		FROM bus_routes r
		WHERE r.geom IS NOT NULL AND ($1 = '' OR r.status = $1)
		ORDER BY r.code, r.route_id`, status)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var code, name, color, geom string
		var isLoop bool
		if err := rows.Scan(&id, &code, &name, &color, &isLoop, &geom); err != nil {
			rows.Close()
			return nil, err
		}
		fc.Add(json.RawMessage(geom), map[string]any{
			"layer":    "bus_route",
			"route_id": id,
			"code":     code,
			"name":     name,
			"color":    color,
			"is_loop":  isLoop,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.pool.Query(ctx, `
		SELECT s.stop_id, s.name, ST_AsGeoJSON(s.geom)::text,
		       COALESCE((SELECT array_agg(DISTINCT rt.code ORDER BY rt.code)
		                 FROM bus_route_stops rs
		                 JOIN bus_routes rt ON rt.route_id = rs.route_id
		                 WHERE rs.stop_id = s.stop_id), '{}'::text[])
		FROM bus_stops s
		WHERE ($1 = '' OR s.status = $1)
		ORDER BY s.name, s.stop_id`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, geom string
		var codes []string
		if err := rows.Scan(&id, &name, &geom, &codes); err != nil {
			return nil, err
		}
		fc.Add(json.RawMessage(geom), map[string]any{
			"layer":       "bus_stop",
			"stop_id":     id,
			"name":        name,
			"route_codes": codes,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return fc.Marshal(), nil
}

// ---------------------------------------------------------------------------
// 车辆与实时位置
// ---------------------------------------------------------------------------

type Vehicle struct {
	VehicleID  string     `json:"vehicle_id"`
	Label      string     `json:"label,omitempty"`
	RouteID    *int64     `json:"route_id,omitempty"`
	RouteCode  string     `json:"route_code,omitempty"`
	Enabled    bool       `json:"enabled"`
	Lng        *float64   `json:"lng,omitempty"`
	Lat        *float64   `json:"lat,omitempty"`
	HeadingDeg *float64   `json:"heading_deg,omitempty"`
	SpeedKMH   *float64   `json:"speed_kmh,omitempty"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
	AgeS       *float64   `json:"age_s,omitempty"` // 服务器算的"距今多少秒"，避免客户端时钟漂移
}

// AdminVehicle 管理台视角：多出注册时间与自由属性。
type AdminVehicle struct {
	Vehicle
	Props     json.RawMessage `json:"props,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type vehicleFilter struct {
	RouteID         *int64
	MaxAgeS         int // 0 = 不过滤（全量下发，客户端用 age_s 自己判断）
	IncludeDisabled bool
}

func (r *Repo) ListVehicles(ctx context.Context, f vehicleFilter) ([]Vehicle, error) {
	var cutoff *time.Time
	if f.MaxAgeS > 0 {
		t := time.Now().Add(-time.Duration(f.MaxAgeS) * time.Second)
		cutoff = &t
	}
	rows, err := r.pool.Query(ctx, `
		SELECT v.vehicle_id, v.label, v.route_id, COALESCE(rt.code, ''), v.enabled,
		       ST_X(p.geom), ST_Y(p.geom), p.heading_deg, p.speed_kmh, p.reported_at
		FROM bus_vehicles v
		LEFT JOIN bus_routes rt ON rt.route_id = v.route_id
		LEFT JOIN LATERAL (
			SELECT bp.geom, bp.heading_deg, bp.speed_kmh, bp.reported_at
			FROM bus_positions bp
			WHERE bp.vehicle_id = v.vehicle_id
			ORDER BY bp.reported_at DESC
			LIMIT 1
		) p ON TRUE
		WHERE ($1::bigint IS NULL OR v.route_id = $1)
		  AND ($2 OR v.enabled)
		  AND ($3::timestamptz IS NULL OR p.reported_at >= $3)
		ORDER BY COALESCE(rt.code, ''), v.vehicle_id`,
		f.RouteID, f.IncludeDisabled, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Vehicle{}
	for rows.Next() {
		var v Vehicle
		if err := rows.Scan(&v.VehicleID, &v.Label, &v.RouteID, &v.RouteCode, &v.Enabled,
			&v.Lng, &v.Lat, &v.HeadingDeg, &v.SpeedKMH, &v.ReportedAt); err != nil {
			return nil, err
		}
		v.AgeS = ageSeconds(v.ReportedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

// AdminVehicles 管理台车辆注册表：含停运车辆与自由属性。
func (r *Repo) AdminVehicles(ctx context.Context, routeID *int64) ([]AdminVehicle, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT v.vehicle_id, v.label, v.route_id, COALESCE(rt.code, ''), v.enabled,
		       ST_X(p.geom), ST_Y(p.geom), p.heading_deg, p.speed_kmh, p.reported_at,
		       v.props, v.created_at, v.updated_at
		FROM bus_vehicles v
		LEFT JOIN bus_routes rt ON rt.route_id = v.route_id
		LEFT JOIN LATERAL (
			SELECT bp.geom, bp.heading_deg, bp.speed_kmh, bp.reported_at
			FROM bus_positions bp
			WHERE bp.vehicle_id = v.vehicle_id
			ORDER BY bp.reported_at DESC
			LIMIT 1
		) p ON TRUE
		WHERE ($1::bigint IS NULL OR v.route_id = $1)
		ORDER BY v.vehicle_id`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AdminVehicle{}
	for rows.Next() {
		var v AdminVehicle
		if err := rows.Scan(&v.VehicleID, &v.Label, &v.RouteID, &v.RouteCode, &v.Enabled,
			&v.Lng, &v.Lat, &v.HeadingDeg, &v.SpeedKMH, &v.ReportedAt,
			&v.Props, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.AgeS = ageSeconds(v.ReportedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

// VehicleEnabled 查车辆是否已注册且在用；位置上报前先问一次，好给出明确的错误。
func (r *Repo) VehicleEnabled(ctx context.Context, vehicleID string) (bool, error) {
	var enabled bool
	err := r.pool.QueryRow(ctx,
		`SELECT enabled FROM bus_vehicles WHERE vehicle_id = $1`, vehicleID).Scan(&enabled)
	if err == pgx.ErrNoRows {
		return false, httpx.NotFound(fmt.Sprintf("车辆 %s 未注册（先 POST /admin/bus/vehicles）", vehicleID))
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

type positionInput struct {
	VehicleID  string
	RouteID    *int64
	Lng        float64
	Lat        float64
	HeadingDeg *float64
	SpeedKMH   *float64
	AccuracyM  *float64
	ReportedAt *time.Time
	Props      json.RawMessage
	Source     string
}

// InsertPosition 追加一条位置。reported_at 缺省用服务器时间（客户端可补传历史点）。
func (r *Repo) InsertPosition(ctx context.Context, in positionInput) (int64, time.Time, error) {
	var id int64
	var reportedAt time.Time
	err := r.pool.QueryRow(ctx, `
		INSERT INTO bus_positions (vehicle_id, route_id, geom, heading_deg, speed_kmh,
		                           accuracy_m, reported_at, props, source)
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326), $5, $6, $7,
		        COALESCE($8, now()), $9::jsonb, $10)
		RETURNING position_id, reported_at`,
		in.VehicleID, in.RouteID, in.Lng, in.Lat, in.HeadingDeg, in.SpeedKMH,
		in.AccuracyM, in.ReportedAt, string(in.Props), in.Source).Scan(&id, &reportedAt)
	if err != nil {
		if pgCode(err) == "23503" {
			return 0, time.Time{}, httpx.Unprocessable("route_id 对应的线路不存在")
		}
		return 0, time.Time{}, err
	}
	return id, reportedAt, nil
}

// ---------------------------------------------------------------------------
// 管理台写操作
// ---------------------------------------------------------------------------

type RouteCreate struct {
	Code        string          `json:"code" binding:"required"`
	Name        string          `json:"name" binding:"required"`
	Description string          `json:"description"`
	Color       string          `json:"color"`
	IsLoop      bool            `json:"is_loop"`
	Geometry    json.RawMessage `json:"geometry"`
	Props       json.RawMessage `json:"props"`
	Status      string          `json:"status"`
}

type RoutePatch struct {
	Code        *string          `json:"code"`
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Color       *string          `json:"color"`
	IsLoop      *bool            `json:"is_loop"`
	Geometry    *json.RawMessage `json:"geometry"`
	Props       *json.RawMessage `json:"props"`
	Status      *string          `json:"status"`
}

func (r *Repo) CreateRoute(ctx context.Context, p RouteCreate, editor string) (int64, error) {
	if err := r.checkGeometry(ctx, p.Geometry, lineGeometryTypes); err != nil {
		return 0, err
	}
	color, err := normalizeColor(p.Color)
	if err != nil {
		return 0, err
	}
	status, err := normalizeStatus(p.Status)
	if err != nil {
		return 0, err
	}
	props, err := normalizeProps(p.Props)
	if err != nil {
		return 0, err
	}

	var id int64
	// 没有几何的线路要传 NULL 而不是空串：feature_geom('') 会让 PostGIS 直接报错
	var geom any
	if !isEmptyGeom(p.Geometry) {
		geom = string(p.Geometry)
	}
	err = r.pool.QueryRow(ctx, `
		INSERT INTO bus_routes (code, name, description, color, is_loop, geom, props, status, created_by, source)
		VALUES ($1, $2, $3, $4, $5, ST_Multi(feature_geom($6::text)), $7::jsonb, $8, $9, 'admin')
		RETURNING route_id`,
		strings.TrimSpace(p.Code), strings.TrimSpace(p.Name), p.Description, color, p.IsLoop,
		geom, string(props), status, editor).Scan(&id)
	if err != nil {
		if pgCode(err) == "23505" {
			return 0, httpx.NewError(http.StatusConflict, "conflict", "线路编号已存在："+strings.TrimSpace(p.Code))
		}
		return 0, err
	}
	return id, nil
}

func (r *Repo) UpdateRoute(ctx context.Context, routeID int64, p RoutePatch) error {
	if p.Code != nil && strings.TrimSpace(*p.Code) == "" {
		return httpx.Unprocessable("线路编号不能为空")
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		return httpx.Unprocessable("线路名称不能为空")
	}
	var color *string
	if p.Color != nil {
		c, err := normalizeColor(*p.Color)
		if err != nil {
			return err
		}
		color = &c
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
		if err := r.checkGeometry(ctx, *p.Geometry, lineGeometryTypes); err != nil {
			return err
		}
		s := string(*p.Geometry)
		geom = &s
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE bus_routes SET
			code        = COALESCE($1, code),
			name        = COALESCE($2, name),
			description = COALESCE($3, description),
			color       = COALESCE($4, color),
			is_loop     = COALESCE($5, is_loop),
			geom        = CASE WHEN COALESCE($6::text, '') = '' THEN geom
			                   ELSE ST_Multi(feature_geom($6::text)) END,
			props       = CASE WHEN $7::text IS NULL THEN props ELSE $7::jsonb END,
			status      = COALESCE($8, status),
			updated_at  = now()
		WHERE route_id = $9`,
		nullableTrim(p.Code), nullableTrim(p.Name), p.Description, color, p.IsLoop,
		geom, props, status, routeID)
	if err != nil {
		if pgCode(err) == "23505" {
			return httpx.NewError(http.StatusConflict, "conflict", "线路编号已存在")
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("线路不存在")
	}
	return nil
}

func (r *Repo) DeleteRoute(ctx context.Context, routeID int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM bus_routes WHERE route_id = $1`, routeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("线路不存在")
	}
	return nil
}

// ReplaceRouteStops 整体替换线路站序：先校验站点都存在，再删旧插新，同一事务内完成。
// 传入空列表即清空站序（保留线路本身）。站序规则由 planStopSeq 在 handler 侧先挡。
func (r *Repo) ReplaceRouteStops(ctx context.Context, routeID int64, stopIDs []int64) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT TRUE FROM bus_routes WHERE route_id = $1`, routeID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, httpx.NotFound("线路不存在")
		}
		return 0, err
	}

	if len(stopIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT stop_id FROM bus_stops WHERE stop_id = ANY($1::bigint[])`, stopIDs)
		if err != nil {
			return 0, err
		}
		found := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			found[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		missing := make([]int64, 0, len(stopIDs))
		for _, id := range stopIDs {
			if !found[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return 0, httpx.Unprocessable("站点不存在：" + joinInt64(missing))
		}
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM bus_route_stops WHERE route_id = $1`, routeID); err != nil {
		return 0, err
	}
	count := 0
	if len(stopIDs) > 0 {
		tag, err := tx.Exec(ctx, `
			INSERT INTO bus_route_stops (route_id, stop_id, seq)
			SELECT $1, u.stop_id, (u.ord - 1)::int
			FROM unnest($2::bigint[]) WITH ORDINALITY AS u(stop_id, ord)`,
			routeID, stopIDs)
		if err != nil {
			return 0, err
		}
		count = int(tag.RowsAffected())
	}
	if _, err := tx.Exec(ctx,
		`UPDATE bus_routes SET updated_at = now() WHERE route_id = $1`, routeID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

type StopCreate struct {
	Name        string          `json:"name" binding:"required"`
	Description string          `json:"description"`
	Geometry    json.RawMessage `json:"geometry"`
	Props       json.RawMessage `json:"props"`
	Status      string          `json:"status"`
}

type StopPatch struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Geometry    *json.RawMessage `json:"geometry"`
	Props       *json.RawMessage `json:"props"`
	Status      *string          `json:"status"`
}

func (r *Repo) CreateStop(ctx context.Context, p StopCreate, editor string) (int64, error) {
	if isEmptyGeom(p.Geometry) {
		return 0, httpx.Unprocessable("站点必须有位置（geometry：Point）")
	}
	if err := r.checkGeometry(ctx, p.Geometry, pointGeometryTypes); err != nil {
		return 0, err
	}
	status, err := normalizeStatus(p.Status)
	if err != nil {
		return 0, err
	}
	props, err := normalizeProps(p.Props)
	if err != nil {
		return 0, err
	}

	var id int64
	err = r.pool.QueryRow(ctx, `
		INSERT INTO bus_stops (name, description, geom, props, status, created_by, source)
		VALUES ($1, $2, feature_geom($3::text), $4::jsonb, $5, $6, 'admin')
		RETURNING stop_id`,
		strings.TrimSpace(p.Name), p.Description, string(p.Geometry),
		string(props), status, editor).Scan(&id)
	return id, err
}

func (r *Repo) UpdateStop(ctx context.Context, stopID int64, p StopPatch) error {
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		return httpx.Unprocessable("站点名称不能为空")
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
		if err := r.checkGeometry(ctx, *p.Geometry, pointGeometryTypes); err != nil {
			return err
		}
		s := string(*p.Geometry)
		geom = &s
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE bus_stops SET
			name        = COALESCE($1, name),
			description = COALESCE($2, description),
			geom        = CASE WHEN COALESCE($3::text, '') = '' THEN geom ELSE feature_geom($3::text) END,
			props       = CASE WHEN $4::text IS NULL THEN props ELSE $4::jsonb END,
			status      = COALESCE($5, status),
			updated_at  = now()
		WHERE stop_id = $6`,
		nullableTrim(p.Name), p.Description, geom, props, status, stopID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("站点不存在")
	}
	return nil
}

func (r *Repo) DeleteStop(ctx context.Context, stopID int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM bus_stops WHERE stop_id = $1`, stopID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("站点不存在")
	}
	return nil
}

type VehicleCreate struct {
	VehicleID string          `json:"vehicle_id" binding:"required"`
	Label     string          `json:"label"`
	RouteID   *int64          `json:"route_id"`
	Enabled   *bool           `json:"enabled"`
	Props     json.RawMessage `json:"props"`
}

type VehiclePatch struct {
	Label   *string          `json:"label"`
	RouteID *int64           `json:"route_id"`
	Enabled *bool            `json:"enabled"`
	Props   *json.RawMessage `json:"props"`
}

func (r *Repo) CreateVehicle(ctx context.Context, p VehicleCreate) (string, error) {
	id := strings.TrimSpace(p.VehicleID)
	if id == "" {
		return "", httpx.Unprocessable("vehicle_id 不能为空")
	}
	if len(id) > maxVehicleIDLen {
		return "", httpx.Unprocessable(fmt.Sprintf("vehicle_id 过长（上限 %d 字符）", maxVehicleIDLen))
	}
	props, err := normalizeProps(p.Props)
	if err != nil {
		return "", err
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO bus_vehicles (vehicle_id, label, route_id, enabled, props)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		id, strings.TrimSpace(p.Label), p.RouteID, enabled, string(props))
	if err != nil {
		if pgCode(err) == "23505" {
			return "", httpx.NewError(http.StatusConflict, "conflict", "车辆编号已存在："+id)
		}
		if pgCode(err) == "23503" {
			return "", httpx.Unprocessable("route_id 对应的线路不存在")
		}
		return "", err
	}
	return id, nil
}

func (r *Repo) UpdateVehicle(ctx context.Context, vehicleID string, p VehiclePatch) error {
	var props *string
	if p.Props != nil {
		normalized, err := normalizeProps(*p.Props)
		if err != nil {
			return err
		}
		s := string(normalized)
		props = &s
	}
	// route_id 不能用 COALESCE 表达"清空排班"：JSON null 在 Go 侧就是 nil，
	// 与"字段没传"无法区分。约定改成 route_id <= 0 表示清空（管理台选"未排班"即发 0）。
	var clearRoute bool
	var routeID any
	if p.RouteID != nil {
		clearRoute = true
		if *p.RouteID > 0 {
			routeID = *p.RouteID
		}
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE bus_vehicles SET
			label      = COALESCE($1, label),
			route_id   = CASE WHEN $6 THEN $2::bigint ELSE route_id END,
			enabled    = COALESCE($3, enabled),
			props      = CASE WHEN $4::text IS NULL THEN props ELSE $4::jsonb END,
			updated_at = now()
		WHERE vehicle_id = $5`,
		nullableTrim(p.Label), routeID, p.Enabled, props, vehicleID, clearRoute)
	if err != nil {
		if pgCode(err) == "23503" {
			return httpx.Unprocessable("route_id 对应的线路不存在")
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("车辆不存在")
	}
	return nil
}

// DeleteVehicle 删除车辆注册。位置表是 ON DELETE CASCADE——历史轨迹一并删除，
// 只想停运请用 PATCH {"enabled": false}。
func (r *Repo) DeleteVehicle(ctx context.Context, vehicleID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM bus_vehicles WHERE vehicle_id = $1`, vehicleID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("车辆不存在")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 几何校验与工具
// ---------------------------------------------------------------------------

// checkGeometry 校验提交的 GeoJSON 几何。类型、坐标范围、顶点数在 Go 侧先挡；
// 能不能被 PostGIS 解析由 PostGIS 判定——它是入库几何的最终解释者。
// 空值 / null 视为"没给几何"（线路允许先建档后画线），由调用方决定是否必填。
// 不做静默修复：提交错了就应该被拒绝并看到原因。
func (r *Repo) checkGeometry(ctx context.Context, raw json.RawMessage, allowed []string) error {
	if isEmptyGeom(raw) {
		return nil
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

	var points int
	var geomType string
	err := r.pool.QueryRow(ctx, `
		SELECT ST_NPoints(g), GeometryType(g)
		FROM (SELECT ST_Force2D(ST_SetSRID(ST_GeomFromGeoJSON($1::text), 4326)) AS g) s`,
		string(raw)).Scan(&points, &geomType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.Unprocessable("几何无法解析为空")
		}
		return httpx.Unprocessable("几何解析失败：" + err.Error())
	}
	if points == 0 {
		return httpx.Unprocessable("几何没有任何坐标点")
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

// rawOrNil 把 SQL 里 COALESCE(..., 'null') 出来的 jsonb 还原成"没有几何"。
func rawOrNil(raw json.RawMessage) json.RawMessage {
	if isEmptyGeom(raw) {
		return nil
	}
	return raw
}

// isEmptyGeom 判断提交的几何是否是"没给"（空串 / JSON null）。
func isEmptyGeom(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

func nullableTrim(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func joinInt64(vals []int64) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}

// ageSeconds 用服务器时间算"距今多少秒"，客户端不必也不该拿自己的时钟去减。
func ageSeconds(reportedAt *time.Time) *float64 {
	if reportedAt == nil {
		return nil
	}
	age := time.Since(*reportedAt).Seconds()
	if age < 0 {
		age = 0
	}
	age = round1(age)
	return &age
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
