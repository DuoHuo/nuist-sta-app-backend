package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/admintoken"
	"github.com/gin-gonic/gin"
)

type memoryRepo struct {
	active    *Model
	versions  map[string]*Model
	assets    map[string][]storedFile
	saveErr   error
	existsErr error
}

func (r *memoryRepo) Exists(context.Context, string) error { return r.existsErr }
func (r *memoryRepo) Save(_ context.Context, m *Model, files []storedFile) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	m.CreatedAt = time.Now().UTC()
	r.active = m
	r.versions[m.Version] = m
	r.assets[m.Version] = files
	return nil
}
func (r *memoryRepo) Active(_ context.Context, id string) (*Model, error) {
	if r.active == nil || r.active.BuildingID != id {
		return nil, httpx.NotFound("no model")
	}
	// Rebuild from persisted manifest and asset metadata, just like the SQL repository.
	m := *r.active
	var err error
	m.Manifest, err = parseManifest(m.Raw)
	if err != nil {
		return nil, err
	}
	m.setFiles(r.assets[m.Version])
	return &m, nil
}
func (r *memoryRepo) Asset(_ context.Context, id, version, asset string) (storedFile, error) {
	if m := r.versions[version]; m != nil && m.BuildingID == id {
		for _, f := range r.assets[version] {
			if f.Asset == asset {
				return f, nil
			}
		}
	}
	return storedFile{}, httpx.NotFound("no asset")
}
func newTestHandler(t *testing.T) (*gin.Engine, *memoryRepo, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	repo := &memoryRepo{versions: map[string]*Model{}, assets: map[string][]storedFile{}}
	dir := t.TempDir()
	h := &Handler{repo: repo, storageDir: dir, write: admintoken.FromSpec("secret").Middleware()}
	h.register(r.Group("/api/v1"))
	return r, repo, dir
}
func makeGLB(t *testing.T, doc string) []byte {
	t.Helper()
	for len(doc)%4 != 0 {
		doc += " "
	}
	b := make([]byte, 20+len(doc))
	copy(b, "glTF")
	binary.LittleEndian.PutUint32(b[4:8], 2)
	binary.LittleEndian.PutUint32(b[8:12], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[12:16], uint32(len(doc)))
	binary.LittleEndian.PutUint32(b[16:20], 0x4e4f534a)
	copy(b[20:], doc)
	return b
}
func sampleGLB(t *testing.T) []byte {
	return makeGLB(t, `{"asset":{"version":"2.0"},"nodes":[{"name":"floor_0"}],"scenes":[{"nodes":[0]}],"scene":0}`)
}

const sampleManifest = `{"schema_version":1,"coordinate_system":"building-local-meters-y-up","origin":{"longitude":118.7,"latitude":32.2},"rotation_deg":0,"building_id":"b1","source":"plans","estimated":true,"notes":"estimate","name":"明德楼","floors":[{"level_index":0,"display_name":"1F","elevation_m":0,"node_name":"floor_0","file":"original.glb","source_photo":"photo.jpg"}]}`

type formPart struct {
	name, filename string
	data           []byte
}

