package geo

import "encoding/json"

// GeoJSON 构造辅助：模块统一用 ST_AsGeoJSON / 结构体输出要素，
// 这里只补少量 Go 侧需要的构造函数。

func Point(lng, lat float64) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type":        "Point",
		"coordinates": []float64{lng, lat},
	})
	return b
}

func LineString(coords [][]float64) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type":        "LineString",
		"coordinates": coords,
	})
	return b
}

// Feature / FeatureCollection 用于室内要素与建筑轮廓输出。
type Feature struct {
	Type       string                 `json:"type"`
	Geometry   json.RawMessage        `json:"geometry"`
	Properties map[string]any         `json:"properties"`
}

type FeatureCollection struct {
	Type     string    `json:"type"`
	Features []Feature `json:"features"`
}

func NewFeatureCollection() *FeatureCollection {
	return &FeatureCollection{Type: "FeatureCollection", Features: []Feature{}}
}

func (fc *FeatureCollection) Add(geometry json.RawMessage, props map[string]any) {
	fc.Features = append(fc.Features, Feature{
		Type:       "Feature",
		Geometry:   geometry,
		Properties: props,
	})
}

func (fc *FeatureCollection) Marshal() json.RawMessage {
	b, _ := json.Marshal(fc)
	return b
}
