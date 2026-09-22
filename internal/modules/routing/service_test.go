package routing

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }
func i64Ptr(v int64) *int64   { return &v }

func TestBuildRouteSegments(t *testing.T) {
	bID := "B-DEMO-01"
	floors := map[int64]FloorInfo{
		1: {FloorID: 1, BuildingID: bID, LevelIndex: 0, DisplayName: "1F"},
		2: {FloorID: 2, BuildingID: bID, LevelIndex: 1, DisplayName: "2F"},
	}
	names := map[string]string{bID: "示范教学楼"}

	// 路径：室外 gate -> j1 -> 东门外 -> (门) -> 1F走廊 -> 楼梯 -> 2F走廊 -> 房间门
	// 与 pgRouting 一致：每行携带到下一节点的边，末行 edge=-1。
	nodes := []PathNode{
		{Seq: 1, NodeID: 1, EdgeID: 10, Cost: 60, Lng: 118.720, Lat: 32.208, EdgeKind: "walkway"},
		{Seq: 2, NodeID: 2, EdgeID: 11, Cost: 30, Lng: 118.719, Lat: 32.207, EdgeKind: "walkway"},
		{Seq: 3, NodeID: 3, EdgeID: 12, Cost: 8, Lng: 118.718, Lat: 32.206, EdgeKind: "door"},
		{Seq: 4, NodeID: 4, EdgeID: 13, Cost: 20, BuildingID: strPtr(bID), FloorID: i64Ptr(1), Lng: 118.7175, Lat: 32.2060, EdgeKind: "corridor"},
		{Seq: 5, NodeID: 5, EdgeID: 14, Cost: 12, BuildingID: strPtr(bID), FloorID: i64Ptr(1), Lng: 118.7172, Lat: 32.2060, EdgeKind: "stair", FloorChange: true},
		{Seq: 6, NodeID: 6, EdgeID: 15, Cost: 10, BuildingID: strPtr(bID), FloorID: i64Ptr(2), Lng: 118.7172, Lat: 32.2060, EdgeKind: "corridor"},
		{Seq: 7, NodeID: 7, EdgeID: -1, BuildingID: strPtr(bID), FloorID: i64Ptr(2), Lng: 118.7171, Lat: 32.2061},
	}

	res := BuildRoute(nodes, floors, names)

	wantTypes := []string{"outdoor", "indoor", "floor_change", "indoor"}
	if len(res.Segments) != len(wantTypes) {
		t.Fatalf("分段数 = %d（%v），期望 %d", len(res.Segments), segTypes(res), len(wantTypes))
	}
	for i, want := range wantTypes {
		if res.Segments[i].Type != want {
			t.Fatalf("第 %d 段类型 = %s，期望 %s（全部：%v）", i, res.Segments[i].Type, want, segTypes(res))
		}
	}

	// 门边（12m）归入室内段
	if res.Segments[1].LengthM != 8+20 {
		t.Fatalf("室内 1F 段长度 = %v，期望 28（含入口门边）", res.Segments[1].LengthM)
	}
	if res.Segments[1].FloorName != "1F" || res.Segments[1].BuildingName != "示范教学楼" {
		t.Fatalf("室内段楼层/建筑名错误: %+v", res.Segments[1])
	}
	fc := res.Segments[2]
	if fc.Action != "stair" || fc.FromFloorName != "1F" || fc.ToFloorName != "2F" || fc.LengthM != 12 {
		t.Fatalf("换层段错误: %+v", fc)
	}
	if res.TotalLengthM != 90+28+12+10 { // 60+30 | 8+20 | 12 | 10
		t.Fatalf("总长度 = %v，期望 140", res.TotalLengthM)
	}
	if len(res.Steps) != 5 || !strings.Contains(res.Steps[len(res.Steps)-1], "到达目的地") {
		t.Fatalf("步骤数 = %d: %v", len(res.Steps), res.Steps)
	}
	if !strings.Contains(res.Segments[1].Instruction, "进入示范教学楼") {
		t.Fatalf("进入建筑文案缺失: %q", res.Segments[1].Instruction)
	}
}

