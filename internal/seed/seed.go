// Package seed 写入演示数据：一栋示范教学楼（两个楼层）、室内外连通的
// 导航路网、若干 POI、Wi-Fi 指纹样本，以及一条校园公交环线（含一辆车与位置）。
// 幂等，可重复执行。
//
// 坐标为本地米制（锚点见 campus_cs），演示用，正式数据由 QGIS / OSM 流程生产。
package seed

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/geo"
)

const (
	demoBuildingID   = "B-DEMO-01"
	demoBuildingName = "示范教学楼（演示数据）"
	demoBusRouteCode = "DEMO-1"
	demoBusVehicleID = "BUS-DEMO-01"
	originLon        = 118.7170
	originLat        = 32.2060
)

type Summary struct {
	BuildingID   string
	FloorIDs     map[int]int64 // level_index -> floor_id
	NodeCount    int
	EdgeCount    int
	POICount     int
	FPSessions   int
	BusRouteID   int64
	BusStopCount int
}

type nodeDef struct {
	key      string
	building bool // 属于演示建筑
	floor    int  // level_index，室外节点忽略
	x, y     float64
	kind     string
}

type edgeDef struct {
	from, to     string
	kind         string
	name         string
	accessible   bool
	floorChange  bool
	costOverride float64 // >0 时使用（换层边）
}

func demoNodes() []nodeDef {
	return []nodeDef{
		// 室外
		{key: "gate", x: 200, y: 60, kind: "junction"},
		{key: "j1", x: 120, y: 45, kind: "junction"},
		{key: "ent_out", x: 86, y: 30, kind: "entrance"},
		// 1F
		{key: "ent_in", building: true, floor: 0, x: 74, y: 30, kind: "entrance"},
		{key: "c1", building: true, floor: 0, x: 60, y: 30, kind: "junction"},
		{key: "c2", building: true, floor: 0, x: 48, y: 30, kind: "junction"},
		{key: "c3", building: true, floor: 0, x: 36, y: 30, kind: "junction"},
		{key: "c4", building: true, floor: 0, x: 24, y: 30, kind: "junction"},
		{key: "sdoor0", building: true, floor: 0, x: 25, y: 33, kind: "door"},
		{key: "stair_b", building: true, floor: 0, x: 25, y: 36.5, kind: "stair"},
		{key: "edoor0", building: true, floor: 0, x: 31, y: 33, kind: "door"},
		{key: "ev_b", building: true, floor: 0, x: 31, y: 36.5, kind: "elevator"},
		{key: "d101", building: true, floor: 0, x: 30, y: 27, kind: "room_door"},
		{key: "r101", building: true, floor: 0, x: 30, y: 23.5, kind: "room_door"},
		{key: "d102", building: true, floor: 0, x: 46, y: 27, kind: "room_door"},
		{key: "r102", building: true, floor: 0, x: 46, y: 23.5, kind: "room_door"},
		{key: "d103", building: true, floor: 0, x: 66, y: 27, kind: "room_door"},
		{key: "r103", building: true, floor: 0, x: 66, y: 23.5, kind: "room_door"},
		// 2F
		{key: "c1b", building: true, floor: 1, x: 60, y: 30, kind: "junction"},
		{key: "c2b", building: true, floor: 1, x: 48, y: 30, kind: "junction"},
		{key: "c3b", building: true, floor: 1, x: 36, y: 30, kind: "junction"},
		{key: "c4b", building: true, floor: 1, x: 24, y: 30, kind: "junction"},
		{key: "sdoor1", building: true, floor: 1, x: 25, y: 33, kind: "door"},
		{key: "stair_t", building: true, floor: 1, x: 25, y: 36.5, kind: "stair"},
		{key: "edoor1", building: true, floor: 1, x: 31, y: 33, kind: "door"},
		{key: "ev_t", building: true, floor: 1, x: 31, y: 36.5, kind: "elevator"},
		{key: "d201", building: true, floor: 1, x: 30, y: 27, kind: "room_door"},
		{key: "r201", building: true, floor: 1, x: 30, y: 23.5, kind: "room_door"},
		{key: "d202", building: true, floor: 1, x: 46, y: 27, kind: "room_door"},
		{key: "r202", building: true, floor: 1, x: 46, y: 23.5, kind: "room_door"},
		{key: "d203", building: true, floor: 1, x: 66, y: 27, kind: "room_door"},
		{key: "r203", building: true, floor: 1, x: 66, y: 23.5, kind: "room_door"},
		{key: "d204", building: true, floor: 1, x: 56, y: 33, kind: "room_door"},
		{key: "r204", building: true, floor: 1, x: 56, y: 36.5, kind: "room_door"},
	}
}

