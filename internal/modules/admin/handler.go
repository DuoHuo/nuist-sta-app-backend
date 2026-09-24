package admin

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/admintoken"
)

type Handler struct {
	repo   *Repo
	cfg    *config.Config
	tokens *admintoken.Tokens
}

// Register 挂载管理端路由。读接口开放（内网），写接口一律要求 X-Collect-Token；
// 令牌未配置时写接口直接拒绝，不再像以前那样静默放行。
func Register(rg *gin.RouterGroup, cfg *config.Config, pool *pgxpool.Pool) {
	h := &Handler{
		repo:   NewRepo(pool),
		cfg:    cfg,
		tokens: admintoken.FromSpec(cfg.Server.CollectToken),
	}
	write := h.tokens.Middleware()

	rg.GET("/admin/stats", h.stats)
	rg.GET("/admin/buildings/geometry", h.allBuildingsGeometry)
	rg.GET("/admin/graph", h.graph)
	rg.GET("/admin/pois/geometry", h.allPOIs)
	rg.GET("/admin/features", h.listFeatures)
	rg.GET("/admin/features/geometry", h.allFeaturesGeometry)

	rg.PATCH("/admin/buildings/:buildingId", write, h.updateBuilding)
	rg.PATCH("/admin/buildings/:buildingId/geometry", write, h.updateBuildingGeometry)
	rg.POST("/admin/buildings", write, h.createBuilding)
	rg.DELETE("/admin/buildings/:buildingId", write, h.deleteBuilding)
	rg.PATCH("/admin/pois/:poiId", write, h.updatePOI)
	rg.DELETE("/admin/pois/:poiId", write, h.deletePOI)
	rg.POST("/admin/pois", write, h.createPOI)
	rg.POST("/admin/features", write, h.createFeature)
	rg.PATCH("/admin/features/:featureId", write, h.updateFeature)
	rg.DELETE("/admin/features/:featureId", write, h.deleteFeature)
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
	id, err := h.repo.CreatePOI(c.Request.Context(), p, admintoken.Editor(c))
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

// ---------------------------------------------------------------------------
// 通用地物
// ---------------------------------------------------------------------------

func (h *Handler) listFeatures(c *gin.Context) {
	list, err := h.repo.ListFeatures(c.Request.Context(),
		strings.TrimSpace(c.Query("kind")), strings.TrimSpace(c.Query("status")),
		strings.TrimSpace(c.Query("q")))
	if err != nil {
		log.Printf("admin list features: %v", err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"features": list, "count": len(list)})
}

func (h *Handler) allFeaturesGeometry(c *gin.Context) {
	raw, err := h.repo.AllFeaturesGeometry(c.Request.Context())
	if err != nil {
		log.Printf("admin features/geometry: %v", err)
		httpx.Respond(c, err)
		return
	}
	c.Data(http.StatusOK, "application/geo+json; charset=utf-8", raw)
}

func (h *Handler) createFeature(c *gin.Context) {
	var p FeatureCreate
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	id, err := h.repo.CreateFeature(c.Request.Context(), p, admintoken.Editor(c))
	if err != nil {
		log.Printf("admin create feature: %v", err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"feature_id": id})
}

func (h *Handler) updateFeature(c *gin.Context) {
	featureID, err := strconv.ParseInt(c.Param("featureId"), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "featureId 应为数字")
		return
	}
	var p FeaturePatch
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	if err := h.repo.UpdateFeature(c.Request.Context(), featureID, p); err != nil {
		log.Printf("admin update feature %d: %v", featureID, err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"updated": featureID})
}

func (h *Handler) deleteFeature(c *gin.Context) {
	featureID, err := strconv.ParseInt(c.Param("featureId"), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "featureId 应为数字")
		return
	}
	if err := h.repo.DeleteFeature(c.Request.Context(), featureID); err != nil {
		log.Printf("admin delete feature %d: %v", featureID, err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"deleted": featureID})
}

// ---------------------------------------------------------------------------
// 建筑：新建与轮廓编辑
// ---------------------------------------------------------------------------

func (h *Handler) createBuilding(c *gin.Context) {
	var p BuildingCreate
	if err := c.ShouldBindJSON(&p); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	id, err := h.repo.CreateBuilding(c.Request.Context(), p, admintoken.Editor(c))
	if err != nil {
		log.Printf("admin create building: %v", err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"building_id": id})
}

func (h *Handler) updateBuildingGeometry(c *gin.Context) {
	var body struct {
		Geometry json.RawMessage `json:"geometry"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "请求体格式错误")
		return
	}
	id := c.Param("buildingId")
	if err := h.repo.UpdateBuildingGeometry(c.Request.Context(), id, body.Geometry); err != nil {
		log.Printf("admin update building geometry %s: %v", id, err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"updated": id})
}
