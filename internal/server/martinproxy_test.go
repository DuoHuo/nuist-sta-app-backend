package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMartinProxyRewritesJSONAddresses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Forwarded-Host"); got != "api.example.test:12345" {
			t.Errorf("X-Forwarded-Host = %q", got)
		}
		if got := r.URL.Path; got != "/campus" {
			t.Errorf("upstream path = %q, want /campus", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tiles":["http://localhost:3000/campus/{z}/{x}/{y}"],"vector_layers":[]}`))
	}))
	t.Cleanup(upstream.Close)

	t.Setenv("CAMPUS_MARTIN_UPSTREAM", upstream.URL)
	r := gin.New()
	r.Any("/martin/*path", martinProxy())

	req := httptest.NewRequest(http.MethodGet, "/martin/campus", nil)
	req.Host = "api.example.test:12345"
	rec := &closeNotifier{ResponseRecorder: httptest.NewRecorder()}
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	want := `{"tiles":["http://api.example.test:12345/martin/campus/{z}/{x}/{y}"],"vector_layers":[]}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// httptest.ResponseRecorder 缺少 gin + ReverseProxy 需要的 CloseNotify。
type closeNotifier struct {
	*httptest.ResponseRecorder
	notify chan bool
}

func (c *closeNotifier) CloseNotify() <-chan bool {
	if c.notify == nil {
		c.notify = make(chan bool)
	}
	return c.notify
}

func TestMartinProxyKeepsTilesUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tile := []byte{0x1a, 0x0f, 0x09, 0x02}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/campus/14/13594/6642" {
			t.Errorf("upstream path = %q", got)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(tile)
	}))
	t.Cleanup(upstream.Close)

	t.Setenv("CAMPUS_MARTIN_UPSTREAM", upstream.URL)
	r := gin.New()
	r.Any("/martin/*path", martinProxy())

	req := httptest.NewRequest(http.MethodGet, "/martin/campus/14/13594/6642", nil)
	req.Host = "api.example.test:12345"
	rec := &closeNotifier{ResponseRecorder: httptest.NewRecorder()}
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if string(rec.Body.Bytes()) != string(tile) {
		t.Errorf("tile body changed: % x", rec.Body.Bytes())
	}
}