func demoEdges() []edgeDef {
	return []edgeDef{
		{from: "gate", to: "j1", kind: "walkway", name: "校园步道"},
		{from: "j1", to: "ent_out", kind: "walkway", name: "校园步道"},
		{from: "ent_out", to: "ent_in", kind: "door", name: "东入口"},

		{from: "ent_in", to: "c1", kind: "corridor", name: "一层走廊"},
		{from: "c1", to: "c2", kind: "corridor", name: "一层走廊"},
		{from: "c2", to: "c3", kind: "corridor", name: "一层走廊"},
		{from: "c3", to: "c4", kind: "corridor", name: "一层走廊"},
		{from: "c4", to: "sdoor0", kind: "door", name: "楼梯间门"},
		{from: "sdoor0", to: "stair_b", kind: "corridor"},
		{from: "c3", to: "edoor0", kind: "door", name: "电梯厅门"},
		{from: "edoor0", to: "ev_b", kind: "corridor"},

		{from: "c3", to: "d101", kind: "door", name: "101 门口"},
		{from: "d101", to: "r101", kind: "door"},
		{from: "c2", to: "d102", kind: "door", name: "102 门口"},
		{from: "d102", to: "r102", kind: "door"},
		{from: "c1", to: "d103", kind: "door", name: "103 门口"},
		{from: "d103", to: "r103", kind: "door"},

		{from: "c1b", to: "c2b", kind: "corridor", name: "二层走廊"},
		{from: "c2b", to: "c3b", kind: "corridor", name: "二层走廊"},
		{from: "c3b", to: "c4b", kind: "corridor", name: "二层走廊"},
		{from: "c4b", to: "sdoor1", kind: "door", name: "楼梯间门"},
		{from: "sdoor1", to: "stair_t", kind: "corridor"},
		{from: "c3b", to: "edoor1", kind: "door", name: "电梯厅门"},
		{from: "edoor1", to: "ev_t", kind: "corridor"},

		{from: "c3b", to: "d201", kind: "door", name: "201 门口"},
		{from: "d201", to: "r201", kind: "door"},
		{from: "c2b", to: "d202", kind: "door", name: "202 门口"},
		{from: "d202", to: "r202", kind: "door"},
		{from: "c1b", to: "d203", kind: "door", name: "203 门口"},
		{from: "d203", to: "r203", kind: "door"},
		{from: "c2b", to: "d204", kind: "door", name: "204 门口"},
		{from: "d204", to: "r204", kind: "door"},

		// 跨层连接：只能经楼梯/电梯，二维坐标相同不可直接跨层
		{from: "stair_b", to: "stair_t", kind: "stair", floorChange: true, accessible: false, costOverride: 12},
		{from: "ev_b", to: "ev_t", kind: "elevator", floorChange: true, costOverride: 16},
	}
}

