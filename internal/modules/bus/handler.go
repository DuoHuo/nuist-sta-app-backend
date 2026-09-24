// Package bus 的 HTTP 层：读接口开放，写接口一律要求 X-Collect-Token。
package bus

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/admintoken"
)

type Handler struct {
	repo *Repo
	cfg  *config.Config
}

// Register 挂载校园公交路由。
//
// 读接口 /bus/* 开放（与建筑、通用地物一致，App 直接取数）；写接口全部走令牌：
//   - 管理台编辑线路/站点/车辆：/admin/bus/*；
//   - 车载设备上报位置：POST /bus/positions——它不是"编辑"，和指纹上报一样是设备写入，
//     所以放在 /bus 下而不是 /admin 下，但同样用 X-Collect-Token 守卫（未配置令牌时 503）。
func Register(rg *gin.RouterGroup, cfg *config.Config, pool *pgxpool.Pool) {
	h := &Handler{repo: NewRepo(pool), cfg: cfg}
	write := admintoken.FromSpec(cfg.Server.CollectToken).Middleware()

	rg.GET("/bus/routes", h.listRoutes)
	rg.GET("/bus/routes/:routeId", h.getRoute)
	rg.GET("/bus/stops", h.listStops)
	rg.GET("/bus/geometry", h.geometry)
	rg.GET("/bus/vehicles", h.listVehicles)
	rg.POST("/bus/positions", write, h.reportPosition)

	rg.GET("/admin/bus/routes", h.adminListRoutes)
	rg.GET("/admin/bus/routes/:routeId", h.adminGetRoute)
	rg.GET("/admin/bus/stops", h.adminListStops)
	rg.GET("/admin/bus/vehicles", h.adminListVehicles)
	rg.GET("/admin/bus/geometry", h.adminGeometry)
	rg.POST("/admin/bus/routes", write, h.createRoute)
	rg.PATCH("/admin/bus/routes/:routeId", write, h.updateRoute)
	rg.DELETE("/admin/bus/routes/:routeId", write, h.deleteRoute)
	rg.PUT("/admin/bus/routes/:routeId/stops", write, h.replaceRouteStops)
	rg.POST("/admin/bus/stops", write, h.createStop)
	rg.PATCH("/admin/bus/stops/:stopId", write, h.updateStop)
	rg.DELETE("/admin/bus/stops/:stopId", write, h.deleteStop)
	rg.POST("/admin/bus/vehicles", write, h.createVehicle)
	rg.PATCH("/admin/bus/vehicles/:vehicleId", write, h.updateVehicle)
	rg.DELETE("/admin/bus/vehicles/:vehicleId", write, h.deleteVehicle)
}

// ---------------------------------------------------------------------------
// 读接口
// ---------------------------------------------------------------------------

func (h *Handler) listRoutes(c *gin.Context) {
	list, err := h.repo.ListRoutes(c.Request.Context(),
		strings.TrimSpace(c.Query("q")), "published", c.Query("geometry") == "1")
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"routes": list, "count": len(list)})
}

func (h *Handler) getRoute(c *gin.Context) {
	routeID, ok := int64Param(c, "routeId", "routeId")
	if !ok {
		return
	}
	rt, err := h.repo.GetRoute(c.Request.Context(), routeID, false)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, rt)
}

func (h *Handler) listStops(c *gin.Context) {
	f := stopFilter{
		Q:      strings.TrimSpace(c.Query("q")),
		Status: "published",
		Limit:  intQueryDefault(c, "limit", 500),
	}
	if v := c.Query("route_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "route_id 应为数字")
			return
		}
		f.RouteID = &id
	}
	if v := c.Query("lng"); v != "" || c.Query("lat") != "" {
		near, ok := parseNear(c)
		if !ok {
			return
		}
		f.Near = near
	}
	list, err := h.repo.ListStops(c.Request.Context(), f)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"stops": list, "count": len(list)})
}

// geometry 输出线路 + 站点的 FeatureCollection，App 把它塞进一个 GeoJSON source 即可
// 自绘（与 /features 同样的路子）。底图侧若启用 Martin 直发瓦片，用的是同一批表与图层名。
func (h *Handler) geometry(c *gin.Context) {
	h.writeGeometry(c, false)
}

func (h *Handler) adminGeometry(c *gin.Context) {
	h.writeGeometry(c, true)
}

func (h *Handler) writeGeometry(c *gin.Context, includeDraft bool) {
	raw, err := h.repo.Geometry(c.Request.Context(), includeDraft)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	c.Data(http.StatusOK, "application/geo+json; charset=utf-8", raw)
}

