// Package admin 后台管理接口：数据统计、全量图层 GeoJSON、建筑/POI 元数据编辑。
// 写接口复用 locate 模块的 X-Collect-Token 约定（配置为空则不校验，仅供内网联调）。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Stats 概览统计。
type Stats struct {
	Buildings         int64    `json:"buildings"`
	BuildingsNamed    int64    `json:"buildings_named"`
	BuildingsHeight   int64    `json:"buildings_with_height"`
	BuildingsIndoor   int64    `json:"buildings_with_indoor_map"`
	Floors            int64    `json:"floors"`
	IndoorFeatures    int64    `json:"indoor_features"`
	POIs              int64    `json:"pois"`
	NavNodes          int64    `json:"nav_nodes"`
	NavEdges          int64    `json:"nav_edges"`
	NavEdgesClosed    int64    `json:"nav_edges_closed"`
	Entrances         int64    `json:"entrances"`
	FPSessions        int64    `json:"fp_sessions"`
	FPObservations    int64    `json:"fp_observations"`
	DataExtent        []string `json:"data_extent"` // [minLon,minLat,maxLon,maxLat]，建筑与路网总范围
}

func (r *Repo) Stats(ctx context.Context) (*Stats, error) {
	s := &Stats{}
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM buildings),
			(SELECT count(*) FROM buildings WHERE name NOT LIKE '未命名建筑%'),
			(SELECT count(*) FROM buildings WHERE height_m IS NOT NULL),
			(SELECT count(*) FROM buildings WHERE has_indoor_map),
			(SELECT count(*) FROM floors),
			(SELECT count(*) FROM indoor_features),
			(SELECT count(*) FROM pois),
			(SELECT count(*) FROM nav_nodes),
			(SELECT count(*) FROM nav_edges),
			(SELECT count(*) FROM nav_edges WHERE NOT is_open),
			(SELECT count(*) FROM building_entrances),
			(SELECT count(*) FROM fp_sessions),
			(SELECT count(*) FROM fp_observations),
			(SELECT COALESCE(string_to_array(replace(replace(
				substring(ST_Extent(g)::text FROM 5), ' ', ','), ')', ''), ','), ARRAY[]::text[])
			 FROM (
				SELECT footprint AS g FROM buildings
				UNION ALL SELECT geom FROM nav_nodes) allg)`).
		Scan(&s.Buildings, &s.BuildingsNamed, &s.BuildingsHeight, &s.BuildingsIndoor,
			&s.Floors, &s.IndoorFeatures, &s.POIs, &s.NavNodes, &s.NavEdges,
			&s.NavEdgesClosed, &s.Entrances, &s.FPSessions, &s.FPObservations,
			&s.DataExtent)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// AllBuildingsGeometry 全部建筑轮廓 + 出入口，FeatureCollection（管理端地图底图）。
func (r *Repo) AllBuildingsGeometry(ctx context.Context) (json.RawMessage, error) {
	return r.queryFeatureCollection(ctx, `
		SELECT jsonb_build_object(
			'type', 'FeatureCollection',
			'features', COALESCE(jsonb_agg(feature ORDER BY props->>'name'), '[]'::jsonb))
		FROM (
			SELECT jsonb_build_object(
				'type', 'Feature',
				'id', hashtext(b.building_id),
				'geometry', ST_AsGeoJSON(b.footprint)::jsonb,
				'properties', jsonb_build_object(
					'layer', 'building',
					'building_id', b.building_id,
					'name', b.name,
					'name_en', (SELECT al FROM unnest(b.aliases) al WHERE al ~ '^[A-Za-z0-9 ()&.'' -]+$' LIMIT 1),
					'height_m', b.height_m,
					'height_source', b.height_source,
					'has_indoor_map', b.has_indoor_map)) AS feature,
				jsonb_build_object('name', b.name) AS props
			FROM buildings b
			UNION ALL
			SELECT jsonb_build_object(
				'type', 'Feature',
				'geometry', ST_AsGeoJSON(e.geom)::jsonb,
				'properties', jsonb_build_object(
					'layer', 'entrance',
					'building_id', e.building_id,
					'name', e.name,
					'is_accessible', e.is_accessible)) AS feature,
				jsonb_build_object('name', COALESCE(e.name,'')) AS props
			FROM building_entrances e
		) merged`)
}

// Graph 全量导航图：室外路网边 + 节点 + POI 锚点。
func (r *Repo) Graph(ctx context.Context) (json.RawMessage, error) {
	return r.queryFeatureCollection(ctx, `
		SELECT jsonb_build_object(
			'type', 'FeatureCollection',
			'features', COALESCE(jsonb_agg(feature), '[]'::jsonb))
		FROM (
			SELECT jsonb_build_object(
				'type', 'Feature',
				'id', e.edge_id,
				'geometry', COALESCE(ST_AsGeoJSON(e.geom)::jsonb,
					ST_AsGeoJSON(ST_MakeLine(n1.geom, n2.geom))::jsonb),
				'properties', jsonb_build_object(
					'layer', 'edge',
					'edge_kind', e.edge_kind,
					'name', e.name,
					'cost_m', e.cost_m,
					'is_open', e.is_open,
					'is_accessible', e.is_accessible)) AS feature
			FROM nav_edges e
			JOIN nav_nodes n1 ON n1.node_id = e.source
			JOIN nav_nodes n2 ON n2.node_id = e.target
			UNION ALL
			SELECT jsonb_build_object(
				'type', 'Feature',
				'id', n.node_id,
				'geometry', ST_AsGeoJSON(n.geom)::jsonb,
				'properties', jsonb_build_object(
					'layer', 'node',
					'kind', n.kind,
					'building_id', n.building_id)) AS feature
			FROM nav_nodes n
		) merged`)
}

// AllPOIs 全部 POI（地图打点用，无条数上限——校园量级 <1w）。
func (r *Repo) AllPOIs(ctx context.Context) (json.RawMessage, error) {
	return r.queryFeatureCollection(ctx, `
		SELECT jsonb_build_object(
			'type', 'FeatureCollection',
			'features', COALESCE(jsonb_agg(feature ORDER BY sort_id), '[]'::jsonb))
		FROM (
			SELECT jsonb_build_object(
				'type', 'Feature',
				'id', poi_id,
				'geometry', ST_AsGeoJSON(location)::jsonb,
				'properties', jsonb_build_object(
					'layer', 'poi',
					'poi_id', p.poi_id,
					'name', p.name,
					'category', p.category,
					'building_id', p.building_id)) AS feature,
				p.poi_id AS sort_id
			FROM pois p
		) merged`)
}

func (r *Repo) queryFeatureCollection(ctx context.Context, sql string) (json.RawMessage, error) {
	var raw []byte
	if err := r.pool.QueryRow(ctx, sql).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return json.RawMessage(`{"type":"FeatureCollection","features":[]}`), nil
		}
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// BuildingPatch 建筑（档案字段）部分更新；nil 表示不改，清空用空串/特殊约定。
type BuildingPatch struct {
	Name         *string   `json:"name"`
	Aliases      *[]string `json:"aliases"`
	HeightM      *float64  `json:"height_m"`      // 传 null 表示清空高度
	ClearHeight  bool      `json:"clear_height"`  // true 时 height_m 置空
	HeightSource *string  `json:"height_source"`  // 空串表示清空
	HasIndoorMap *bool     `json:"has_indoor_map"`
}

var allowedHeightSource = map[string]bool{
	"measured": true, "estimated_floors": true, "display_default": true, "": true,
}

func (r *Repo) UpdateBuilding(ctx context.Context, buildingID string, p BuildingPatch) error {
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		return httpx.Unprocessable("建筑名称不能为空")
	}
	if p.HeightSource != nil && !allowedHeightSource[*p.HeightSource] {
		return httpx.Unprocessable("height_source 只能是 measured / estimated_floors / display_default")
	}
	if p.HeightM != nil && *p.HeightM < 0 {
		return httpx.Unprocessable("height_m 不能为负")
	}
	if (p.HeightM != nil && *p.HeightM > 0) && (p.HeightSource == nil || *p.HeightSource == "") {
		return httpx.Unprocessable("填写高度时必须同时指定 height_source")
	}

	// 逐字段 COALESCE 式更新，保持未提交字段不变。
	// 注意：height_source 有 CHECK 约束（三值之一），清空必须写 NULL 而不是 ''。
	tag, err := r.pool.Exec(ctx, `
		UPDATE buildings SET
			name          = COALESCE($1, name),
			aliases       = COALESCE($2, aliases),
			height_m      = CASE WHEN $4 THEN NULL ELSE COALESCE($3, height_m) END,
			height_source = CASE WHEN $4 THEN NULL
			                      WHEN $5::text IS NULL THEN height_source
			                      WHEN $5::text = '' THEN NULL
			                      ELSE $5::text END,
			has_indoor_map= COALESCE($6, has_indoor_map),
			updated_at    = now()
		WHERE building_id = $7`,
		p.Name, p.Aliases, p.HeightM, p.ClearHeight, p.HeightSource, p.HasIndoorMap, buildingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("建筑不存在")
	}
	return nil
}

// POIPatch 地点部分更新。
type POIPatch struct {
	Name         *string   `json:"name"`
	Category     *string   `json:"category"`
	ClearCategory bool     `json:"clear_category"` // true 时清空分类
	Keywords     *[]string `json:"keywords"`
}

func (r *Repo) UpdatePOI(ctx context.Context, poiID int64, p POIPatch) error {
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		return httpx.Unprocessable("地点名称不能为空")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE pois SET
			name     = COALESCE($1, name),
			category = CASE WHEN $2 THEN NULL ELSE COALESCE(NULLIF($3,''), category) END,
			keywords = COALESCE($4, keywords)
		WHERE poi_id = $5`,
		p.Name, p.ClearCategory, p.Category, p.Keywords, poiID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("地点不存在")
	}
	return nil
}