func uploadRequest(t *testing.T, parts []formPart) *http.Request {
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
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/buildings/b1/model", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Collect-Token", "secret")
	return req
}
func standardParts(t *testing.T) []formPart {
	return []formPart{{"file", "../../evil.glb", sampleGLB(t)}, {"manifest", "", []byte(sampleManifest)}, {"floor_0", "floor.glb", sampleGLB(t)}}
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
func TestUploadReadVersionsAndDownloads(t *testing.T) {
	r, repo, dir := newTestHandler(t)
	if w := perform(r, httptest.NewRequest("GET", "/api/v1/buildings/b1/model", nil)); w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
	for _, fileManifest := range []bool{false, true} {
		parts := standardParts(t)
		if fileManifest {
			parts[1].filename = "manifest.json"
		}
		data := decodeData(t, perform(r, uploadRequest(t, parts)))
		for key, want := range map[string]any{"source": "plans", "estimated": true, "notes": "estimate", "building_id": "b1", "name": "明德楼"} {
			if data[key] != want {
				t.Errorf("%s = %v", key, data[key])
			}
		}
		glb := sampleGLB(t)
		hash := sha256.Sum256(glb)
		if data["size_bytes"] != float64(len(glb)) || data["sha256"] != hex.EncodeToString(hash[:]) {
			t.Fatalf("wrong file metadata %v", data)
		}
		floor := data["floors"].([]any)[0].(map[string]any)
		if floor["level_index"] != float64(0) || floor["display_name"] != "1F" || floor["elevation_m"] != float64(0) || floor["node_name"] != "floor_0" || floor["source_photo"] != "photo.jpg" {
			t.Fatalf("floor: %v", floor)
		}
		if floor["file"].(map[string]any)["url"] != floor["url"] || floor["size_bytes"] != float64(len(glb)) || floor["version"] != data["version"] {
			t.Fatal("floor metadata mismatch")
		}
		active := decodeData(t, perform(r, httptest.NewRequest("GET", "/api/v1/buildings/b1/model", nil)))
		if active["version"] != data["version"] {
			t.Fatal("active not updated")
		}
		for _, url := range []string{data["url"].(string), floor["url"].(string)} {
			w := perform(r, httptest.NewRequest("GET", url, nil))
			if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), glb) || w.Header().Get("Content-Type") != "model/gltf-binary" {
				t.Fatalf("download: %d %s", w.Code, w.Body.String())
			}
			req := httptest.NewRequest("GET", url, nil)
			req.Header.Set("Range", "bytes=0-3")
			if w := perform(r, req); w.Code != 206 || w.Body.String() != "glTF" {
				t.Fatalf("range %d %q", w.Code, w.Body.String())
			}
			req = httptest.NewRequest("GET", url, nil)
			req.Header.Set("If-None-Match", w.Header().Get("ETag"))
			if w := perform(r, req); w.Code != 304 {
				t.Fatalf("etag %d", w.Code)
			}
			if w := perform(r, httptest.NewRequest("HEAD", url, nil)); w.Code != 200 || w.Body.Len() != 0 {
				t.Fatalf("head %d", w.Code)
			}
			if w := perform(r, httptest.NewRequest("GET", strings.Replace(url, "/b1/", "/other/", 1), nil)); w.Code != 404 {
				t.Fatalf("cross-building %d", w.Code)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || len(repo.versions) != 2 {
		t.Fatal("versions overwritten or temporary files remain")
	}
	for version := range repo.versions {
		url := "/api/v1/buildings/b1/model/versions/" + version + "/files/building.glb"
		if w := perform(r, httptest.NewRequest("GET", url, nil)); w.Code != 200 {
			t.Fatal("old version unavailable")
		}
	}
}
func TestFailedUploadPreservesActiveAndCleansFiles(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]formPart) []formPart
		dbFail bool
	}{
		{"bad glb", func(p []formPart) []formPart { p[0].data = []byte("no glb"); return p }, false},
		{"bad floor", func(p []formPart) []formPart { p[2].data = []byte("no glb"); return p }, false},
		{"missing file", func(p []formPart) []formPart { return p[1:] }, false},
		{"missing floor", func(p []formPart) []formPart { return p[:2] }, false},
		{"missing manifest", func(p []formPart) []formPart { return []formPart{p[0], p[2]} }, false},
		{"duplicate", func(p []formPart) []formPart { return append(p, p[0]) }, false},
		{"unknown", func(p []formPart) []formPart { return append(p, formPart{"other", "", []byte("x")}) }, false},
		{"extra floor", func(p []formPart) []formPart { return append(p, formPart{"floor_1", "f.glb", sampleGLB(t)}) }, false},
		{"path field", func(p []formPart) []formPart { p[2].name = "floor_../../x"; return p }, false},
		{"invalid manifest", func(p []formPart) []formPart { p[1].data = []byte(`{}`); return p }, false},
		{"wrong building", func(p []formPart) []formPart {
			p[1].data = []byte(strings.Replace(sampleManifest, "b1", "b2", 1))
			return p
		}, false},
		{"wrong node", func(p []formPart) []formPart {
			p[2].data = makeGLB(t, `{"asset":{"version":"2.0"},"nodes":[{"name":"floor_9"}]}`)
			return p
		}, false},
		{"duplicate node", func(p []formPart) []formPart {
			p[0].data = makeGLB(t, `{"asset":{"version":"2.0"},"nodes":[{"name":"floor_0"},{"name":"floor_0"}]}`)
			return p
		}, false},
		{"database error", func(p []formPart) []formPart { return p }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, repo, dir := newTestHandler(t)
			decodeData(t, perform(r, uploadRequest(t, standardParts(t))))
			old := repo.active.Version
			if tc.dbFail {
				repo.saveErr = errors.New("database failed")
			}
			w := perform(r, uploadRequest(t, tc.mutate(standardParts(t))))
			want := 400
			if tc.dbFail {
				want = 500
			}
			if w.Code != want {
				t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
			}
			if repo.active.Version != old {
				t.Fatal("active changed")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != old {
				t.Fatalf("leftovers: %v", entries)
			}
		})
	}
}
func TestUploadAuthorizationAndLimits(t *testing.T) {
	r, repo, dir := newTestHandler(t)
	req := uploadRequest(t, standardParts(t))
	req.Header.Del("X-Collect-Token")
	if w := perform(r, req); w.Code != 401 {
		t.Fatalf("auth: %d", w.Code)
	}
	req = uploadRequest(t, standardParts(t))
	req.ContentLength = MaxRequestSize + 1
	if w := perform(r, req); w.Code != 413 {
		t.Fatalf("total limit: %d", w.Code)
	}
	parts := standardParts(t)
	parts[1].data = bytes.Repeat([]byte(" "), int(maxManifestSize+1))
	if w := perform(r, uploadRequest(t, parts)); w.Code != 413 {
		t.Fatalf("manifest limit: %d", w.Code)
	}
	// Streaming input tests the limit when Content-Length is unavailable.
	var prefix bytes.Buffer
	mw := multipart.NewWriter(&prefix)
	if _, err := mw.CreateFormFile("file", "large.glb"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("POST", "/api/v1/admin/buildings/b1/model", io.MultiReader(bytes.NewReader(prefix.Bytes()), io.LimitReader(zeroReader{}, MaxFileSize+1)))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Collect-Token", "secret")
	if w := perform(r, req); w.Code != 413 {
		t.Fatalf("stream file limit: %d %s", w.Code, w.Body.String())
	}
	// A valid multipart followed by an oversized epilogue still exceeds the request cap.
	req = uploadRequest(t, standardParts(t))
	req.Body = io.NopCloser(io.MultiReader(req.Body, io.LimitReader(zeroReader{}, MaxRequestSize)))
	req.ContentLength = -1
	if w := perform(r, req); w.Code != 413 {
		t.Fatalf("stream request limit: %d %s", w.Code, w.Body.String())
	}
	repo.existsErr = httpx.NotFound("building missing")
	if w := perform(r, uploadRequest(t, standardParts(t))); w.Code != 404 {
		t.Fatalf("building: %d", w.Code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || repo.active != nil {
		t.Fatal("failed requests left state")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestMingdeGeneratedAssets(t *testing.T) {
	base := filepath.Join("..", "..", "..", "data", "mingde")
	raw, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("local generated Mingde fixtures are not present")
	}
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Floors) != 7 {
		t.Fatalf("expected seven floors, got %d", len(manifest.Floors))
	}
	all, err := os.ReadFile(filepath.Join(base, "mingde.glb"))
	if err != nil {
		t.Fatal(err)
	}
	parts := []formPart{{"file", "mingde.glb", all}, {"manifest", "manifest.json", raw}}
	for _, f := range manifest.Floors {
		filename := fmt.Sprintf("mingde-%dF.glb", f.LevelIndex+1)
		b, err := os.ReadFile(filepath.Join(base, filename))
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, formPart{fmt.Sprintf("floor_%d", f.LevelIndex), filename, b})
	}
	r, _, _ := newTestHandler(t)
	req := uploadRequest(t, parts)
	var identity struct {
		BuildingID string `json:"building_id"`
	}
	if err = json.Unmarshal(raw, &identity); err != nil {
		t.Fatal(err)
	}
	req.URL.Path = "/api/v1/admin/buildings/" + identity.BuildingID + "/model"
	data := decodeData(t, perform(r, req))
	floors := data["floors"].([]any)
	if len(floors) != 7 {
		t.Fatal("floor count")
	}
	for _, f := range floors {
		floor := f.(map[string]any)
		if floor["url"] == "" || floor["source_photo"] == nil {
			t.Fatalf("missing metadata %v", floor)
		}
	}
}
