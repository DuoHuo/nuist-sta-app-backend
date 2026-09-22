package admin

import (
	"log"
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

// Register 挂载管理端路由。读接口开放（内网），写接口受 X-Collect-Token 保护。
func Register(rg *gin.RouterGroup, cfg *config.Config, pool *pgxpool.Pool) {
	h := &Handler{repo: NewRepo(pool), cfg: cfg}

	rg.GET("/admin/stats", h.stats)
	rg.GET("/admin/buildings/geometry", h.allBuildingsGeometry)
	rg.GET("/admin/graph", h.graph)
	rg.GET("/admin/pois/geometry", h.allPOIs)

	rg.PATCH("/admin/buildings/:buildingId", h.requireToken, h.updateBuilding)
	rg.DELETE("/admin/buildings/:buildingId", h.requireToken, h.deleteBuilding)
	rg.PATCH("/admin/pois/:poiId", h.requireToken, h.updatePOI)
	rg.DELETE("/admin/pois/:poiId", h.requireToken, h.deletePOI)
	rg.POST("/admin/pois", h.requireToken, h.createPOI)
}

// requireToken：配置了 collect_token 时，写接口必须携带 X-Collect-Token（同指纹采集约定）。
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

func (h *Handler) stats(c *gin.Context) {
	s, err := h.repo.Stats(c.Request.Context())
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, s)
}

func (h *Handler) allBuildingsGeometry(c *gin.Context) {
	raw, err := h.repo.AllBuildingsGeometry(c.Request.Context())
	if err != nil {
		log.Printf("admin buildings/geometry: %v", err)
		httpx.Respond(c, err)
		return
	}
	c.Data(http.StatusOK, "application/geo+json; charset=utf-8", raw)
}

func (h *Handler) graph(c *gin.Context) {
	raw, err := h.repo.Graph(c.Request.Context())
	if err != nil {
		log.Printf("admin graph: %v", err)
		httpx.Respond(c, err)
		return
	}
	c.Data(http.StatusOK, "application/geo+json; charset=utf-8", raw)
}

func (h *Handler) allPOIs(c *gin.Context) {
	raw, err := h.repo.AllPOIs(c.Request.Context())
	if err != nil {
		log.Printf("admin pois/geometry: %v", err)
		httpx.Respond(c, err)
		return
	}
	c.Data(http.StatusOK, "application/geo+json; charset=utf-8", raw)
}

func (h *Handler) updateBuilding(c *gin.Context) {
	var p BuildingPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	if err := h.repo.UpdateBuilding(c.Request.Context(), c.Param("buildingId"), p); err != nil {
		log.Printf("admin update building %s: %v", c.Param("buildingId"), err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"updated": c.Param("buildingId")})
}

func (h *Handler) updatePOI(c *gin.Context) {
	poiID, err := strconv.ParseInt(c.Param("poiId"), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "poiId 应为数字")
		return
	}
	var p POIPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	if err := h.repo.UpdatePOI(c.Request.Context(), poiID, p); err != nil {
		log.Printf("admin update poi %d: %v", poiID, err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"updated": poiID})
}

func (h *Handler) createPOI(c *gin.Context) {
	var p POICreate
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	id, err := h.repo.CreatePOI(c.Request.Context(), p)
	if err != nil {
		log.Printf("admin create poi: %v", err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"poi_id": id})
}

func (h *Handler) deletePOI(c *gin.Context) {
	poiID, err := strconv.ParseInt(c.Param("poiId"), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "poiId 应为数字")
		return
	}
	if err := h.repo.DeletePOI(c.Request.Context(), poiID); err != nil {
		log.Printf("admin delete poi %d: %v", poiID, err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"deleted": poiID})
}

func (h *Handler) deleteBuilding(c *gin.Context) {
	id := c.Param("buildingId")
	if err := h.repo.DeleteBuilding(c.Request.Context(), id); err != nil {
		log.Printf("admin delete building %s: %v", id, err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"deleted": id})
}
