package photos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/gin-gonic/gin"
)

// memoryRepo 是 repository 的内存实现，语义与 SQL 实现一致（含共享文件保留规则）。
type memoryRepo struct {
	photos    map[int64]Photo
	nextID    int64
	missing   map[string]bool // 模拟数据库中不存在的建筑
	existsErr error
	saveErr   error
	deleteErr error
}

func (r *memoryRepo) Exists(_ context.Context, id string) error {
	if r.existsErr != nil {
		return r.existsErr
	}
	if r.missing[id] {
		return httpx.NotFound("建筑不存在")
	}
	return nil
}
func (r *memoryRepo) List(ctx context.Context, id string) ([]Photo, error) {
	if err := r.Exists(ctx, id); err != nil {
		return nil, err
	}
	list := []Photo{}
	for _, p := range r.photos {
		if p.BuildingID == id {
			list = append(list, p)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].SortOrder != list[j].SortOrder {
			return list[i].SortOrder < list[j].SortOrder
		}
		return list[i].PhotoID < list[j].PhotoID
	})
	return list, nil
}
func (r *memoryRepo) Save(ctx context.Context, p *Photo) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	if err := r.Exists(ctx, p.BuildingID); err != nil {
		return err
	}
	r.nextID++
	p.PhotoID = r.nextID
	p.URL = photoURL(p.FileName)
	r.photos[p.PhotoID] = *p
	return nil
}
func (r *memoryRepo) Delete(_ context.Context, id int64) (string, error) {
	if r.deleteErr != nil {
		return "", r.deleteErr
	}
	p, ok := r.photos[id]
	if !ok {
		return "", httpx.NotFound("图片不存在")
	}
	delete(r.photos, id)
	for _, other := range r.photos {
		if other.FileName == p.FileName {
			return "", nil
		}
	}
	return p.FileName, nil
}

var urlPattern = regexp.MustCompile(`^/photos/[0-9a-f]{32}\.jpg$`)

func newTestHandler(t *testing.T) (*gin.Engine, *memoryRepo, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	repo := &memoryRepo{photos: map[int64]Photo{}, missing: map[string]bool{}}
	dir := filepath.Join(t.TempDir(), "photos")
	h := &Handler{repo: repo, storageDir: dir, token: "secret"}
	h.register(r.Group("/api/v1"), r)
	return r, repo, dir
}

type formPart struct {
	name, filename string
	data           []byte
}

func uploadRequest(t *testing.T, building string, parts []formPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		var part io.Writer
		var err error
		if p.filename == "" {
			part, err = w.CreateFormField(p.name)
		} else {
			part, err = w.CreateFormFile(p.name, p.filename)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(p.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/buildings/"+building+"/photos", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Collect-Token", "secret")
	return req
}

func perform(r http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("非 JSON 响应 %d: %s", w.Code, w.Body.String())
	}
	return envelope.Code, envelope.Message
}

func listPhotos(t *testing.T, r http.Handler, building string) ([]any, int) {
	t.Helper()
	data := decodeData(t, perform(r, httptest.NewRequest(http.MethodGet, "/api/v1/buildings/"+building+"/photos", nil)))
	items, ok := data["photos"].([]any)
	if !ok {
		t.Fatalf("photos 应为数组: %v", data)
	}
	return items, int(data["count"].(float64))
}

func deletePhoto(t *testing.T, r http.Handler, id int64) {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/photos/"+strconv.FormatInt(id, 10), nil)
	req.Header.Set("X-Collect-Token", "secret")
	data := decodeData(t, perform(r, req))
	if data["deleted"] != true {
		t.Fatalf("deleted = %v", data["deleted"])
	}
}

