// Package routing 基于 pgRouting 的室内外一体化路线规划。
package routing

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// FloorInfo 路线分段所需的楼层信息。
type FloorInfo struct {
	FloorID     int64
	BuildingID  string
	LevelIndex  int
	DisplayName string
}

// PathNode 是 pgr_dijkstra 结果的一行：Edge* 和 Cost 描述从本节点到下一节点
// 所走的边。末行是终点，EdgeID = -1、Cost = 0，没有出边。
// EdgeGeom 已按实际行进方向排列（逆向经过边时反转存储几何）。
type PathNode struct {
	Seq         int
	NodeID      int64
	EdgeID      int64
	Cost        float64
	BuildingID  *string
	FloorID     *int64
	NodeKind    string
	Lng         float64
	Lat         float64
	EdgeKind    string
	EdgeName    string
	FloorChange bool
	EdgeGeom    json.RawMessage // 边自带几何（可空，空则用两端点直线）
}

const pathSQL = `
	SELECT d.seq, d.node, d.edge, d.cost,
	       n.building_id, n.floor_id, n.kind, ST_X(n.geom), ST_Y(n.geom),
	       COALESCE(e.edge_kind,''), COALESCE(e.name,''), COALESCE(e.floor_change,false),
		       ST_AsGeoJSON(CASE WHEN d.node = e.source THEN e.geom ELSE ST_Reverse(e.geom) END)::text
	FROM pgr_dijkstra($1, $2::bigint, $3::bigint, true) d
	JOIN nav_nodes n ON n.node_id = d.node
	LEFT JOIN nav_edges e ON e.edge_id = d.edge
	ORDER BY d.seq`

// Route 调用 pgRouting Dijkstra 计算节点间路径。
func (r *Repo) Route(ctx context.Context, from, to int64, accessibleOnly bool) ([]PathNode, error) {
	edgesSQL := `SELECT edge_id AS id, source, target, cost_m AS cost, reverse_cost_m AS reverse_cost
	             FROM nav_edges WHERE is_open`
	if accessibleOnly {
		edgesSQL += ` AND is_accessible AND edge_kind <> 'stair'`
	}
	rows, err := r.pool.Query(ctx, pathSQL, edgesSQL, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PathNode
	for rows.Next() {
		var n PathNode
		var edgeGeom *string
		if err := rows.Scan(&n.Seq, &n.NodeID, &n.EdgeID, &n.Cost,
			&n.BuildingID, &n.FloorID, &n.NodeKind, &n.Lng, &n.Lat,
			&n.EdgeKind, &n.EdgeName, &n.FloorChange, &edgeGeom); err != nil {
			return nil, err
		}
		if edgeGeom != nil {
			n.EdgeGeom = json.RawMessage(*edgeGeom)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NearestNode 把经纬度吸附到最近的路网节点。
func (r *Repo) NearestNode(ctx context.Context, lng, lat float64) (nodeID int64, distM float64, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT n.node_id,
		       ST_Distance(n.geom::geography, ST_SetSRID(ST_MakePoint($1,$2),4326)::geography)
		FROM nav_nodes n
		ORDER BY n.geom <-> ST_SetSRID(ST_MakePoint($1,$2),4326)
		LIMIT 1`, lng, lat).Scan(&nodeID, &distM)
	if err == pgx.ErrNoRows {
		return 0, 0, httpx.NotFound("导航路网为空")
	}
	return nodeID, distM, err
}

// NodeExists 校验节点存在。
func (r *Repo) NodeExists(ctx context.Context, nodeID int64) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nav_nodes WHERE node_id=$1)`, nodeID).Scan(&ok)
	return ok, err
}

// NodeForPOI 返回 POI 的导航锚点；无锚点则按位置就近吸附。
func (r *Repo) NodeForPOI(ctx context.Context, poiID int64) (int64, error) {
	var navNodeID *int64
	var lng, lat float64
	err := r.pool.QueryRow(ctx,
		`SELECT nav_node_id, ST_X(location), ST_Y(location) FROM pois WHERE poi_id=$1`, poiID).
		Scan(&navNodeID, &lng, &lat)
	if err == pgx.ErrNoRows {
		return 0, httpx.NotFound("地点不存在")
	}
	if err != nil {
		return 0, err
	}
	if navNodeID != nil {
		return *navNodeID, nil
	}
	id, _, err := r.NearestNode(ctx, lng, lat)
	return id, err
}

// NodeForBuilding 返回建筑主入口节点（第一个入口），无则吸附到轮廓质心。
func (r *Repo) NodeForBuilding(ctx context.Context, buildingID string) (int64, error) {
	var navNodeID *int64
	err := r.pool.QueryRow(ctx, `
		SELECT e.nav_node_id FROM building_entrances e
		WHERE e.building_id=$1 AND e.nav_node_id IS NOT NULL
		ORDER BY e.entrance_id LIMIT 1`, buildingID).Scan(&navNodeID)
	if err == nil && navNodeID != nil {
		return *navNodeID, nil
	}
	if err != nil && err != pgx.ErrNoRows {
		return 0, err
	}
	var lng, lat float64
	err = r.pool.QueryRow(ctx, `
		SELECT ST_X(ST_Centroid(footprint)), ST_Y(ST_Centroid(footprint))
		FROM buildings WHERE building_id=$1`, buildingID).Scan(&lng, &lat)
	if err == pgx.ErrNoRows {
		return 0, httpx.NotFound("建筑不存在")
	}
	if err != nil {
		return 0, err
	}
	id, _, err := r.NearestNode(ctx, lng, lat)
	return id, err
}

// NodeForFloor 返回指定建筑指定楼层内距建筑质心最近的路网节点
// （用于"导航到某楼某层"这类粗粒度目的地）。
func (r *Repo) NodeForFloor(ctx context.Context, buildingID string, levelIndex int) (int64, error) {
	var nodeID int64
	err := r.pool.QueryRow(ctx, `
		SELECT n.node_id
		FROM buildings b
		JOIN floors f      ON f.building_id  = b.building_id AND f.level_index = $2
		JOIN nav_nodes n   ON n.floor_id     = f.floor_id
		WHERE b.building_id = $1
		ORDER BY n.geom <-> ST_Centroid(b.footprint)
		LIMIT 1`, buildingID, levelIndex).Scan(&nodeID)
	if err == pgx.ErrNoRows {
		return 0, httpx.NotFound("该楼层不存在或没有路网数据")
	}
	return nodeID, err
}

// FloorsByIDs 批量取楼层信息（含 building -> 名称映射）。
func (r *Repo) FloorsByIDs(ctx context.Context, ids []int64) (map[int64]FloorInfo, error) {
	out := map[int64]FloorInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT floor_id, building_id, level_index, display_name
		FROM floors WHERE floor_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f FloorInfo
		if err := rows.Scan(&f.FloorID, &f.BuildingID, &f.LevelIndex, &f.DisplayName); err != nil {
			return nil, err
		}
		out[f.FloorID] = f
	}
	return out, rows.Err()
}

func (r *Repo) BuildingNames(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT building_id, name FROM buildings WHERE building_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
