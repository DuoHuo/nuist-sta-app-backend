// Package glyphs 把样式所需的字形文件（{fontstack}/{range}.pbf）以同源静态文件下发。
//
// 背景：样式 configs/style.osm-bright.json 里的 glyphs 原先指向
// https://tiles.openfreemap.org，校园网/手机侧时常超时或域名解析失败，
// 现象是底图正常但**图上没有任何文字**（App 侧 logcat 里 Mbgl 刷
// "Failed to load glyph range ... (timeout)"）。改为自托管后不再依赖外网。
//
// 字形文件由 scripts/fetch-fonts.sh 预取，目录结构即 MapLibre 的约定：
//
//	<dir>/<fontstack>/<range>.pbf   例如 data/glyphs/Noto Sans Regular/0-255.pbf
//
// 注意 Martin 不适用于此：它只接受 ttf/otf 源字体并按需自行生成字形，
// 喂给它预生成的 .pbf 会直接报 NoFontFilesFound 并启动失败。
package glyphs

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
)

// Route 是字形下发路由；样式里按 <base>/glyphs/{fontstack}/{range}.pbf 引用。
const Route = "/glyphs/*filepath"

// Register 挂载 /glyphs/*：只读、只发 .pbf 文件，不开目录列表。
func Register(engine *gin.Engine, cfg *config.Config) {
	dir := cfg.Map.GlyphsDir
	if dir == "" {
		dir = "data/glyphs"
	}
	// http.Dir 会拒绝跳出根目录的路径，无需自行判断 ".."。
	fs := http.StripPrefix("/glyphs/", http.FileServer(http.Dir(dir)))

	handler := func(c *gin.Context) {
		// 只放行 .pbf 文件：目录请求与其它后缀一律 404，避免列出字体清单。
		rel := c.Param("filepath")
		if !strings.HasSuffix(rel, ".pbf") {
			c.Status(http.StatusNotFound)
			return
		}
		// 文件名即内容：同一 URL 的内容永不改变，给长缓存。
		c.Header("Cache-Control", "public, max-age=604800, immutable")
		fs.ServeHTTP(c.Writer, c.Request)
	}

	engine.GET(Route, handler)
	engine.HEAD(Route, handler)
}
