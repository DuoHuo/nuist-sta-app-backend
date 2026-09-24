package glyphs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
)

func newTestRouter(t *testing.T, dir string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	cfg := config.Default()
	cfg.Map.GlyphsDir = dir
	Register(r, cfg)
	return r
}

func TestServesGlyphFile(t *testing.T) {
	dir := t.TempDir()
	stackDir := filepath.Join(dir, "Noto Sans Regular")
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDir, "0-255.pbf"), []byte("glyph-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	// 字体名带空格，客户端会按 %20 请求。
	newTestRouter(t, dir).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/glyphs/Noto%20Sans%20Regular/0-255.pbf", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", w.Code)
	}
	if w.Body.String() != "glyph-bytes" {
		t.Fatalf("内容 = %q", w.Body.String())
	}
	if w.Header().Get("Cache-Control") == "" {
		t.Error("缺少 Cache-Control，字形是不可变资源，应给长缓存")
	}
}

func TestRefusesNonPbfAndDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newTestRouter(t, dir)

	for _, path := range []string{
		"/glyphs/secret.txt",             // 非 .pbf 后缀
		"/glyphs/",                       // 目录（不开列表）
		"/glyphs/Noto%20Sans%20Regular/", // 字体目录
		"/glyphs/../configs/config.yaml", // 越权路径
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code == http.StatusOK {
			t.Errorf("%s: 不该返回 200（实际 %d）", path, w.Code)
		}
	}
}
