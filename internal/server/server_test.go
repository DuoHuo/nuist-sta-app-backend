package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/modules/models"
	"github.com/gin-gonic/gin"
)

func TestModelRoutesRegistered(t *testing.T) {
	r := New(config.Default(), nil)
	routes := map[string]bool{}
	for _, route := range r.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{"POST " + models.UploadRoute, "GET /api/v1/buildings/:buildingId/model", "GET /api/v1/buildings/:buildingId/model/versions/:version/files/:asset"} {
		if !routes[route] {
			t.Errorf("missing %s", route)
		}
	}
}
func TestBodyLimitUploadException(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(bodyLimit(4 << 20))
	consume := func(c *gin.Context) {
		if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusNoContent)
	}
	r.POST("/ordinary", consume)
	r.POST(models.UploadRoute, consume)
	for _, tc := range []struct {
		path    string
		size    int64
		unknown bool
		want    int
	}{
		{"/ordinary", 4 << 20, false, 204},
		{"/ordinary", (4 << 20) + 1, false, 413},
		{"/ordinary", (4 << 20) + 1, true, 413},
		{"/api/v1/admin/buildings/b1/model", 5 << 20, false, 204},
		{"/api/v1/admin/buildings/b1/model", 5 << 20, true, 204},
		{"/api/v1/admin/buildings/b1/model", models.MaxRequestSize, false, 204},
		{"/api/v1/admin/buildings/b1/model", models.MaxRequestSize + 1, false, 413},
		{"/api/v1/admin/buildings/b1/model", models.MaxRequestSize + 1, true, 413},
	} {
		req := httptest.NewRequest("POST", tc.path, io.LimitReader(repeatReader{}, tc.size))
		if !tc.unknown {
			req.ContentLength = tc.size
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s size=%d unknown=%t: %d want %d", tc.path, tc.size, tc.unknown, w.Code, tc.want)
		}
	}
}

type repeatReader struct{}

func (repeatReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }
func TestModelUploadUsesExistingToken(t *testing.T) {
	cfg := config.Default()
	cfg.Server.CollectToken = "secret"
	r := New(cfg, nil)
	req := httptest.NewRequest("POST", "/api/v1/admin/buildings/b1/model", strings.NewReader("unauthenticated"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// 坏字节的查询串要在网关层挡住：以前会带着 invalid byte sequence 一路走到
// Postgres，变成 500（Windows 控制台下 GBK 的 curl 实测踩到）。
func TestBadQueryEncodingRejected(t *testing.T) {
	r := New(config.Default(), nil)
	req := httptest.NewRequest("GET", "/api/v1/features?q=%E4%B8%9C", nil) // 合法百分号编码
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusBadRequest {
		t.Fatalf("合法百分号编码被拒：%s", w.Body.String())
	}
	bad := httptest.NewRequest("GET", "/api/v1/admin/features?q=\xd1\xdd", nil) // 裸 GBK 字节
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, bad)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("坏字节查询串 -> %d，期望 400：%s", w2.Code, w2.Body.String())
	}
	// 百分号编码解出来是坏字节：URL 本身合法，同样不能放到数据库里
	encoded := httptest.NewRequest("GET", "/api/v1/admin/features?q=%D1%DD", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, encoded)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("百分号编码的坏字节 -> %d，期望 400：%s", w3.Code, w3.Body.String())
	}
}

// 公交接口是给 App 与底图预留的契约：路径一旦漏挂，App 侧只会看到 404。
// 这里把清单钉住——新增或改名都要同步改这里、README 的 API 表与 map.config。
func TestBusRoutesRegistered(t *testing.T) {
	r := New(config.Default(), nil)
	routes := map[string]bool{}
	for _, route := range r.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /api/v1/bus/routes",
		"GET /api/v1/bus/routes/:routeId",
		"GET /api/v1/bus/stops",
		"GET /api/v1/bus/geometry",
		"GET /api/v1/bus/vehicles",
		"POST /api/v1/bus/positions",
		"GET /api/v1/admin/bus/routes",
		"GET /api/v1/admin/bus/routes/:routeId",
		"GET /api/v1/admin/bus/stops",
		"GET /api/v1/admin/bus/vehicles",
		"GET /api/v1/admin/bus/geometry",
		"POST /api/v1/admin/bus/routes",
		"PATCH /api/v1/admin/bus/routes/:routeId",
		"DELETE /api/v1/admin/bus/routes/:routeId",
		"PUT /api/v1/admin/bus/routes/:routeId/stops",
		"POST /api/v1/admin/bus/stops",
		"PATCH /api/v1/admin/bus/stops/:stopId",
		"DELETE /api/v1/admin/bus/stops/:stopId",
		"POST /api/v1/admin/bus/vehicles",
		"PATCH /api/v1/admin/bus/vehicles/:vehicleId",
		"DELETE /api/v1/admin/bus/vehicles/:vehicleId",
	} {
		if !routes[route] {
			t.Errorf("missing %s", route)
		}
	}
}

func TestBusWritesRequireToken(t *testing.T) {
	cfg := config.Default()
	cfg.Server.CollectToken = "secret"
	r := New(cfg, nil)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/bus/positions", `{"vehicle_id":"BUS-01","lng":118.7,"lat":32.2}`},
		{"POST", "/api/v1/admin/bus/routes", `{"code":"1号线","name":"校园环线"}`},
		{"PATCH", "/api/v1/admin/bus/routes/1", `{"name":"校园环线"}`},
		{"PUT", "/api/v1/admin/bus/routes/1/stops", `{"stop_ids":[1,2]}`},
		{"POST", "/api/v1/admin/bus/vehicles", `{"vehicle_id":"BUS-01"}`},
		{"DELETE", "/api/v1/admin/bus/vehicles/BUS-01", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未带令牌 -> %d，期望 401: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// 没配置令牌时写接口一律 503（fail-closed），不能静默放行
	r2 := New(config.Default(), nil)
	req := httptest.NewRequest("POST", "/api/v1/bus/positions",
		strings.NewReader(`{"vehicle_id":"BUS-01","lng":118.7,"lat":32.2}`))
	w := httptest.NewRecorder()
	r2.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("未配置令牌 -> %d，期望 503: %s", w.Code, w.Body.String())
	}
}
