package routing

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/geo"
)

// Segment 是返回给 App 的路线分段：室外段 / 室内楼层段 / 换层动作。
type Segment struct {
	Type          string          `json:"type"` // outdoor | indoor | floor_change
	BuildingID    string          `json:"building_id,omitempty"`
	BuildingName  string          `json:"building_name,omitempty"`
	FloorID       int64           `json:"floor_id,omitempty"`
	FloorName     string          `json:"floor_name,omitempty"`
	LevelIndex    int             `json:"level_index,omitempty"`
	Action        string          `json:"action,omitempty"` // stairs | elevator | ramp
	FromFloorName string          `json:"from_floor_name,omitempty"`
	ToFloorName   string          `json:"to_floor_name,omitempty"`
	LengthM       float64         `json:"length_m"`
	Instruction   string          `json:"instruction"`
	Geometry      json.RawMessage `json:"geometry"`
}

type RouteResult struct {
	TotalLengthM float64   `json:"total_length_m"`
	Segments     []Segment `json:"segments"`
	Steps        []string  `json:"steps"`
	OriginNodeID int64     `json:"origin_node_id"`
	DestNodeID   int64     `json:"dest_node_id"`
}

// segContext 标识一个分段所处的"建筑+楼层"，室外为 ("", -1)。
type segContext struct {
	building string
	floorID  int64
}

func ctxOf(n *PathNode) segContext {
	if n.BuildingID == nil || n.FloorID == nil {
		return segContext{building: "", floorID: -1}
	}
	return segContext{building: *n.BuildingID, floorID: *n.FloorID}
}

var actionNames = map[string]string{
	"stair":    "楼梯",
	"elevator": "电梯",
	"ramp":     "坡道",
}

type segBuilder struct {
	ctx             segContext
	coords          [][]float64
	length          float64
	firstInBuilding bool
}

func (b *segBuilder) appendLine(tail [][]float64) {
	for _, p := range tail {
		if len(b.coords) > 0 {
			last := b.coords[len(b.coords)-1]
			if math.Abs(last[0]-p[0]) < 1e-9 && math.Abs(last[1]-p[1]) < 1e-9 {
				continue
			}
		}
		b.coords = append(b.coords, p)
	}
}

// BuildRoute 把 pgRouting 节点序列切成 分段路线 + 文字步骤。
// 纯函数，便于单元测试。
func BuildRoute(nodes []PathNode, floors map[int64]FloorInfo, buildingNames map[string]string) *RouteResult {
	res := &RouteResult{
		Segments: []Segment{},
		Steps:    []string{},
	}
	if len(nodes) == 0 {
		return res
	}
	res.OriginNodeID = nodes[0].NodeID
	res.DestNodeID = nodes[len(nodes)-1].NodeID

	floorName := func(fid int64) string {
		if f, ok := floors[fid]; ok {
			return f.DisplayName
		}
		return "未知楼层"
	}
	bName := func(id string) string {
		if n, ok := buildingNames[id]; ok {
			return n
		}
		return id
	}

	var cur *segBuilder
	lastSegType := "" // 用于生成"进入建筑"类文案

	closeCur := func() {
		if cur == nil {
			return
		}
		if cur.length < 0.01 || len(cur.coords) < 2 {
			// 零长度段（如起点紧邻换层边）不输出
			cur = nil
			return
		}
		seg := Segment{
			Type:       "outdoor",
			LengthM:    round1(cur.length),
			Geometry:   geo.LineString(cur.coords),
			BuildingID: cur.ctx.building,
		}
		if cur.ctx.building != "" {
			seg.Type = "indoor"
			seg.FloorID = cur.ctx.floorID
			seg.FloorName = floorName(cur.ctx.floorID)
			seg.BuildingName = bName(cur.ctx.building)
			if f, ok := floors[cur.ctx.floorID]; ok {
				seg.LevelIndex = f.LevelIndex
			}
			if cur.firstInBuilding {
				seg.Instruction = fmt.Sprintf("进入%s（%s），步行 %s 米", seg.BuildingName, seg.FloorName, meters(cur.length))
			} else {
				seg.Instruction = fmt.Sprintf("在 %s 步行 %s 米", seg.FloorName, meters(cur.length))
			}
		} else {
			seg.BuildingID = ""
			seg.Instruction = fmt.Sprintf("沿校园道路步行 %s 米", meters(cur.length))
		}
		res.Segments = append(res.Segments, seg)
		lastSegType = seg.Type
		cur = nil
	}

	for i := 1; i < len(nodes); i++ {
		prev, next := nodes[i-1], nodes[i]
		// 每条出边只计一次，汇总原始费用后再统一取整。
		res.TotalLengthM += prev.Cost
		prevCtx, nextCtx := ctxOf(&prev), ctxOf(&next)
		prevPt := []float64{prev.Lng, prev.Lat}

		if cur == nil {
			cur = &segBuilder{ctx: prevCtx, coords: [][]float64{prevPt}}
			// 上一段不是同一建筑的室内段 -> 视为"刚进入"
			cur.firstInBuilding = prevCtx.building != "" && lastSegType != "indoor"
		}

		// 换层动作单独成段
		// pgRouting 的出边在 prev 行，next 行仅提供这条边的到达节点。
		if prev.FloorChange {
			closeCur()
			action := prev.EdgeKind
			name := actionNames[action]
			if name == "" {
				name = action
			}
			res.Segments = append(res.Segments, Segment{
				Type:          "floor_change",
				BuildingID:    nextCtx.building,
				BuildingName:  bName(nextCtx.building),
				Action:        action,
				FromFloorName: floorName(prevCtx.floorID),
				ToFloorName:   floorName(nextCtx.floorID),
				LengthM:       round1(prev.Cost),
				Instruction:   fmt.Sprintf("经%s从 %s 前往 %s", name, floorName(prevCtx.floorID), floorName(nextCtx.floorID)),
				Geometry:      geo.LineString([][]float64{{prev.Lng, prev.Lat}, {next.Lng, next.Lat}}),
			})
			lastSegType = "floor_change"
			continue
		}

		// 室内 <-> 室外 / 换建筑：切换分段（跨门槛边归入新段）
		if prevCtx != nextCtx {
			wasIndoor := lastSegType == "indoor"
			closeCur()
			cur = &segBuilder{
				ctx:             nextCtx,
				coords:          [][]float64{prevPt},
				firstInBuilding: nextCtx.building != "" && !wasIndoor,
			}
		}

		// 累加边几何
		if len(prev.EdgeGeom) > 0 {
			if tail, ok := lineCoords(prev.EdgeGeom); ok {
				cur.appendLine(tail)
			} else {
				cur.appendLine([][]float64{{next.Lng, next.Lat}})
			}
		} else {
			cur.appendLine([][]float64{{next.Lng, next.Lat}})
		}
		cur.length += prev.Cost
	}
	closeCur()

	for _, s := range res.Segments {
		res.Steps = append(res.Steps, s.Instruction)
	}
	res.Steps = append(res.Steps, "到达目的地")
	res.TotalLengthM = round1(res.TotalLengthM)
	return res
}

// lineCoords 从 GeoJSON LineString 里取坐标。
func lineCoords(raw json.RawMessage) ([][]float64, bool) {
	var g struct {
		Type        string      `json:"type"`
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil || g.Type != "LineString" || len(g.Coordinates) == 0 {
		return nil, false
	}
	return g.Coordinates, true
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func meters(v float64) string {
	return fmt.Sprintf("%.0f", v)
}
