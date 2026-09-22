package routing

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

type Handler struct {
	repo *Repo
	cfg  *config.Config
}

// PlaceRef 起点/终点的多种表达，按优先级解析：
// node_id > poi_id > 经纬度吸附 > 建筑（+楼层）。
type PlaceRef struct {
	NodeID     int64    `json:"node_id"`
	POIID      int64    `json:"poi_id"`
	Lng        *float64 `json:"lng"`
	Lat        *float64 `json:"lat"`
	BuildingID string   `json:"building_id"`
	LevelIndex *int     `json:"level_index"`
}

func (p PlaceRef) empty() bool {
	return p.NodeID == 0 && p.POIID == 0 && p.Lng == nil && p.BuildingID == ""
}

type routeRequest struct {
	Origin      PlaceRef `json:"origin"`
	Destination PlaceRef `json:"destination"`
	Options     struct {
		AccessibleOnly bool `json:"accessible_only"`
	} `json:"options"`
}

func Register(rg *gin.RouterGroup, cfg *config.Config, pool *pgxpool.Pool) {
	h := &Handler{repo: NewRepo(pool), cfg: cfg}
	rg.POST("/route", h.route)
}

func (h *Handler) route(c *gin.Context) {
	var req routeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误: "+err.Error())
		return
	}
	if req.Origin.empty() {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "origin 缺少 node_id / poi_id / lng+lat / building_id 之一")
		return
	}
	if req.Destination.empty() {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "destination 缺少 node_id / poi_id / lng+lat / building_id 之一")
		return
	}

	ctx := c.Request.Context()
	originID, err := h.resolve(ctx, req.Origin, "起点")
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	destID, err := h.resolve(ctx, req.Destination, "终点")
	if err != nil {
		httpx.Respond(c, err)
		return
	}

	nodes, err := h.repo.Route(ctx, originID, destID, req.Options.AccessibleOnly)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	if len(nodes) < 2 {
		httpx.Err(c, http.StatusNotFound, "no_route", "起终点之间没有可通行路线（可能不连通或路段关闭）")
		return
	}

	res := BuildRoute(nodes, h.floorMap(ctx, nodes), h.buildingMap(ctx, nodes))
	httpx.OK(c, res)
}

func (h *Handler) resolve(ctx context.Context, p PlaceRef, what string) (int64, error) {
	switch {
	case p.NodeID != 0:
		ok, err := h.repo.NodeExists(ctx, p.NodeID)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, httpx.NotFound(what + " 节点不存在")
		}
		return p.NodeID, nil
	case p.POIID != 0:
		return h.repo.NodeForPOI(ctx, p.POIID)
	case p.Lng != nil && p.Lat != nil:
		nodeID, distM, err := h.repo.NearestNode(ctx, *p.Lng, *p.Lat)
		if err != nil {
			return 0, err
		}
		if distM > h.cfg.Locate.MaxSnapM {
			return 0, httpx.Unprocessable(what + " 距离可通行路网过远（" + strconv.Itoa(int(distM)) + " 米），请靠近道路或建筑入口")
		}
		return nodeID, nil
	case p.BuildingID != "":
		if p.LevelIndex != nil {
			return h.repo.NodeForFloor(ctx, p.BuildingID, *p.LevelIndex)
		}
		return h.repo.NodeForBuilding(ctx, p.BuildingID)
	}
	return 0, httpx.BadRequest(what + " 无法解析")
}

func (h *Handler) floorMap(ctx context.Context, nodes []PathNode) map[int64]FloorInfo {
	ids := map[int64]bool{}
	for _, n := range nodes {
		if n.FloorID != nil {
			ids[*n.FloorID] = true
		}
	}
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	m, err := h.repo.FloorsByIDs(ctx, list)
	if err != nil {
		return map[int64]FloorInfo{}
	}
	return m
}

func (h *Handler) buildingMap(ctx context.Context, nodes []PathNode) map[string]string {
	ids := map[string]bool{}
	for _, n := range nodes {
		if n.BuildingID != nil {
			ids[*n.BuildingID] = true
		}
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	m, err := h.repo.BuildingNames(ctx, list)
	if err != nil {
		return map[string]string{}
	}
	return m
}
