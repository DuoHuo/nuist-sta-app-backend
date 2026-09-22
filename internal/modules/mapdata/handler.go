package mapdata

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

type Handler struct {
	repo *Repo
	cfg  *config.Config
}

// Register 挂载地图数据路由。
func Register(rg *gin.RouterGroup, cfg *config.Config, pool *pgxpool.Pool) {
	h := &Handler{repo: NewRepo(pool), cfg: cfg}

	rg.GET("/map/config", h.mapConfig)
	rg.GET("/buildings", h.listBuildings)
	rg.GET("/buildings/:buildingId", h.getBuilding)
	rg.GET("/buildings/:buildingId/geometry", h.buildingGeometry)
	rg.GET("/buildings/:buildingId/floors", h.listFloors)
	rg.GET("/floors/:floorId/features", h.floorFeatures)
	rg.GET("/pois", h.searchPOIs)
	rg.GET("/pois/:poiId", h.getPOI)
}

// mapConfig 输出 App 初始化地图所需的资源地址（Martin 瓦片/样式/范围/署名）。
func (h *Handler) mapConfig(c *gin.Context) {
	httpx.OK(c, gin.H{
		"tile_url":     h.cfg.Map.TileURL,
		"style_url":    h.cfg.Map.StyleURL,
		"glyphs_url":   h.cfg.Map.GlyphsURL,
		"sprites_url":  h.cfg.Map.SpritesURL,
		"bounds":       h.cfg.Map.Bounds,
		"attribution":  h.cfg.Map.Attribution,
		"data_sources": gin.H{"buildings": "geojson-api", "indoor": "geojson-api"},
	})
}

func (h *Handler) listBuildings(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	bbox, ok := parseBBox(c.Query("bbox"))
	if !ok {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "bbox 格式应为 minLon,minLat,maxLon,maxLat")
		return
	}
	list, err := h.repo.ListBuildings(c.Request.Context(), q, bbox)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"buildings": list, "count": len(list)})
}

func (h *Handler) getBuilding(c *gin.Context) {
	b, err := h.repo.GetBuilding(c.Request.Context(), c.Param("buildingId"))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, b)
}

func (h *Handler) buildingGeometry(c *gin.Context) {
	raw, err := h.repo.BuildingGeometry(c.Request.Context(), c.Param("buildingId"))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	c.Data(http.StatusOK, "application/geo+json; charset=utf-8", raw)
}

func (h *Handler) listFloors(c *gin.Context) {
	floors, err := h.repo.Floors(c.Request.Context(), c.Param("buildingId"))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"floors": floors})
}

func (h *Handler) floorFeatures(c *gin.Context) {
	floorID, err := strconv.ParseInt(c.Param("floorId"), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "floorId 应为数字")
		return
	}
	var kinds []string
	if v := c.Query("kinds"); v != "" {
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				kinds = append(kinds, k)
			}
		}
	}
	raw, err := h.repo.FloorFeatures(c.Request.Context(), floorID, kinds)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	c.Data(http.StatusOK, "application/geo+json; charset=utf-8", raw)
}

func (h *Handler) searchPOIs(c *gin.Context) {
	pois, err := h.repo.SearchPOIs(c.Request.Context(),
		strings.TrimSpace(c.Query("q")),
		strings.TrimSpace(c.Query("building_id")),
		strings.TrimSpace(c.Query("category")),
		intQueryDefault(c, "limit", 20))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, gin.H{"pois": pois, "count": len(pois)})
}

func (h *Handler) getPOI(c *gin.Context) {
	poiID, err := strconv.ParseInt(c.Param("poiId"), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "poiId 应为数字")
		return
	}
	p, err := h.repo.GetPOI(c.Request.Context(), poiID)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, p)
}

func parseBBox(s string) (*[4]float64, bool) {
	if s == "" {
		return nil, true
	}
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return nil, false
	}
	var bbox [4]float64
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, false
		}
		bbox[i] = v
	}
	if bbox[0] >= bbox[2] || bbox[1] >= bbox[3] {
		return nil, false
	}
	return &bbox, true
}

func intQueryDefault(c *gin.Context, key string, def int) int {
	if v := c.Query(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
