// Package webui 内嵌管理台静态资源（go:embed），由 api 服务器在 /admin 下直接托管，
// 无需额外前端服务与构建步骤；MapLibre GL 以本地 vendor 文件引入，不依赖外部 CDN。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

// Register 挂载 /admin 静态资源路由（/admin 重定向到 /admin/）。
func Register(r *gin.Engine) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return
	}
	fileServer := http.StripPrefix("/admin/", http.FileServer(http.FS(sub)))

	r.GET("/admin", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/admin/")
	})
	r.GET("/admin/*filepath", func(c *gin.Context) {
		path := strings.TrimPrefix(c.Param("filepath"), "/")
		if path == "" {
			path = "index.html"
		}
		// 单页应用：非静态资源路径直接以 200 返回 index.html 内容。
		// 不能用 FileFromFS("index.html")：内嵌 FileServer 会把 index.html
		// 重定向到上级目录，形成 301 循环（MapLibre sprite 曾因此加载失败）。
		if _, err := fs.Stat(sub, path); err != nil {
			index, readErr := fs.ReadFile(sub, "index.html")
			if readErr != nil {
				c.String(http.StatusNotFound, "not found")
				return
			}
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", index)
			return
		}
		// 资源随二进制内嵌发布，无协商缓存依据：no-cache 保证升级后浏览器取新版
		c.Header("Cache-Control", "no-cache")
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
