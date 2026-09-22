package locate

import "testing"

func TestEstimateByWifiNearest(t *testing.T) {
	// 三个指纹：1F 两个、2F 一个。观测与 F1 位置 A 完全一致。
	cands := []Candidate{
		{SessionID: 1, BuildingID: "B1", FloorID: 1, Lng: 118.7170, Lat: 32.2060,
			Obs: map[string]int{"aa:00:00:00:00:01": -50, "aa:00:00:00:00:02": -60}},
		{SessionID: 2, BuildingID: "B1", FloorID: 1, Lng: 118.7172, Lat: 32.2062,
			Obs: map[string]int{"aa:00:00:00:00:01": -60, "aa:00:00:00:00:02": -50}},
		{SessionID: 3, BuildingID: "B1", FloorID: 2, Lng: 118.7170, Lat: 32.2060,
			Obs: map[string]int{"aa:00:00:00:00:01": -55, "aa:00:00:00:00:02": -55}},
	}
	obs := map[string]int{"aa:00:00:00:00:01": -50, "aa:00:00:00:00:02": -60}

	est := EstimateByWifi(obs, cands, EstimateParams{K: 3, MinMatchedAPs: 2, MissingPenaltyDB: 12})
	if !est.Found {
		t.Fatalf("应定位成功: %+v", est)
	}
	if est.BuildingID != "B1" || est.FloorID != 1 {
		t.Fatalf("楼栋/楼层错误: %s/%d", est.BuildingID, est.FloorID)
	}
	// 最近邻距离为 0，估计位置应非常接近指纹 1
	if est.Lng < 118.7169 || est.Lng > 118.7171 {
		t.Fatalf("经度估计偏离: %v", est.Lng)
	}
	if est.MatchedAPs != 2 {
		t.Fatalf("matched_aps = %d", est.MatchedAPs)
	}
	if est.Label != "high" && est.Label != "medium" {
		t.Fatalf("可信度标签异常: %s (%v)", est.Label, est.Confidence)
	}
}

func TestEstimateByWifiNoMatch(t *testing.T) {
	obs := map[string]int{"bb:00:00:00:00:01": -50}
	est := EstimateByWifi(obs, nil, EstimateParams{K: 4, MinMatchedAPs: 2, MissingPenaltyDB: 12})
	if est.Found {
		t.Fatal("无候选时不应返回定位")
	}
	if est.Reason == "" {
		t.Fatal("应给出失败原因")
	}
}

func TestEstimateMissingAPPenalty(t *testing.T) {
	// 完全命中的指纹应比单边缺 AP 的指纹更近
	cands := []Candidate{
		{SessionID: 1, BuildingID: "B1", FloorID: 1, Lng: 1, Lat: 1,
			Obs: map[string]int{"a:1": -50}},
		{SessionID: 2, BuildingID: "B1", FloorID: 2, Lng: 2, Lat: 2,
			Obs: map[string]int{"a:1": -50, "a:2": -50}},
	}
	obs := map[string]int{"a:1": -50, "a:2": -50}
	est := EstimateByWifi(obs, cands, EstimateParams{K: 1, MinMatchedAPs: 1, MissingPenaltyDB: 12})
	if len(est.Neighbors) == 0 || est.Neighbors[0].SessionID != 2 {
		t.Fatalf("最近邻应为指纹 2（完全命中）: %+v", est.Neighbors)
	}
}