// listVehicles 实时位置：默认只下发 max_age_s（默认 120 秒）内有位置更新的在用车辆。
// max_age_s=0 表示不过滤——全量下发并带 age_s，由客户端自己判断新鲜度。
func (h *Handler) listVehicles(c *gin.Context) {
	f := vehicleFilter{MaxAgeS: defaultMaxAgeS}
	if v := c.Query("max_age_s"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "max_age_s 应为整数秒")
			return
		}
		age, err := normalizeMaxAge(n)
		if err != nil {
			httpx.Respond(c, err)
			return
		}
		f.MaxAgeS = age
	}
	if v := c.Query("route_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "route_id 应为数字")
			return
		}
		f.RouteID = &id
	}
	list, err := h.repo.ListVehicles(c.Request.Context(), f)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"vehicles":    list,
		"count":       len(list),
		"max_age_s":   f.MaxAgeS,
		"server_time": time.Now().Format(time.RFC3339),
		"note":        "age_s 由服务器计算；超过 max_age_s 未更新的车辆不下发（视为离线）",
	})
}

// ---------------------------------------------------------------------------
// 位置上报
// ---------------------------------------------------------------------------

type positionReq struct {
	VehicleID  string          `json:"vehicle_id" binding:"required"`
	RouteID    *int64          `json:"route_id"`
	Lng        *float64        `json:"lng"`
	Lat        *float64        `json:"lat"`
	HeadingDeg *float64        `json:"heading_deg"`
	SpeedKMH   *float64        `json:"speed_kmh"`
	AccuracyM  *float64        `json:"accuracy_m"`
	ReportedAt *time.Time      `json:"reported_at"`
	Props      json.RawMessage `json:"props"`
	Source     string          `json:"source"`
}

func (h *Handler) reportPosition(c *gin.Context) {
	var req positionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误: "+err.Error())
		return
	}
	if req.Lng == nil || req.Lat == nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "lng 与 lat 必须成对提供")
		return
	}
	props, err := normalizeProps(req.Props)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	in := positionInput{
		VehicleID:  req.VehicleID,
		RouteID:    req.RouteID,
		Lng:        *req.Lng,
		Lat:        *req.Lat,
		HeadingDeg: req.HeadingDeg,
		SpeedKMH:   req.SpeedKMH,
		AccuracyM:  req.AccuracyM,
		ReportedAt: req.ReportedAt,
		Props:      props,
		Source:     req.Source,
	}
	in.VehicleID = strings.TrimSpace(in.VehicleID)
	if err := validatePosition(in); err != nil {
		httpx.Respond(c, err)
		return
	}

	ctx := c.Request.Context()
	enabled, err := h.repo.VehicleEnabled(ctx, in.VehicleID)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	if !enabled {
		httpx.Err(c, http.StatusConflict, "vehicle_disabled",
			"车辆已停用（enabled=false）；恢复上报请先在管理台启用")
		return
	}

	id, reportedAt, err := h.repo.InsertPosition(ctx, in)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"position_id": id,
		"vehicle_id":  in.VehicleID,
		"reported_at": reportedAt.Format(time.RFC3339),
	})
}

// ---------------------------------------------------------------------------
// 管理台：线路
// ---------------------------------------------------------------------------

func (h *Handler) adminListRoutes(c *gin.Context) {
	list, err := h.repo.AdminRoutes(c.Request.Context(),
		strings.TrimSpace(c.Query("q")), strings.TrimSpace(c.Query("status")),
		c.Query("geometry") != "0")
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"routes": list, "count": len(list)})
}

// adminGetRoute 管理台线路详情：含草稿线路与草稿站点、按 seq 排好的站点。
// 公开的 /bus/routes/:id 只出 published；管理台的排站序弹窗要能编辑草稿线，
// 所以这条单独放在 /admin 下。
func (h *Handler) adminGetRoute(c *gin.Context) {
	routeID, ok := int64Param(c, "routeId", "routeId")
	if !ok {
		return
	}
	rt, err := h.repo.GetRoute(c.Request.Context(), routeID, true)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, rt)
}

func (h *Handler) createRoute(c *gin.Context) {
	var p RouteCreate
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	id, err := h.repo.CreateRoute(c.Request.Context(), p, admintoken.Editor(c))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"route_id": id})
}

func (h *Handler) updateRoute(c *gin.Context) {
	routeID, ok := int64Param(c, "routeId", "routeId")
	if !ok {
		return
	}
	var p RoutePatch
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	if err := h.repo.UpdateRoute(c.Request.Context(), routeID, p); err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"updated": routeID})
}

func (h *Handler) deleteRoute(c *gin.Context) {
	routeID, ok := int64Param(c, "routeId", "routeId")
	if !ok {
		return
	}
	if err := h.repo.DeleteRoute(c.Request.Context(), routeID); err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"deleted": routeID})
}

