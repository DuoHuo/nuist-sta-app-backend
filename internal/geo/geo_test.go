package geo

import (
	"encoding/json"
	"math"
	"testing"
)

func TestLocalCSRoundTrip(t *testing.T) {
	for _, rotation := range []float64{0, 15, -30, 90} {
		cs := LocalCS{OriginLon: 118.7170, OriginLat: 32.2060, RotationDeg: rotation}
		for _, p := range [][2]float64{{0, 0}, {120.5, -80.25}, {-300, 420}} {
			lon, lat := cs.ToWGS84(p[0], p[1])
			x, y := cs.ToLocalMeters(lon, lat)
			if math.Abs(x-p[0]) > 1e-6 || math.Abs(y-p[1]) > 1e-6 {
				t.Fatalf("rotation=%v 点 %v: 逆变换得到 (%v,%v)", rotation, p, x, y)
			}
		}
	}
}

func TestLocalCSScale(t *testing.T) {
	cs := LocalCS{OriginLon: 118.7170, OriginLat: 32.2060}
	// 平面近似与球面真值的固有偏差 < 1.5%（100m 内 < 1.5m，对室内定位与
	// 指纹一致性无影响：全链路使用同一变换）
	lon, lat := cs.ToWGS84(100, 0)
	dx := HaversineM(cs.OriginLon, cs.OriginLat, lon, lat)
	if math.Abs(dx-100) > 1.5 {
		t.Fatalf("东向 100m 实算 %.2fm", dx)
	}
	_, lat2 := cs.ToWGS84(0, 100)
	dy := HaversineM(cs.OriginLon, cs.OriginLat, cs.OriginLon, lat2)
	if math.Abs(dy-100) > 1.5 {
		t.Fatalf("北向 100m 实算 %.2fm", dy)
	}
}

func TestHaversineKnownDistance(t *testing.T) {
	// 北京 -> 上海 约 1067 km
	d := HaversineM(116.4074, 39.9042, 121.4737, 31.2304)
	if d < 1050_000 || d > 1085_000 {
		t.Fatalf("北京-上海距离 %.0fm，超出预期范围", d)
	}
}

func TestGeoJSONLineString(t *testing.T) {
	raw := LineString([][]float64{{118.1, 32.1}, {118.2, 32.2}})
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "LineString" {
		t.Fatalf("type = %v", m["type"])
	}
}
