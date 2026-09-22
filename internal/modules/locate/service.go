package locate

import (
	"math"
	"sort"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/geo"
)

// Neighbor 最近邻输出（调试与可信度评估用）。
type Neighbor struct {
	SessionID  int64   `json:"session_id"`
	Distance   float64 `json:"distance"`
	Weight     float64 `json:"weight"`
	BuildingID string  `json:"building_id"`
	FloorID    int64   `json:"floor_id"`
	Lng        float64 `json:"lng"`
	Lat        float64 `json:"lat"`
}

// Estimate 定位结果。
type Estimate struct {
	Found       bool
	Reason      string
	BuildingID  string
	FloorID     int64
	Lng, Lat    float64
	Confidence  float64 // 0~1
	Label       string  // high | medium | low
	Neighbors   []Neighbor
	MatchedAPs  int
}

type EstimateParams struct {
	K                int
	MinMatchedAPs    int
	MissingPenaltyDB float64
}

// EstimateByWifi 加权最近邻（WKNN）基线。
//
// 距离定义：对客户端观测与指纹的 BSSID 并集逐个比较 RSSI 差；
// 单边缺失的 AP 记固定惩罚（missingPenaltyDB），比硬补 -100dBm 更稳定。
// 位置 = 最近 k 个指纹的加权均值；楼栋/楼层 = 权重投票；
// 可信度综合"楼层票选占比"与"近邻位置离散度"，宁可标低不虚高。
func EstimateByWifi(observed map[string]int, cands []Candidate, p EstimateParams) Estimate {
	est := Estimate{Neighbors: []Neighbor{}}
	if p.K <= 0 {
		p.K = 4
	}

	matched := map[string]bool{}
	for _, c := range cands {
		for b := range c.Obs {
			if _, ok := observed[b]; ok {
				matched[b] = true
			}
		}
	}
	est.MatchedAPs = len(matched)

	if len(observed) == 0 {
		est.Reason = "没有可用观测"
		return est
	}
	if est.MatchedAPs < p.MinMatchedAPs {
		est.Reason = "命中的 AP 数不足（指纹库覆盖不到当前位置或 AP 变动）"
		return est
	}

	type scored struct {
		cand Candidate
		dist float64
	}
	scoredList := make([]scored, 0, len(cands))
	for _, c := range cands {
		total := 0.0
		// 客户端看到的 AP：指纹缺这个 AP 时按惩罚计
		for b, r := range observed {
			if _, ok := c.Obs[b]; !ok {
				total += p.MissingPenaltyDB * p.MissingPenaltyDB
				continue
			}
			d := float64(r - c.Obs[b])
			total += d * d
		}
		// 指纹里有、客户端没看到的 AP：同样按惩罚计
		for b := range c.Obs {
			if _, ok := observed[b]; !ok {
				total += p.MissingPenaltyDB * p.MissingPenaltyDB
			}
		}
		scoredList = append(scoredList, scored{c, math.Sqrt(total)})
	}
	sort.Slice(scoredList, func(i, j int) bool { return scoredList[i].dist < scoredList[j].dist })
	if len(scoredList) > p.K {
		scoredList = scoredList[:p.K]
	}

	var wSum float64
	wLng, wLat := 0.0, 0.0
	type voteKey struct {
		b string
		f int64
	}
	votes := map[voteKey]float64{}
	var neighbors []Neighbor
	for _, s := range scoredList {
		w := 1 / (s.dist + 0.5)
		wSum += w
		wLng += w * s.cand.Lng
		wLat += w * s.cand.Lat
		votes[voteKey{s.cand.BuildingID, s.cand.FloorID}] += w
		neighbors = append(neighbors, Neighbor{
			SessionID: s.cand.SessionID, Distance: round3(s.dist), Weight: round3(w),
			BuildingID: s.cand.BuildingID, FloorID: s.cand.FloorID,
			Lng: s.cand.Lng, Lat: s.cand.Lat,
		})
	}

	// 楼层投票
	var bestKey voteKey
	var bestW float64
	for k, w := range votes {
		if w > bestW {
			bestKey, bestW = k, w
		}
	}
	floorShare := bestW / wSum

	// 位置离散度（米）
	meanLng, meanLat := wLng/wSum, wLat/wSum
	var disp float64
	for i, s := range scoredList {
		w := neighbors[i].Weight
		d := geo.HaversineM(meanLng, meanLat, s.cand.Lng, s.cand.Lat)
		disp += w * d * d
	}
	disp = math.Sqrt(disp / wSum)

	conf := floorShare * (1 / (1 + disp/10))
	if conf > 1 {
		conf = 1
	}
	est.Found = true
	est.BuildingID = bestKey.b
	est.FloorID = bestKey.f
	est.Lng, est.Lat = meanLng, meanLat
	est.Confidence = round3(conf)
	switch {
	case conf >= 0.7:
		est.Label = "high"
	case conf >= 0.4:
		est.Label = "medium"
	default:
		est.Label = "low"
	}
	est.Neighbors = neighbors
	return est
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