// replaceRouteStops 整体替换站序：body 只给站点的 stop_id 顺序，seq 由服务端按下标生成。
// 空数组 = 清空站序（线路保留）。站序规则（≥2 站、中间站不重复、仅环线首末同站）
// 在写库前先挡——见 planStopSeq。
func (h *Handler) replaceRouteStops(c *gin.Context) {
	routeID, ok := int64Param(c, "routeId", "routeId")
	if !ok {
		return
	}
	var body struct {
		StopIDs []int64 `json:"stop_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误（应为 {\"stop_ids\":[1,2,3]}）")
		return
	}
	ctx := c.Request.Context()

	rt, err := h.repo.GetRoute(ctx, routeID, true)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	if _, err := planStopSeq(body.StopIDs, rt.IsLoop); err != nil {
		httpx.Respond(c, err)
		return
	}
	count, err := h.repo.ReplaceRouteStops(ctx, routeID, body.StopIDs)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"route_id": routeID, "stop_count": count})
}

// ---------------------------------------------------------------------------
// 管理台：站点与车辆
// ---------------------------------------------------------------------------

func (h *Handler) adminListStops(c *gin.Context) {
	f := stopFilter{
		Q:      strings.TrimSpace(c.Query("q")),
		Status: strings.TrimSpace(c.Query("status")),
		Limit:  intQueryDefault(c, "limit", 500),
	}
	if v := c.Query("route_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "route_id 应为数字")
			return
		}
		f.RouteID = &id
	}
	list, err := h.repo.AdminStops(c.Request.Context(), f)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"stops": list, "count": len(list)})
}

func (h *Handler) createStop(c *gin.Context) {
	var p StopCreate
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	id, err := h.repo.CreateStop(c.Request.Context(), p, admintoken.Editor(c))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"stop_id": id})
}

func (h *Handler) updateStop(c *gin.Context) {
	stopID, ok := int64Param(c, "stopId", "stopId")
	if !ok {
		return
	}
	var p StopPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	if err := h.repo.UpdateStop(c.Request.Context(), stopID, p); err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"updated": stopID})
}

func (h *Handler) deleteStop(c *gin.Context) {
	stopID, ok := int64Param(c, "stopId", "stopId")
	if !ok {
		return
	}
	if err := h.repo.DeleteStop(c.Request.Context(), stopID); err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"deleted": stopID})
}

func (h *Handler) adminListVehicles(c *gin.Context) {
	var routeID *int64
	if v := c.Query("route_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			httpx.Err(c, http.StatusBadRequest, "bad_request", "route_id 应为数字")
			return
		}
		routeID = &id
	}
	list, err := h.repo.AdminVehicles(c.Request.Context(), routeID)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"vehicles": list, "count": len(list)})
}

func (h *Handler) createVehicle(c *gin.Context) {
	var p VehicleCreate
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	id, err := h.repo.CreateVehicle(c.Request.Context(), p)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"vehicle_id": id})
}

func (h *Handler) updateVehicle(c *gin.Context) {
	var p VehiclePatch
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	if err := h.repo.UpdateVehicle(c.Request.Context(), c.Param("vehicleId"), p); err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"updated": c.Param("vehicleId")})
}

func (h *Handler) deleteVehicle(c *gin.Context) {
	if err := h.repo.DeleteVehicle(c.Request.Context(), c.Param("vehicleId")); err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"deleted": c.Param("vehicleId")})
}

// ---------------------------------------------------------------------------
// 参数解析
// ---------------------------------------------------------------------------

// parseNear 解析成对的 lng/lat（附近站点查询）。缺一个就是写错了，直接拒绝。
func parseNear(c *gin.Context) (*[2]float64, bool) {
	lngRaw, latRaw := c.Query("lng"), c.Query("lat")
	if lngRaw == "" || latRaw == "" {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "lng 与 lat 必须成对提供")
		return nil, false
	}
	lng, errLng := strconv.ParseFloat(lngRaw, 64)
	lat, errLat := strconv.ParseFloat(latRaw, 64)
	if errLng != nil || errLat != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "lng / lat 应为数字")
		return nil, false
	}
	if lng < -180 || lng > 180 || lat < -90 || lat > 90 {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "lng / lat 超出 WGS84 范围")
		return nil, false
	}
	return &[2]float64{lng, lat}, true
}

func int64Param(c *gin.Context, name, label string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", label+" 应为数字")
		return 0, false
	}
	return id, true
}

func intQueryDefault(c *gin.Context, key string, def int) int {
	if v := c.Query(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