// Run 在一个事务内完成所有写入。
func Run(ctx context.Context, pool *pgxpool.Pool) (*Summary, error) {
	cs := geo.LocalCS{OriginLon: originLon, OriginLat: originLat}
	s := &Summary{FloorIDs: map[int]int64{}}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 0. 坐标系锚点
	if _, err := tx.Exec(ctx, `
		INSERT INTO campus_cs (id, origin_lon, origin_lat, rotation_deg, description)
		VALUES (1, $1, $2, 0, '演示数据锚点')
		ON CONFLICT (id) DO UPDATE
		SET origin_lon = EXCLUDED.origin_lon, origin_lat = EXCLUDED.origin_lat,
		    description = EXCLUDED.description, updated_at = now()`,
		originLon, originLat); err != nil {
		return nil, err
	}

	// 1. 清理旧演示数据（级联删除楼层/要素/路网/指纹）
	if _, err := tx.Exec(ctx,
		`DELETE FROM pois WHERE building_id IS NULL AND 'demo-seed' = ANY(keywords)`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM nav_nodes WHERE building_id IS NULL AND props->>'seed' = 'demo'`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM buildings WHERE building_id = $1`, demoBuildingID); err != nil {
		return nil, err
	}
	// 公交：车辆先删（位置随车辆级联），再删线路（站序随线路级联）
	if _, err := tx.Exec(ctx,
		`DELETE FROM bus_vehicles WHERE vehicle_id = $1`, demoBusVehicleID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM bus_routes WHERE source = 'demo-seed'`); err != nil {
		return nil, err
	}

	// 2. 建筑 + 楼层
	if _, err := tx.Exec(ctx, `
		INSERT INTO buildings (building_id, name, aliases, footprint, height_m, height_source, has_indoor_map, splat_scene_id)
		VALUES ($1, $2, $3, ST_PolygonFromText($4, 4326), 15.5, 'estimated_floors', TRUE, NULL)`,
		demoBuildingID, demoBuildingName, []string{"示范楼", "Demo Building"},
		polygonWKT(cs, 20, 20, 80, 40)); err != nil {
		return nil, fmt.Errorf("插入建筑: %w", err)
	}
	for _, f := range []struct {
		level int
		name  string
		elev  float64
		sort  int
	}{{0, "1F", 0, 10}, {1, "2F", 7.5, 20}} {
		var floorID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO floors (building_id, level_index, display_name, elevation_m, sort_order)
			VALUES ($1,$2,$3,$4,$5) RETURNING floor_id`,
			demoBuildingID, f.level, f.name, f.elev, f.sort).Scan(&floorID); err != nil {
			return nil, fmt.Errorf("插入楼层: %w", err)
		}
		s.FloorIDs[f.level] = floorID
	}
	f0, f1 := s.FloorIDs[0], s.FloorIDs[1]

	// 3. 室内要素
	features := []struct {
		floor          int64
		kind           string
		cat            string
		name           string
		code           string
		x1, y1, x2, y2 float64
	}{}
	addPoly := func(floor int64, kind, cat, name, code string, x1, y1, x2, y2 float64) {
		features = append(features, struct {
			floor          int64
			kind           string
			cat            string
			name           string
			code           string
			x1, y1, x2, y2 float64
		}{floor, kind, cat, name, code, x1, y1, x2, y2})
	}
	for _, fid := range []int64{f0, f1} {
		addPoly(fid, "corridor", "", "中央走廊", "", 22, 27, 78, 33)
		addPoly(fid, "stair", "交通", "楼梯间", "", 22, 33, 28, 40)
		addPoly(fid, "elevator", "交通", "电梯厅", "", 28, 33, 34, 40)
	}
	addPoly(f0, "room", "教室", "101 教室", "101", 22, 20, 38, 27)
	addPoly(f0, "room", "自习室", "102 自习室", "102", 38, 20, 54, 27)
	addPoly(f0, "room", "教室", "103 教室", "103", 54, 20, 78, 27)
	addPoly(f1, "room", "教室", "201 教室", "201", 22, 20, 38, 27)
	addPoly(f1, "room", "会议室", "202 会议室", "202", 38, 20, 54, 27)
	addPoly(f1, "room", "教室", "203 教室", "203", 54, 20, 78, 27)
	addPoly(f1, "room", "机房", "204 机房", "204", 34, 33, 78, 40)

	for _, f := range features {
		if _, err := tx.Exec(ctx, `
			INSERT INTO indoor_features (floor_id, kind, category, name, room_code, geom)
			VALUES ($1,$2,$3,$4,$5,ST_PolygonFromText($6, 4326))`,
			f.floor, f.kind, f.cat, f.name, f.code, polygonWKT(cs, f.x1, f.y1, f.x2, f.y2)); err != nil {
			return nil, fmt.Errorf("插入室内要素 %s: %w", f.name, err)
		}
	}

	// 门点要素（Point）
	doorPts := []struct {
		floor int64
		name  string
		x, y  float64
	}{
		{f0, "东入口", 78, 30},
		{f0, "101 门", 30, 27}, {f0, "102 门", 46, 27}, {f0, "103 门", 66, 27},
		{f1, "201 门", 30, 27}, {f1, "202 门", 46, 27}, {f1, "203 门", 66, 27}, {f1, "204 门", 56, 33},
	}
	for _, d := range doorPts {
		lng, lat := cs.ToWGS84(d.x, d.y)
		if _, err := tx.Exec(ctx, `
			INSERT INTO indoor_features (floor_id, kind, name, geom)
			VALUES ($1,'door',$2, ST_SetSRID(ST_MakePoint($3,$4),4326))`,
			d.floor, d.name, lng, lat); err != nil {
			return nil, fmt.Errorf("插入门点 %s: %w", d.name, err)
		}
	}

	// 4. 出入口
	entLng, entLat := cs.ToWGS84(78, 30)
	if _, err := tx.Exec(ctx, `
		INSERT INTO building_entrances (building_id, name, is_accessible, geom)
		VALUES ($1, '东入口', TRUE, ST_SetSRID(ST_MakePoint($2,$3),4326))`,
		demoBuildingID, entLng, entLat); err != nil {
		return nil, err
	}
	// nav_node_id 稍后回填（节点先插入）

	// 5. 导航节点与边
	nodeIDs := map[string]int64{}
	nodePos := map[string][2]float64{}
	for _, n := range demoNodes() {
		lng, lat := cs.ToWGS84(n.x, n.y)
		var bid any
		var fid any
		if n.building {
			bid = demoBuildingID
			fid = s.FloorIDs[n.floor]
		}
		var id int64
		props := `'{}'::jsonb`
		if !n.building {
			props = `'{"seed":"demo"}'::jsonb`
		}
		if err := tx.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO nav_nodes (building_id, floor_id, kind, geom, props)
			VALUES ($1,$2,$3,ST_SetSRID(ST_MakePoint($4,$5),4326),%s)
			RETURNING node_id`, props),
			bid, fid, n.kind, lng, lat).Scan(&id); err != nil {
			return nil, fmt.Errorf("插入节点 %s: %w", n.key, err)
		}
		nodeIDs[n.key] = id
		nodePos[n.key] = [2]float64{n.x, n.y}
		s.NodeCount++
	}

	for _, e := range demoEdges() {
		cost := e.costOverride
		if cost <= 0 {
			a, b := nodePos[e.from], nodePos[e.to]
			cost = math.Hypot(a[0]-b[0], a[1]-b[1])
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO nav_edges (source, target, cost_m, reverse_cost_m, edge_kind, name, is_open, is_accessible, floor_change)
			VALUES ($1,$2,$3,$3,$4,$5,TRUE,$6,$7)`,
			nodeIDs[e.from], nodeIDs[e.to], round2(cost), e.kind, e.name, e.accessible, e.floorChange); err != nil {
			return nil, fmt.Errorf("插入边 %s->%s: %w", e.from, e.to, err)
		}
		s.EdgeCount++
	}

	// 回填出入口的 nav_node_id
	if _, err := tx.Exec(ctx, `
		UPDATE building_entrances SET nav_node_id = $1
		WHERE building_id = $2 AND name = '东入口'`, nodeIDs["ent_out"], demoBuildingID); err != nil {
		return nil, err
	}

	// 6. POI
	pois := []struct {
		name, cat string
		building  bool
		floor     int64
		navKey    string
		x, y      float64
	}{
		{"101 教室", "教室", true, f0, "r101", 30, 23.5},
		{"102 自习室", "自习室", true, f0, "r102", 46, 23.5},
		{"103 教室", "教室", true, f0, "r103", 66, 23.5},
		{"201 教室", "教室", true, f1, "r201", 30, 23.5},
		{"202 会议室", "会议室", true, f1, "r202", 46, 23.5},
		{"203 教室", "教室", true, f1, "r203", 66, 23.5},
		{"204 机房", "机房", true, f1, "r204", 56, 36.5},
		{"东门（演示）", "地标", false, 0, "gate", 200, 60},
	}
	for _, p := range pois {
		lng, lat := cs.ToWGS84(p.x, p.y)
		var bid, fid any
		kw := []string{p.cat}
		if p.building {
			bid = demoBuildingID
			fid = p.floor
		} else {
			kw = append(kw, "demo-seed")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pois (name, category, keywords, building_id, floor_id, location, nav_node_id)
			VALUES ($1,$2,$3,$4,$5,ST_SetSRID(ST_MakePoint($6,$7),4326),$8)`,
			p.name, p.cat, kw, bid, fid, lng, lat, nodeIDs[p.navKey]); err != nil {
			return nil, fmt.Errorf("插入 POI %s: %w", p.name, err)
		}
		s.POICount++
	}

	// 7. Wi-Fi 指纹样本
	aps := []struct {
		bssid string
		x, y  float64
	}{
		{"02:00:00:00:00:01", 24, 36},
		{"02:00:00:00:00:02", 78, 36},
		{"02:00:00:00:00:03", 24, 20},
		{"02:00:00:00:00:04", 78, 20},
	}
	rssiAt := func(x, y float64, level int, ap struct {
		bssid string
		x, y  float64
	}) int {
		d := math.Hypot(x-ap.x, y-ap.y)
		v := -(42 + 0.85*d + 8*float64(level))
		v = math.Max(v, -92)
		v = math.Min(v, -42)
		return int(math.Round(v))
	}
	sessions := []struct {
		level int
		x, y  float64
	}{
		{0, 36, 30}, {0, 48, 30}, {0, 60, 30}, {0, 30, 23.5}, {0, 66, 23.5},
		{1, 36, 30}, {1, 48, 30}, {1, 60, 30}, {1, 56, 36.5}, {1, 30, 23.5},
	}
	for _, sess := range sessions {
		lng, lat := cs.ToWGS84(sess.x, sess.y)
		var sid int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO fp_sessions (building_id, floor_id, x_m, y_m, geom, device_model, note)
			VALUES ($1,$2,$3,$4,ST_SetSRID(ST_MakePoint($5,$6),4326),'demo-collector','演示指纹')
			RETURNING session_id`,
			demoBuildingID, s.FloorIDs[sess.level], sess.x, sess.y, lng, lat).Scan(&sid); err != nil {
			return nil, err
		}
		for _, ap := range aps {
			if _, err := tx.Exec(ctx, `
				INSERT INTO fp_observations (session_id, bssid, ssid, rssi, freq_mhz)
				VALUES ($1,$2,'CAMPUS-DEMO',$3,2437)`,
				sid, ap.bssid, rssiAt(sess.x, sess.y, sess.level, ap)); err != nil {
				return nil, err
			}
		}
		s.FPSessions++
	}

	// 8. 校园公交演示环线：4 站（环线首末同站）+ 1 辆车 + 1 个新鲜位置。
	//    位置是"每车取最新一条"的语义，所以只插一条就够 App 轮询 /bus/vehicles 看到车。
	busStops := []struct {
		name string
		x, y float64
	}{
		{"东门站（演示）", 200, 60},
		{"示范教学楼站（演示）", 78, 45},
		{"图书馆站（演示）", 40, 90},
		{"宿舍区站（演示）", 140, 120},
	}
	stopIDs := make([]int64, 0, len(busStops))
	for _, st := range busStops {
		lng, lat := cs.ToWGS84(st.x, st.y)
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO bus_stops (name, description, geom, props, status, source)
			VALUES ($1, '演示数据', ST_SetSRID(ST_MakePoint($2,$3),4326), '{"demo":true}'::jsonb, 'published', 'demo-seed')
			RETURNING stop_id`, st.name, lng, lat).Scan(&id); err != nil {
			return nil, fmt.Errorf("插入公交站 %s: %w", st.name, err)
		}
		stopIDs = append(stopIDs, id)
		s.BusStopCount++
	}

	// 线路走向：绕四站一圈，末点回到起点（环线的正常写法）
	loop := [][2]float64{{200, 60}, {160, 40}, {78, 45}, {40, 90}, {140, 120}, {200, 60}}
	coords := make([]string, 0, len(loop))
	for _, p := range loop {
		lng, lat := cs.ToWGS84(p[0], p[1])
		coords = append(coords, fmt.Sprintf("[%.7f,%.7f]", lng, lat))
	}
	routeGeoJSON := `{"type":"LineString","coordinates":[` + strings.Join(coords, ",") + `]}`

	if err := tx.QueryRow(ctx, `
		INSERT INTO bus_routes (code, name, description, color, is_loop, geom, props, status, source)
		VALUES ($1, '校园环线（演示数据）',
		        '演示环线：东门 → 教学楼 → 图书馆 → 宿舍区 → 东门',
		        '#0b7285', TRUE, ST_Multi(feature_geom($2::text)),
		        '{"service_hours":"07:30-21:30","headway_min":10}'::jsonb, 'published', 'demo-seed')
		RETURNING route_id`, demoBusRouteCode, routeGeoJSON).Scan(&s.BusRouteID); err != nil {
		return nil, fmt.Errorf("插入公交线路: %w", err)
	}

	// 站序：下标即 seq；环线首末同站——起点站作为终点再出现一次
	for i, stopID := range append(append([]int64{}, stopIDs...), stopIDs[0]) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO bus_route_stops (route_id, stop_id, seq) VALUES ($1,$2,$3)`,
			s.BusRouteID, stopID, i); err != nil {
			return nil, fmt.Errorf("插入站序 %d: %w", i, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO bus_vehicles (vehicle_id, label, route_id, enabled, props)
		VALUES ($1, '1号车（演示）', $2, TRUE, '{"demo":true}'::jsonb)`,
		demoBusVehicleID, s.BusRouteID); err != nil {
		return nil, fmt.Errorf("插入演示车辆: %w", err)
	}
	posLng, posLat := cs.ToWGS84(120, 80)
	if _, err := tx.Exec(ctx, `
		INSERT INTO bus_positions (vehicle_id, route_id, geom, heading_deg, speed_kmh, source)
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3,$4),4326), 90, 12, 'demo-seed')`,
		demoBusVehicleID, s.BusRouteID, posLng, posLat); err != nil {
		return nil, fmt.Errorf("插入演示车辆位置: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func polygonWKT(cs geo.LocalCS, x1, y1, x2, y2 float64) string {
	pts := [][2]float64{{x1, y1}, {x2, y1}, {x2, y2}, {x1, y2}, {x1, y1}}
	wkt := "POLYGON(("
	for i, p := range pts {
		lng, lat := cs.ToWGS84(p[0], p[1])
		if i > 0 {
			wkt += ", "
		}
		wkt += fmt.Sprintf("%.7f %.7f", lng, lat)
	}
	return wkt + "))"
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
