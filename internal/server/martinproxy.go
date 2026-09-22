// Martin 瓦片服务的同源反向代理：/martin/* -> martin 实例。
// 管理台等浏览器端通过 api 自身域名访问瓦片/样式，
// 不必猜测 martin 对外暴露的端口（端口转发方案解耦）。
package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func martinProxy() gin.HandlerFunc {
	upstream := os.Getenv("CAMPUS_MARTIN_UPSTREAM")
	if upstream == "" {
		upstream = "http://127.0.0.1:3000"
	}
	target, err := url.Parse(upstream)
	if err != nil {
		return func(c *gin.Context) {
			c.String(http.StatusBadGateway, "CAMPUS_MARTIN_UPSTREAM 配置无效")
		}
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		Proxy:              nil,
		MaxIdleConns:       16,
		IdleConnTimeout:    60 * time.Second,
		DisableCompression: false,
	}
	return func(c *gin.Context) {
		c.Request.URL.Path = strings.TrimPrefix(c.Request.URL.Path, "/martin")
		if c.Request.URL.Path == "" {
			c.Request.URL.Path = "/"
		}
		// 让 martin 依据真实对外协议和主机生成地址，而不是容器内部地址。
		if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
			c.Request.Header.Set("X-Forwarded-Proto", proto)
		} else {
			c.Request.Header.Set("X-Forwarded-Proto", "http")
		}
		c.Request.Header.Set("X-Forwarded-Host", c.Request.Host)
		proxy.ModifyResponse = func(resp *http.Response) error {
			// martin 的重定向（如 /styles/campus -> /styles/campus/）用绝对路径，
			// 穿透代理后会打到 api 自身 —— 重写 Location 补回 /martin 前缀
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") {
				resp.Header.Set("Location", "/martin"+loc)
			}
			if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
				return nil
			}
			// TileJSON/样式/目录里的地址统一改写成 api 同源的 /martin/* 路径，
			// 避免客户端拿到 martin 容器内部地址（localhost:3000）或被网络
			// 拦截的独立端口。
			body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if err != nil {
				return err
			}
			public := "http://" + c.Request.Host
			for _, origin := range []string{
				upstream,
				"http://localhost:3000",
				"http://127.0.0.1:3000",
				public,
			} {
				body = bytes.ReplaceAll(body, []byte(origin+"/../../campus"), []byte(public+"/martin/campus"))
				body = bytes.ReplaceAll(body, []byte(origin+"/campus"), []byte(public+"/martin/campus"))
				body = bytes.ReplaceAll(body, []byte(origin+"/styles"), []byte(public+"/martin/styles"))
				body = bytes.ReplaceAll(body, []byte(origin+"/catalog"), []byte(public+"/martin/catalog"))
				body = bytes.ReplaceAll(body, []byte(origin+"/health"), []byte(public+"/martin/health"))
			}
			body = bytes.ReplaceAll(body, []byte(public+"/martin/martin/"), []byte(public+"/martin/"))
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			return nil
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}