func TestBuildRoutePureOutdoor(t *testing.T) {
	nodes := []PathNode{
		{NodeID: 1, EdgeID: 1, Cost: 7, Lng: 1, Lat: 1, EdgeKind: "walkway"},
		{NodeID: 2, EdgeID: 2, Cost: 3, Lng: 1.001, Lat: 1, EdgeKind: "walkway"},
		{NodeID: 3, EdgeID: -1, Lng: 1.002, Lat: 1},
	}
	res := BuildRoute(nodes, map[int64]FloorInfo{}, map[string]string{})
	if len(res.Segments) != 1 || res.Segments[0].Type != "outdoor" || res.TotalLengthM != 10 {
		t.Fatalf("纯室外路线分段错误: %+v", res.Segments)
	}
}

func TestBuildRouteEmpty(t *testing.T) {
	res := BuildRoute(nil, nil, nil)
	if res == nil || len(res.Segments) != 0 {
		t.Fatal("空输入应返回空结果")
	}
}

func TestBuildRouteOutgoingEdgeGeometry(t *testing.T) {
	nodes := []PathNode{
		{NodeID: 1, EdgeID: 10, Cost: 4, Lng: 1, Lat: 1, EdgeGeom: json.RawMessage(`{"type":"LineString","coordinates":[[1,1],[1.5,1.2],[2,1]]}`)},
		{NodeID: 2, EdgeID: 11, Cost: 7, Lng: 2, Lat: 1, EdgeGeom: json.RawMessage(`{"type":"LineString","coordinates":[[2,1],[2.5,1.3],[3,1]]}`)},
		{NodeID: 3, EdgeID: -1, Lng: 3, Lat: 1},
	}
	res := BuildRoute(nodes, nil, nil)
	if len(res.Segments) != 1 || res.TotalLengthM != 11 {
		t.Fatalf("首尾边应各计一次: %+v", res)
	}
	coords, ok := lineCoords(res.Segments[0].Geometry)
	want := [][]float64{{1, 1}, {1.5, 1.2}, {2, 1}, {2.5, 1.3}, {3, 1}}
	if !ok || !reflect.DeepEqual(coords, want) {
		t.Fatalf("边几何错位: got %v, want %v", coords, want)
	}
}

func TestBuildRouteConsecutiveFloorChanges(t *testing.T) {
	for _, descending := range []bool{false, true} {
		t.Run(fmt.Sprintf("descending=%t", descending), func(t *testing.T) {
			floors := map[int64]FloorInfo{}
			var nodes []PathNode
			for i := 0; i < 7; i++ {
				fid := int64(i + 1)
				if descending {
					fid = int64(7 - i)
				}
				floors[fid] = FloorInfo{FloorID: fid, BuildingID: "B", LevelIndex: int(fid - 1), DisplayName: fmt.Sprintf("%dF", fid)}
				n := PathNode{NodeID: fid, BuildingID: strPtr("B"), FloorID: i64Ptr(fid), Lng: 118, Lat: 32, EdgeID: int64(10 + i), Cost: 10.8, EdgeKind: "stair", FloorChange: true}
				if i == 6 {
					n.EdgeID, n.Cost, n.EdgeKind, n.FloorChange = -1, 0, "", false
				}
				nodes = append(nodes, n)
			}
			res := BuildRoute(nodes, floors, map[string]string{"B": "明德楼"})
			if len(res.Segments) != 6 || res.TotalLengthM != 64.8 {
				t.Fatalf("连续换层丢失或多计: %+v", res)
			}
			for i, s := range res.Segments {
				if s.Type != "floor_change" || s.Action != "stair" || s.FromFloorName != floors[*nodes[i].FloorID].DisplayName || s.ToFloorName != floors[*nodes[i+1].FloorID].DisplayName || s.LengthM != 10.8 {
					t.Fatalf("第 %d 次换层错位: %+v", i, s)
				}
			}
		})
	}
}

func segTypes(r *RouteResult) []string {
	out := []string{}
	for _, s := range r.Segments {
		out = append(out, s.Type)
	}
	return out
}
