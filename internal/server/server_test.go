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
