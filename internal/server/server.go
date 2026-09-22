// Package server 组装 HTTP 路由与中间件。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/modules/admin"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/modules/locate"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/modules/mapdata"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/modules/models"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/modules/photos"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/modules/routing"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/webui"
)

func New(cfg *config.Config, pool *pgxpool.Pool) *gin.Engine {
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(requestID(), requestLog(), gin.Recovery(), cors(cfg.Server.CORSOrigins), bodyLimit(4<<20))
	r.MaxMultipartMemory = 1 << 20

	r.GET("/healthz", func(c *gin.Context) {
		if err := pool.Ping(c.Request.Context()); err != nil {
			httpx.Err(c, http.StatusServiceUnavailable, "db_unavailable", "数据库不可用")
			return
		}
		httpx.OK(c, gin.H{"status": "ok", "time": time.Now().Format(time.RFC3339)})
	})

	v1 := r.Group("/api/v1")
	mapdata.Register(v1, cfg, pool)
	routing.Register(v1, cfg, pool)
	locate.Register(v1, cfg, pool)
	admin.Register(v1, cfg, pool)
	models.Register(v1, cfg, pool)
	photos.Register(v1, r, cfg, pool) // 实拍图片：/api/v1 接口 + /photos 静态文件

	r.Any("/martin/*path", martinProxy()) // 瓦片服务同源代理（环境变量 CAMPUS_MARTIN_UPSTREAM）

	webui.Register(r) // /admin 管理台（内嵌静态资源）

	return r
}

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Printf("%s %s -> %d (%s) id=%s",
			c.Request.Method, c.Request.URL.Path, c.Writer.Status(),
			time.Since(start).Round(time.Millisecond), c.GetHeader("X-Request-ID"))
	}
}

func cors(origins []string) gin.HandlerFunc {
	allowAll := len(origins) == 0
	allowed := map[string]bool{}
	for _, o := range origins {
		allowed[strings.ToLower(o)] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || allowed[strings.ToLower(origin)]) {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID, X-Collect-Token")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func bodyLimit(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := n
		if c.Request.Method == http.MethodPost {
			switch c.FullPath() {
			case models.UploadRoute:
				limit = models.MaxRequestSize
			case photos.UploadRoute:
				limit = photos.MaxRequestSize
			}
		}
		if c.Request.ContentLength > limit {
			httpx.Err(c, http.StatusRequestEntityTooLarge, "body_too_large", "请求体过大")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
