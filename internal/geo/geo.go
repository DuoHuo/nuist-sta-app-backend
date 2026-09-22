// Package geo 提供 WGS84 <-> 校园本地米制坐标 变换与基础距离计算。
//
// 约定（见 migrations/campus_cs 表）：
//   - 地图与接口交换一律使用 WGS84（EPSG:4326）地理坐标；
//   - 室内距离、指纹采集位置使用本地米制坐标（x 向东、y 向北，单位米）；
//   - 两者之间的变换由 campus_cs 锚点（原点 + 旋转角）唯一确定。
//
// 本地米制采用简单平面近似（校园 ~3km² 内误差可忽略），不引入投影库。
package geo

import "math"

const (
	MetersPerDegLonE = 111320.0 // 赤道经度每度约 111.3 km
)

// 子午线弧长：每度纬度的米数随纬度变化（32°N 约 110.89 km/度）。
func metersPerDegLat(latDeg float64) float64 {
	lat := deg2rad(latDeg)
	return 111132.92 - 559.82*math.Cos(2*lat) + 1.175*math.Cos(4*lat)
}

// 经度弧长：每度经度的米数随纬度变化。
func metersPerDegLon(latDeg float64) float64 {
	lat := deg2rad(latDeg)
	return 111412.84*math.Cos(lat) - 93.5*math.Cos(3*lat) + 0.118*math.Cos(5*lat)
}

// LocalCS 描述本地米制坐标系：原点的经纬度 + 相对正北的旋转角（度）。
type LocalCS struct {
	OriginLon   float64 `json:"origin_lon"`
	OriginLat   float64 `json:"origin_lat"`
	RotationDeg float64 `json:"rotation_deg"`
}

func deg2rad(d float64) float64 { return d * math.Pi / 180 }

func (cs LocalCS) lonMetersPerDeg() float64 {
	return metersPerDegLon(cs.OriginLat)
}

// latMetersPerDeg 以锚点纬度计算每度纬度对应的米数。
func (cs LocalCS) latMetersPerDeg() float64 {
	return metersPerDegLat(cs.OriginLat)
}

// ToWGS84 把本地米制坐标换算为经纬度。
func (cs LocalCS) ToWGS84(xM, yM float64) (lon, lat float64) {
	x, y := xM, yM
	if cs.RotationDeg != 0 {
		th := deg2rad(cs.RotationDeg)
		x = xM*math.Cos(th) + yM*math.Sin(th)
		y = -xM*math.Sin(th) + yM*math.Cos(th)
	}
	lon = cs.OriginLon + x/cs.lonMetersPerDeg()
	lat = cs.OriginLat + y/cs.latMetersPerDeg()
	return lon, lat
}

// ToLocalMeters 把经纬度换算为本地米制坐标（ToWGS84 的逆变换）。
func (cs LocalCS) ToLocalMeters(lon, lat float64) (xM, yM float64) {
	x := (lon - cs.OriginLon) * cs.lonMetersPerDeg()
	y := (lat - cs.OriginLat) * cs.latMetersPerDeg()
	if cs.RotationDeg != 0 {
		th := deg2rad(cs.RotationDeg)
		xM = x*math.Cos(th) - y*math.Sin(th)
		yM = x*math.Sin(th) + y*math.Cos(th)
	} else {
		xM, yM = x, y
	}
	return xM, yM
}

// HaversineM 返回两个 WGS84 坐标间的大圆距离（米）。
func HaversineM(lon1, lat1, lon2, lat2 float64) float64 {
	const r = 6371000.0
	dLat := deg2rad(lat2 - lat1)
	dLon := deg2rad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(deg2rad(lat1))*math.Cos(deg2rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
