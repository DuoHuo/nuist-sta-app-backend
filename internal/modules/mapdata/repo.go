// Package mapdata 提供建筑、楼层、室内要素与地点搜索服务。
package mapdata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/geo"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

type Building struct {
	BuildingID   string   `json:"building_id"`
	OSMID        *int64   `json:"osm_id,omitempty"`
	Name         string   `json:"name"`
	Aliases      []string `json:"aliases"`
	HeightM      *float64 `json:"height_m"`
	HeightSource string   `json:"height_source,omitempty"`
	HasIndoorMap bool     `json:"has_indoor_map"`
	SplatSceneID string   `json:"splat_scene_id,omitempty"`
	CentroidLng  float64  `json:"centroid_lng"`
	CentroidLat  float64  `json:"centroid_lat"`

	// 仅详情接口返回
	Floors    []Floor    `json:"floors,omitempty"`
	Entrances []Entrance `json:"entrances,omitempty"`
}

type Floor struct {
	FloorID     int64    `json:"floor_id"`
	BuildingID  string   `json:"building_id"`
	LevelIndex  int      `json:"level_index"`
	DisplayName string   `json:"display_name"`
	ElevationM  *float64 `json:"elevation_m"`
	SortOrder   int      `json:"sort_order"`
}

type Entrance struct {
	EntranceID   int64  `json:"entrance_id"`
	Name         string `json:"name"`
	IsAccessible bool   `json:"is_accessible"`
	Lng          float64 `json:"lng"`
	Lat          float64 `json:"lat"`
	NavNodeID    *int64 `json:"nav_node_id,omitempty"`
}

const buildingColumns = `b.building_id, b.osm_id, b.name, b.aliases, b.height_m,
	COALESCE(b.height_source,''), b.has_indoor_map, COALESCE(b.splat_scene_id,''),
	ST_X(ST_Centroid(b.footprint)), ST_Y(ST_Centroid(b.footprint))`

