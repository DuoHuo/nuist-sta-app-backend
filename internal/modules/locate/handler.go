package locate

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

var bssidRe = regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)

type Handler struct {
	repo *Repo
	cfg  *config.Config
}

func Register(rg *gin.RouterGroup, cfg *config.Config, pool *pgxpool.Pool) {
	h := &Handler{repo: NewRepo(pool), cfg: cfg}
	rg.POST("/fingerprints", h.requireToken, h.uploadFingerprint)
	rg.GET("/fingerprints", h.listFingerprints)
	rg.POST("/locate/wifi", h.locateWifi)
}

// requireToken：配置了 collect_token 时，写接口必须携带 X-Collect-Token。
func (h *Handler) requireToken(c *gin.Context) {
	if h.cfg.Server.CollectToken == "" {
		c.Next()
		return
	}
	if c.GetHeader("X-Collect-Token") != h.cfg.Server.CollectToken {
		httpx.Err(c, http.StatusUnauthorized, "unauthorized", "缺少或错误的 X-Collect-Token")
		return
	}
	c.Next()
}

type obsJSON struct {
	BSSID   string `json:"bssid" binding:"required"`
	SSID    string `json:"ssid"`
	RSSI    int    `json:"rssi" binding:"required"`
	FreqMHz *int   `json:"freq_mhz"`
}

type fingerprintReq struct {
	BuildingID     string    `json:"building_id" binding:"required"`
	LevelIndex     *int      `json:"level_index" binding:"required"`
	XM             *float64  `json:"x_m"`
	YM             *float64  `json:"y_m"`
	Lng            *float64  `json:"lng"`
	Lat            *float64  `json:"lat"`
	CapturedAt     *time.Time `json:"captured_at"`
	DeviceModel    string    `json:"device_model"`
	OrientationDeg *float64  `json:"orientation_deg"`
	Note           string    `json:"note"`
	Observations   []obsJSON `json:"observations" binding:"required,min=1,dive"`
}

// uploadFingerprint 采集端上报一次指纹。
func (h *Handler) uploadFingerprint(c *gin.Context) {
	var req fingerprintReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误: "+err.Error())
		return
	}
	ctx := c.Request.Context()

	if req.XM != nil && req.YM == nil || req.XM == nil && req.YM != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "x_m 与 y_m 必须成对提供")
		return
	}
	if req.Lng != nil && req.Lat == nil || req.Lng == nil && req.Lat != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "lng 与 lat 必须成对提供")
		return
	}
	if req.XM == nil && req.Lng == nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "需要提供 x_m+y_m（米制）或 lng+lat 之一标记采集位置")
		return
	}

	// 统一换算为经纬度入库（米制原值也保存）
	var lng, lat float64
	if req.XM != nil {
		cs, err := h.repo.CampusCS(ctx)
		if err != nil {
			httpx.Respond(c, err)
			return
		}
		lng, lat = cs.ToWGS84(*req.XM, *req.YM)
	} else {
		lng, lat = *req.Lng, *req.Lat
	}

	obs := make([]ObsInput, 0, len(req.Observations))
	for _, o := range req.Observations {
		if !bssidRe.MatchString(o.BSSID) {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "BSSID 格式错误: "+o.BSSID)
			return
		}
		if o.RSSI > 0 || o.RSSI < -100 {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "RSSI 应在 -100..0 dBm")
			return
		}
		obs = append(obs, ObsInput{BSSID: o.BSSID, SSID: o.SSID, RSSI: o.RSSI, FreqMHz: o.FreqMHz})
	}

	floorID, err := h.repo.ResolveFloor(ctx, req.BuildingID, *req.LevelIndex)
	if err != nil {
		httpx.Respond(c, err)
		return
	}

	in := InsertInput{
		BuildingID:     req.BuildingID,
		FloorID:        floorID,
		XM:             req.XM,
		YM:             req.YM,
		Lng:            lng,
		Lat:            lat,
		DeviceModel:    req.DeviceModel,
		OrientationDeg: req.OrientationDeg,
		Note:           req.Note,
		Observations:   obs,
	}
	if req.CapturedAt != nil {
		in.CapturedAt = *req.CapturedAt
	} else {
		in.CapturedAt = time.Now()
	}

	sessionID, err := h.repo.InsertSession(ctx, in)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"session_id": sessionID, "obs_count": len(obs)})
}

func (h *Handler) listFingerprints(c *gin.Context) {
	var floorID *int64
	if v := c.Query("floor_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "floor_id 应为数字")
			return
		}
		floorID = &id
	}
	limit := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	list, err := h.repo.ListSessions(c.Request.Context(), c.Query("building_id"), floorID, limit)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"fingerprints": list, "count": len(list)})
}

type locateReq struct {
	Observations   []obsJSON `json:"observations" binding:"required,min=1,dive"`
	HintBuildingID string    `json:"hint_building_id"`
	HintLevelIndex *int      `json:"hint_level_index"`
	K              int       `json:"k"`
}

func (h *Handler) locateWifi(c *gin.Context) {
	var req locateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误: "+err.Error())
		return
	}
	ctx := c.Request.Context()

	observed := map[string]int{}
	bssids := make([]string, 0, len(req.Observations))
	for _, o := range req.Observations {
		if !bssidRe.MatchString(o.BSSID) {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "BSSID 格式错误: "+o.BSSID)
			return
		}
		if o.RSSI > 0 || o.RSSI < -100 {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "RSSI 应在 -100..0 dBm")
			return
		}
		if _, dup := observed[o.BSSID]; !dup {
			bssids = append(bssids, o.BSSID)
		}
		observed[o.BSSID] = o.RSSI
	}

	// 提示信息（可选）：缩小候选范围
	var floorID *int64
	if req.HintBuildingID != "" && req.HintLevelIndex != nil {
		id, err := h.repo.ResolveFloor(ctx, req.HintBuildingID, *req.HintLevelIndex)
		if err != nil {
			httpx.Respond(c, err)
			return
		}
		floorID = &id
	}

	cands, err := h.repo.Candidates(ctx, bssids, req.HintBuildingID)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	if floorID != nil {
		filtered := cands[:0]
		for _, c := range cands {
			if c.FloorID == *floorID {
				filtered = append(filtered, c)
			}
		}
		cands = filtered
	}

	k := h.cfg.Locate.K
	if req.K > 0 && req.K <= 20 {
		k = req.K
	}
	est := EstimateByWifi(observed, cands, EstimateParams{
		K:                k,
		MinMatchedAPs:    h.cfg.Locate.MinMatchedAPs,
		MissingPenaltyDB: h.cfg.Locate.MissingPenaltyDB,
	})

	resp := gin.H{
		"found":         est.Found,
		"neighbors":     est.Neighbors,
		"matched_aps":   est.MatchedAPs,
		"observed_aps":  len(observed),
		"candidate_num": len(cands),
		"note":          "WKNN 基线结果：低可信度时请引导用户确认位置，不要直接绘制精确蓝点",
	}
	if !est.Found {
		resp["reason"] = est.Reason
		httpx.OK(c, resp)
		return
	}
	resp["estimate"] = gin.H{
		"building_id": est.BuildingID,
		"floor_id":    est.FloorID,
		"lng":         math.Round(est.Lng*1e6) / 1e6,
		"lat":         math.Round(est.Lat*1e6) / 1e6,
		"confidence":  est.Confidence,
		"label":       est.Label,
	}
	httpx.OK(c, resp)
}