// POICreate 手工新建地点（管理台地图点选）。
type POICreate struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Keywords []string `json:"keywords"`
	Lng      float64  `json:"lng"`
	Lat      float64  `json:"lat"`
}

func (r *Repo) CreatePOI(ctx context.Context, p POICreate) (int64, error) {
	if strings.TrimSpace(p.Name) == "" {
		return 0, httpx.Unprocessable("地点名称不能为空")
	}
	if p.Lng < -180 || p.Lng > 180 || p.Lat < -90 || p.Lat > 90 {
		return 0, httpx.Unprocessable("坐标超出 WGS84 范围")
	}
	if p.Keywords == nil {
		p.Keywords = []string{}
	}
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO pois (name, category, keywords, location)
		VALUES ($1, NULLIF($2,''), $3, ST_SetSRID(ST_MakePoint($4,$5),4326))
		RETURNING poi_id`,
		p.Name, p.Category, p.Keywords, p.Lng, p.Lat).Scan(&id)
	return id, err
}

func (r *Repo) DeletePOI(ctx context.Context, poiID int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM pois WHERE poi_id = $1`, poiID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("地点不存在")
	}
	return nil
}

// DeleteBuilding 删除建筑：楼层/室内要素/出入口/该建筑路网节点与关联边级联删除。
func (r *Repo) DeleteBuilding(ctx context.Context, buildingID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM buildings WHERE building_id = $1`, buildingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("建筑不存在")
	}
	return nil
}