func scanBuilding(row pgx.Row) (*Building, error) {
	var b Building
	err := row.Scan(&b.BuildingID, &b.OSMID, &b.Name, &b.Aliases, &b.HeightM,
		&b.HeightSource, &b.HasIndoorMap, &b.SplatSceneID,
		&b.CentroidLng, &b.CentroidLat)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ListBuildings 按关键字 / bbox 过滤建筑（不含几何，含质心用于定位视野）。
func (r *Repo) ListBuildings(ctx context.Context, q string, bbox *[4]float64) ([]Building, error) {
	where := []string{"TRUE"}
	args := []any{}
	if q != "" {
		args = append(args, "%"+q+"%")
		p := len(args)
		where = append(where, fmt.Sprintf(
			"(b.name ILIKE $%d OR EXISTS (SELECT 1 FROM unnest(b.aliases) a WHERE a ILIKE $%d))", p, p))
	}
	if bbox != nil {
		args = append(args, bbox[0], bbox[1], bbox[2], bbox[3])
		p := len(args)
		where = append(where, fmt.Sprintf("b.footprint && ST_MakeEnvelope($%d,$%d,$%d,$%d,4326)", p, p+1, p+2, p+3))
	}
	sql := fmt.Sprintf("SELECT %s FROM buildings b WHERE %s ORDER BY b.name",
		buildingColumns, strings.Join(where, " AND "))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Building{}
	for rows.Next() {
		var b Building
		if err := rows.Scan(&b.BuildingID, &b.OSMID, &b.Name, &b.Aliases, &b.HeightM,
			&b.HeightSource, &b.HasIndoorMap, &b.SplatSceneID,
			&b.CentroidLng, &b.CentroidLat); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBuilding 返回建筑详情（含楼层清单与出入口）。
func (r *Repo) GetBuilding(ctx context.Context, buildingID string) (*Building, error) {
	row := r.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM buildings b WHERE b.building_id = $1", buildingColumns), buildingID)
	b, err := scanBuilding(row)
	if err == pgx.ErrNoRows {
		return nil, httpx.NotFound(fmt.Sprintf("建筑 %s 不存在", buildingID))
	}
	if err != nil {
		return nil, err
	}
	if b.Floors, err = r.Floors(ctx, buildingID); err != nil {
		return nil, err
	}
	if b.Entrances, err = r.Entrances(ctx, buildingID); err != nil {
		return nil, err
	}
	return b, nil
}

func (r *Repo) Floors(ctx context.Context, buildingID string) ([]Floor, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT floor_id, building_id, level_index, display_name, elevation_m, sort_order
		FROM floors WHERE building_id = $1 ORDER BY sort_order, level_index`, buildingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Floor{}
	for rows.Next() {
		var f Floor
		if err := rows.Scan(&f.FloorID, &f.BuildingID, &f.LevelIndex, &f.DisplayName, &f.ElevationM, &f.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *Repo) Entrances(ctx context.Context, buildingID string) ([]Entrance, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT entrance_id, COALESCE(name,''), is_accessible, ST_X(geom), ST_Y(geom), nav_node_id
		FROM building_entrances WHERE building_id = $1 ORDER BY entrance_id`, buildingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entrance{}
	for rows.Next() {
		var e Entrance
		if err := rows.Scan(&e.EntranceID, &e.Name, &e.IsAccessible, &e.Lng, &e.Lat, &e.NavNodeID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BuildingGeometry 返回建筑轮廓 + 出入口的 FeatureCollection。
func (r *Repo) BuildingGeometry(ctx context.Context, buildingID string) (json.RawMessage, error) {
	var footprint string
	err := r.pool.QueryRow(ctx,
		`SELECT ST_AsGeoJSON(footprint)::text FROM buildings WHERE building_id=$1`, buildingID).Scan(&footprint)
	if err == pgx.ErrNoRows {
		return nil, httpx.NotFound(fmt.Sprintf("建筑 %s 不存在", buildingID))
	}
	if err != nil {
		return nil, err
	}
	fc := geo.NewFeatureCollection()
	fc.Add(json.RawMessage(footprint), map[string]any{
		"building_id": buildingID,
		"kind":        "footprint",
	})
	entrances, err := r.Entrances(ctx, buildingID)
	if err != nil {
		return nil, err
	}
	for _, e := range entrances {
		fc.Add(geo.Point(e.Lng, e.Lat), map[string]any{
			"entrance_id":   e.EntranceID,
			"kind":          "entrance",
			"name":          e.Name,
			"is_accessible": e.IsAccessible,
		})
	}
	return fc.Marshal(), nil
}

// FloorFeatures 返回某楼层室内要素的 FeatureCollection；kinds 为空返回全部。
func (r *Repo) FloorFeatures(ctx context.Context, floorID int64, kinds []string) (json.RawMessage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT feature_id, kind, COALESCE(category,''), COALESCE(name,''), COALESCE(room_code,''),
		       COALESCE(props::text,'{}'), ST_AsGeoJSON(geom)::text
		FROM indoor_features
		WHERE floor_id = $1 AND ($2::text[] IS NULL OR kind = ANY($2))
		ORDER BY kind, COALESCE(room_code, name, feature_id::text)`, floorID, kindsOrNil(kinds))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fc := geo.NewFeatureCollection()
	for rows.Next() {
		var fid int64
		var kind, category, name, roomCode, propsRaw, geomRaw string
		if err := rows.Scan(&fid, &kind, &category, &name, &roomCode, &propsRaw, &geomRaw); err != nil {
			return nil, err
		}
		props := map[string]any{}
		if err := json.Unmarshal([]byte(propsRaw), &props); err != nil {
			return nil, fmt.Errorf("feature %d props 非法 JSON: %w", fid, err)
		}
		props["feature_id"] = fid
		props["kind"] = kind
		setIfNotEmpty(props, "category", category)
		setIfNotEmpty(props, "name", name)
		setIfNotEmpty(props, "room_code", roomCode)
		fc.Add(json.RawMessage(geomRaw), props)
	}
	return fc.Marshal(), rows.Err()
}

type POI struct {
	POIID        int64    `json:"poi_id"`
	Name         string   `json:"name"`
	Category     string   `json:"category,omitempty"`
	Keywords     []string `json:"keywords,omitempty"`
	BuildingID   string   `json:"building_id,omitempty"`
	BuildingName string   `json:"building_name,omitempty"`
	FloorID      *int64   `json:"floor_id,omitempty"`
	FloorName    string   `json:"floor_name,omitempty"`
	Lng          float64  `json:"lng"`
	Lat          float64  `json:"lat"`
	NavNodeID    *int64   `json:"nav_node_id,omitempty"`
}

// SearchPOIs 地点搜索：关键字匹配名称/分类/别名，可按建筑过滤。
func (r *Repo) SearchPOIs(ctx context.Context, q, buildingID, category string, limit int) ([]POI, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT p.poi_id, p.name, COALESCE(p.category,''), p.keywords,
		       COALESCE(p.building_id,''), COALESCE(b.name,''),
		       p.floor_id, COALESCE(f.display_name,''),
		       ST_X(p.location), ST_Y(p.location), p.nav_node_id
		FROM pois p
		LEFT JOIN buildings b ON b.building_id = p.building_id
		LEFT JOIN floors f ON f.floor_id = p.floor_id
		WHERE ($1 = '' OR p.name ILIKE '%'||$1||'%'
		            OR p.category ILIKE '%'||$1||'%'
		            OR EXISTS (SELECT 1 FROM unnest(p.keywords) k WHERE k ILIKE '%'||$1||'%'))
		  AND ($2 = '' OR p.building_id = $2)
		  AND ($3 = '' OR p.category = $3)
		ORDER BY (p.name ILIKE $4) DESC, p.name
		LIMIT $5`, q, buildingID, category, q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []POI{}
	for rows.Next() {
		var p POI
		if err := rows.Scan(&p.POIID, &p.Name, &p.Category, &p.Keywords,
			&p.BuildingID, &p.BuildingName, &p.FloorID, &p.FloorName,
			&p.Lng, &p.Lat, &p.NavNodeID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repo) GetPOI(ctx context.Context, poiID int64) (*POI, error) {
	var p POI
	err := r.pool.QueryRow(ctx, `
		SELECT p.poi_id, p.name, COALESCE(p.category,''), p.keywords,
		       COALESCE(p.building_id,''), COALESCE(b.name,''),
		       p.floor_id, COALESCE(f.display_name,''),
		       ST_X(p.location), ST_Y(p.location), p.nav_node_id
		FROM pois p
		LEFT JOIN buildings b ON b.building_id = p.building_id
		LEFT JOIN floors f ON f.floor_id = p.floor_id
		WHERE p.poi_id = $1`, poiID).
		Scan(&p.POIID, &p.Name, &p.Category, &p.Keywords,
			&p.BuildingID, &p.BuildingName, &p.FloorID, &p.FloorName,
			&p.Lng, &p.Lat, &p.NavNodeID)
	if err == pgx.ErrNoRows {
		return nil, httpx.NotFound("地点不存在")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func kindsOrNil(kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	return kinds
}

func setIfNotEmpty(props map[string]any, key, val string) {
	if val != "" {
		props[key] = val
	}
}