func serve(t *testing.T, r http.Handler, url string, want []byte, contentType string) {
	t.Helper()
	w := perform(r, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("GET %s -> %d %q", url, w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != contentType {
		t.Fatalf("%s Content-Type %q", url, got)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("%s Cache-Control %q", url, got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("%s X-Content-Type-Options %q", url, got)
	}
	if w := perform(r, httptest.NewRequest(http.MethodHead, url, nil)); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD %s -> %d", url, w.Code)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestUploadListServeAndDelete(t *testing.T) {
	r, repo, dir := newTestHandler(t)
	// 空列表：count 0，photos 是空数组而不是 null。
	data := decodeData(t, perform(r, httptest.NewRequest(http.MethodGet, "/api/v1/buildings/b1/photos", nil)))
	if data["count"] != float64(0) {
		t.Fatalf("count = %v", data["count"])
	}
	if items, ok := data["photos"].([]any); !ok || len(items) != 0 {
		t.Fatalf("photos = %v", data["photos"])
	}
	img := []byte("jpeg-content-1")
	photo := decodeData(t, perform(r, uploadRequest(t, "b1", []formPart{
		{"file", "IMG_0001.JPG", img},
		{"caption", "", []byte("东门实拍")},
		{"source", "", []byte("survey-2026")},
		{"taken_at", "", []byte("2026-05-06T07:08:09Z")},
		{"sort_order", "", []byte("3")},
	})))
	if photo["photo_id"] != float64(1) || photo["building_id"] != "b1" || photo["sort_order"] != float64(3) {
		t.Fatalf("photo = %v", photo)
	}
	if photo["caption"] != "东门实拍" || photo["source"] != "survey-2026" || photo["taken_at"] != "2026-05-06T07:08:09Z" {
		t.Fatalf("photo = %v", photo)
	}
	// 扩展名统一小写后入库（上传的是 .JPG）。
	url, _ := photo["url"].(string)
	if !urlPattern.MatchString(url) {
		t.Fatalf("url = %q", url)
	}
	serve(t, r, url, img, "image/jpeg")
	if names := dirNames(t, dir); len(names) != 1 || names[0] != strings.TrimPrefix(url, StaticPrefix) {
		t.Fatalf("存储文件 %v", names)
	}
	// 未提供 taken_at 时为 null，sort_order 默认 0，按 sort_order、photo_id 排序。
	png := []byte("png-content-1")
	second := decodeData(t, perform(r, uploadRequest(t, "b1", []formPart{{"file", "shot.png", png}, {"sort_order", "", []byte("-1")}})))
	if second["taken_at"] != nil || second["sort_order"] != float64(-1) {
		t.Fatalf("photo = %v", second)
	}
	serve(t, r, second["url"].(string), png, "image/png")
	webp := []byte("webp-content-1")
	third := decodeData(t, perform(r, uploadRequest(t, "b1", []formPart{{"file", "wall.webp", webp}})))
	serve(t, r, third["url"].(string), webp, "image/webp")
	items, count := listPhotos(t, r, "b1")
	if count != 3 || len(items) != 3 {
		t.Fatalf("count = %d items = %v", count, items)
	}
	// sort_order 为 -1、0（默认）、3，排序结果即 second、third、first。
	if items[0].(map[string]any)["url"] != second["url"] || items[1].(map[string]any)["url"] != third["url"] || items[2].(map[string]any)["url"] != url {
		t.Fatalf("排序错误: %v", items)
	}
	if items[0].(map[string]any)["caption"] != "" || items[0].(map[string]any)["taken_at"] != nil {
		t.Fatalf("列表字段错误: %v", items[0])
	}
	// 其他建筑的列表为空；未知建筑 404。
	if _, count := listPhotos(t, r, "b2"); count != 0 {
		t.Fatalf("跨建筑可见 count = %d", count)
	}
	repo.missing["nope"] = true
	w := perform(r, httptest.NewRequest(http.MethodGet, "/api/v1/buildings/nope/photos", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知建筑 -> %d", w.Code)
	}
	if code, _ := decodeEnvelope(t, w); code != "not_found" {
		t.Fatalf("code = %q", code)
	}
	// 删除后元数据与文件同时消失。
	deletePhoto(t, r, 1)
	if w := perform(r, httptest.NewRequest(http.MethodGet, url, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("删除后仍可访问: %d", w.Code)
	}
	if items, count := listPhotos(t, r, "b1"); count != 2 || len(items) != 2 {
		t.Fatalf("删除后 count = %d items = %v", count, items)
	}
	if names := dirNames(t, dir); len(names) != 2 {
		t.Fatalf("残留文件 %v", names)
	}
}

func TestUploadDeduplicatesContent(t *testing.T) {
	r, repo, dir := newTestHandler(t)
	img := []byte("identical-bytes")
	first := decodeData(t, perform(r, uploadRequest(t, "b1", []formPart{{"file", "a.png", img}})))
	second := decodeData(t, perform(r, uploadRequest(t, "b1", []formPart{{"file", "b.png", img}})))
	if first["url"] != second["url"] {
		t.Fatalf("同内容未复用文件: %v %v", first["url"], second["url"])
	}
	if first["photo_id"] == second["photo_id"] {
		t.Fatal("每次上传都应新增一行元数据")
	}
	// 文件名含扩展名，同一内容换个扩展名是不同的文件。
	other := decodeData(t, perform(r, uploadRequest(t, "b1", []formPart{{"file", "a.jpg", img}})))
	if other["url"] == first["url"] {
		t.Fatal("不同扩展名不应共用一个文件")
	}
	if names := dirNames(t, dir); len(names) != 2 {
		t.Fatalf("存储文件 %v", names)
	}
	if _, count := listPhotos(t, r, "b1"); count != 3 {
		t.Fatalf("count = %d", count)
	}
	// 仍有记录引用同一文件时必须保留文件。
	deletePhoto(t, r, int64(first["photo_id"].(float64)))
	if w := perform(r, httptest.NewRequest(http.MethodGet, first["url"].(string), nil)); w.Code != http.StatusOK {
		t.Fatalf("共享文件被提前删除: %d", w.Code)
	}
	deletePhoto(t, r, int64(second["photo_id"].(float64)))
	if w := perform(r, httptest.NewRequest(http.MethodGet, first["url"].(string), nil)); w.Code != http.StatusNotFound {
		t.Fatalf("最后一个引用删除后文件仍在: %d", w.Code)
	}
	deletePhoto(t, r, int64(other["photo_id"].(float64)))
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("残留文件 %v", names)
	}
	if len(repo.photos) != 0 {
		t.Fatalf("残留元数据 %v", repo.photos)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestUploadRejectsBadRequests(t *testing.T) {
	image := []byte("image-bytes")
	valid := func() []formPart {
		return []formPart{{"file", "a.jpg", image}, {"caption", "", []byte("说明")}}
	}
	cases := []struct {
		name   string
		mutate func([]formPart) []formPart
	}{
		{"missing file", func(p []formPart) []formPart { return p[1:] }},
		{"unsupported type", func(p []formPart) []formPart { p[0].filename = "a.gif"; return p }},
		{"missing extension", func(p []formPart) []formPart { p[0].filename = "photo"; return p }},
		{"no filename", func(p []formPart) []formPart { p[0].filename = ""; return p }},
		{"empty file", func(p []formPart) []formPart { p[0].data = nil; return p }},
		{"duplicate field", func(p []formPart) []formPart { return append(p, formPart{"caption", "", []byte("x")}) }},
		{"unknown field", func(p []formPart) []formPart { return append(p, formPart{"meta", "", []byte("x")}) }},
		{"bad taken_at", func(p []formPart) []formPart { return append(p, formPart{"taken_at", "", []byte("昨天")}) }},
		{"bad sort_order", func(p []formPart) []formPart {
			return append(p, formPart{"sort_order", "", []byte("第一")})
		}},
		{"oversized caption", func(p []formPart) []formPart {
			return append(p[:1], formPart{"caption", "", bytes.Repeat([]byte("x"), int(maxFieldSize)+1)})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, repo, dir := newTestHandler(t)
			w := perform(r, uploadRequest(t, "b1", tc.mutate(valid())))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if code, _ := decodeEnvelope(t, w); code != "bad_request" {
				t.Fatalf("code = %q", code)
			}
			if names := dirNames(t, dir); len(names) != 0 {
				t.Fatalf("残留文件 %v", names)
			}
			if len(repo.photos) != 0 {
				t.Fatal("失败请求写入了元数据")
			}
		})
	}
}

func TestUploadAuthorizationLimitsAndFailures(t *testing.T) {
	r, repo, dir := newTestHandler(t)
	valid := []formPart{{"file", "a.jpg", []byte("image-bytes")}}
	// 令牌校验与 models 模块一致。
	req := uploadRequest(t, "b1", valid)
	req.Header.Del("X-Collect-Token")
	if w := perform(r, req); w.Code != http.StatusUnauthorized {
		t.Fatalf("缺少令牌 -> %d", w.Code)
	}
	req = uploadRequest(t, "b1", valid)
	req.Header.Set("X-Collect-Token", "wrong")
	if w := perform(r, req); w.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌 -> %d", w.Code)
	}
	// Content-Length 可知时的整体上限。
	req = uploadRequest(t, "b1", valid)
	req.ContentLength = MaxRequestSize + 1
	if w := perform(r, req); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("整体超限 -> %d", w.Code)
	}
	// Content-Length 不可知时按流式累计拦截单个文件。
	var prefix bytes.Buffer
	mw := multipart.NewWriter(&prefix)
	if _, err := mw.CreateFormFile("file", "large.jpg"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/buildings/b1/photos",
		io.MultiReader(bytes.NewReader(prefix.Bytes()), io.LimitReader(zeroReader{}, MaxFileSize+1)))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Collect-Token", "secret")
	if w := perform(r, req); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("单文件超限 -> %d %s", w.Code, w.Body.String())
	}
	// 合法 multipart 之后跟超长 epilogue 仍会触发整体上限。
	req = uploadRequest(t, "b1", valid)
	req.Body = io.NopCloser(io.MultiReader(req.Body, io.LimitReader(zeroReader{}, MaxRequestSize)))
	req.ContentLength = -1
	if w := perform(r, req); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("流式整体超限 -> %d %s", w.Code, w.Body.String())
	}
	// 建筑不存在 -> 404。
	repo.existsErr = httpx.NotFound("建筑不存在")
	if w := perform(r, uploadRequest(t, "b1", valid)); w.Code != http.StatusNotFound {
		t.Fatalf("建筑不存在 -> %d", w.Code)
	}
	repo.existsErr = nil
	// 落库失败 -> 500，且不留文件。
	repo.saveErr = errors.New("database failed")
	if w := perform(r, uploadRequest(t, "b1", valid)); w.Code != http.StatusInternalServerError {
		t.Fatalf("落库失败 -> %d", w.Code)
	}
	repo.saveErr = nil
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("失败请求残留文件 %v", names)
	}
	if len(repo.photos) != 0 {
		t.Fatal("失败请求写入了元数据")
	}
	// 正常上传仍然可用（临时文件已清理，不干扰后续请求）。
	photo := decodeData(t, perform(r, uploadRequest(t, "b1", valid)))
	serve(t, r, photo["url"].(string), []byte("image-bytes"), "image/jpeg")
	if names := dirNames(t, dir); len(names) != 1 {
		t.Fatalf("存储文件 %v", names)
	}
}

func TestDeleteUnknownPhoto(t *testing.T) {
	r, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/photos/999", nil)
	req.Header.Set("X-Collect-Token", "secret")
	w := perform(r, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("缺失图片 -> %d", w.Code)
	}
	if code, _ := decodeEnvelope(t, w); code != "not_found" {
		t.Fatalf("code = %q", code)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/photos/abc", nil)
	req.Header.Set("X-Collect-Token", "secret")
	if w := perform(r, req); w.Code != http.StatusBadRequest {
		t.Fatalf("非数字 photoId -> %d", w.Code)
	}
	if w := perform(r, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/photos/1", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("缺少令牌 -> %d", w.Code)
	}
}

func TestStaticRejectsUnsafeNames(t *testing.T) {
	r, _, dir := newTestHandler(t)
	secret := []byte("SECRET")
	if err := os.MkdirAll(filepath.Dir(dir), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.jpg"), secret, 0600); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{
		"/photos/",
		"/photos/../secret.jpg",
		"/photos/%2e%2e%2fsecret.jpg",
		"/photos//etc/passwd",
		"/photos/note.jpg",
		"/photos/.upload-123456",
		"/photos/0123456789abcdef0123456789abcdef.jpg",  // 形状合法但文件不存在
		"/photos/0123456789ABCDEF0123456789abcdef.jpg",  // 大写的十六进制
		"/photos/0123456789abcdef0123456789abcde.jpg",   // 31 位
		"/photos/0123456789abcdef0123456789abcdefg.jpg", // 非十六进制字符
		"/photos/0123456789abcdef0123456789abcdef.gif",  // 不支持的扩展名
	} {
		w := perform(r, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code == http.StatusOK || bytes.Contains(w.Body.Bytes(), secret) {
			t.Fatalf("GET %s -> %d %q", url, w.Code, w.Body.String())
		}
	}
	// 合法文件名照常服务。
	name := strings.Repeat("ab", 16) + ".jpg"
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	w := perform(r, httptest.NewRequest(http.MethodGet, StaticPrefix+name, nil))
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("合法文件 -> %d %q", w.Code, w.Body.String())
	}
}

func TestContentType(t *testing.T) {
	for name, want := range map[string]string{
		strings.Repeat("ab", 16) + ".jpg":  "image/jpeg",
		strings.Repeat("ab", 16) + ".jpeg": "image/jpeg",
		strings.Repeat("ab", 16) + ".png":  "image/png",
		strings.Repeat("ab", 16) + ".webp": "image/webp",
	} {
		if got := contentType(name); got != want {
			t.Fatalf("%s -> %q", name, got)
		}
	}
}
