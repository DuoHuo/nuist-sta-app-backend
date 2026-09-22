package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

var versionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var floorPartPattern = regexp.MustCompile(`^floor_(0|[1-9][0-9]*|-[1-9][0-9]*)$`)

type Handler struct {
	repo       repository
	storageDir string
	token      string
}

func Register(rg *gin.RouterGroup, cfg *config.Config, pool *pgxpool.Pool) {
	dir := cfg.Models.StorageDir
	if dir == "" {
		dir = "data/models"
	}
	h := &Handler{repo: &Repo{pool: pool}, storageDir: dir, token: cfg.Server.CollectToken}
	h.register(rg)
}

func (h *Handler) register(rg *gin.RouterGroup) {
	rg.POST("/admin/buildings/:buildingId/model", h.requireToken, h.upload)
	rg.GET("/buildings/:buildingId/model", h.active)
	rg.GET("/buildings/:buildingId/model/versions/:version/files/:asset", h.download)
	rg.HEAD("/buildings/:buildingId/model/versions/:version/files/:asset", h.download)
}

func (h *Handler) requireToken(c *gin.Context) {
	if h.token != "" && c.GetHeader("X-Collect-Token") != h.token {
		httpx.Err(c, http.StatusUnauthorized, "unauthorized", "缺少或错误的 X-Collect-Token")
		return
	}
	c.Next()
}

func (h *Handler) active(c *gin.Context) {
	m, err := h.repo.Active(c.Request.Context(), c.Param("buildingId"))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, m)
}

func tooLarge() error {
	return httpx.NewError(http.StatusRequestEntityTooLarge, "body_too_large", "单文件最大64MiB，manifest最大1MiB，总请求最大128MiB")
}
func multipartError(err error) error {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return tooLarge()
	}
	return httpx.BadRequest("无效的 multipart 请求: " + err.Error())
}

func (h *Handler) upload(c *gin.Context) {
	m, err := h.storeUpload(c)
	if err != nil {
		log.Printf("model upload: %v", err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, m)
}

func (h *Handler) storeUpload(c *gin.Context) (*Model, error) {
	if c.Request.ContentLength > MaxRequestSize {
		return nil, tooLarge()
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxRequestSize)
	id := c.Param("buildingId")
	if err := h.repo.Exists(c.Request.Context(), id); err != nil {
		return nil, err
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return nil, multipartError(err)
	}
	if err = os.MkdirAll(h.storageDir, 0750); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(h.storageDir, ".upload-")
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			if err := os.RemoveAll(stage); err != nil {
				log.Printf("model upload cleanup %s: %v", stage, err)
			}
		}
	}()
	var raw []byte
	files := map[string]storedFile{}
	nodes := map[string]map[string]int{}
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, multipartError(err)
		}
		name := part.FormName()
		if seen[name] || (name != "manifest" && name != "file" && !floorPartPattern.MatchString(name)) || len(seen) >= maxFloors+2 {
			return nil, httpx.BadRequest("重复、未知或过多的 multipart 字段")
		}
		seen[name] = true
		if name == "manifest" {
			raw, err = io.ReadAll(io.LimitReader(part, maxManifestSize+1))
			if err != nil {
				return nil, multipartError(err)
			}
			if int64(len(raw)) > maxManifestSize {
				return nil, tooLarge()
			}
			continue
		}
		asset := name + ".glb"
		if name == "file" {
			asset = "building.glb"
		}
		path := filepath.Join(stage, asset)
		info, err := writeAsset(path, asset, part)
		if err != nil {
			return nil, err
		}
		names, err := validateGLB(path)
		if err != nil {
			return nil, httpx.BadRequest(name + ": " + err.Error())
		}
		files[name], nodes[name] = info, names
	}
	// Account for the entire HTTP body, including a multipart epilogue.
	if _, err = io.Copy(io.Discard, c.Request.Body); err != nil {
		return nil, multipartError(err)
	}
	if !seen["file"] || !seen["manifest"] {
		return nil, httpx.BadRequest("file 和 manifest 为必填字段")
	}
	manifest, err := parseManifest(raw)
	if err != nil {
		return nil, httpx.BadRequest(err.Error())
	}
	var identity struct {
		BuildingID *string `json:"building_id"`
	}
	if err = json.Unmarshal(raw, &identity); err != nil {
		return nil, httpx.BadRequest("invalid building_id")
	}
	if identity.BuildingID != nil && *identity.BuildingID != id {
		return nil, httpx.BadRequest("manifest building_id 与路径不一致")
	}
	if len(files) != len(manifest.Floors)+1 {
		return nil, httpx.BadRequest("单层文件必须与 manifest.floors 一一对应")
	}
	all := []storedFile{files["file"]}
	for _, floor := range manifest.Floors {
		name := "floor_" + strconv.FormatInt(int64(floor.LevelIndex), 10)
		f, ok := files[name]
		if !ok {
			return nil, httpx.BadRequest("缺少单层文件 " + name)
		}
		if nodes["file"][floor.NodeName] != 1 || nodes[name][floor.NodeName] != 1 {
			return nil, httpx.BadRequest("整栋和单层 GLB 必须包含唯一节点 " + floor.NodeName)
		}
		all = append(all, f)
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	m := &Model{Manifest: manifest, BuildingID: id, Version: hex.EncodeToString(nonce[:]), Raw: raw}
	final := filepath.Join(h.storageDir, m.Version)
	// Reserve the version directory; never replace another version, even on collision.
	if err = os.Mkdir(final, 0750); err != nil {
		return nil, err
	}
	defer func() {
		if cleanup {
			if err := os.RemoveAll(final); err != nil {
				log.Printf("model version cleanup %s: %v", final, err)
			}
		}
	}()
	for _, f := range all {
		if err = os.Rename(filepath.Join(stage, f.Asset), filepath.Join(final, f.Asset)); err != nil {
			return nil, err
		}
	}
	if err = os.Remove(stage); err != nil {
		return nil, err
	}
	m.setFiles(all)
	if err = h.repo.Save(c.Request.Context(), m, all); err != nil {
		var uncertain *uncertainCommit
		if errors.As(err, &uncertain) {
			// Do not destroy assets that may already be committed and active.
			cleanup = false
			log.Printf("model commit outcome unknown; retain version %s for reconciliation", m.Version)
		}
		return nil, err
	}
	cleanup = false
	return m, nil
}

func writeAsset(path, asset string, src io.Reader) (storedFile, error) {
	info := storedFile{Asset: asset}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return info, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(src, MaxFileSize+1))
	if copyErr == nil && n > MaxFileSize {
		copyErr = tooLarge()
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		var limit *http.MaxBytesError
		if errors.As(copyErr, &limit) {
			return info, tooLarge()
		}
		if errors.Is(copyErr, io.ErrUnexpectedEOF) {
			return info, multipartError(copyErr)
		}
		return info, copyErr
	}
	if closeErr != nil {
		return info, closeErr
	}
	info.Size, info.SHA256 = n, hex.EncodeToString(hash.Sum(nil))
	return info, nil
}

func (h *Handler) download(c *gin.Context) {
	version, asset := c.Param("version"), c.Param("asset")
	if !versionPattern.MatchString(version) || (asset != "building.glb" && !(len(asset) > 4 && filepath.Ext(asset) == ".glb" && floorPartPattern.MatchString(asset[:len(asset)-4]))) {
		httpx.Respond(c, httpx.NotFound("模型文件不存在"))
		return
	}
	info, err := h.repo.Asset(c.Request.Context(), c.Param("buildingId"), version, asset)
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	f, err := os.Open(filepath.Join(h.storageDir, version, asset))
	if errors.Is(err, os.ErrNotExist) {
		httpx.Respond(c, httpx.NotFound("模型文件不存在"))
		return
	}
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	if !stat.Mode().IsRegular() || stat.Size() != info.Size {
		httpx.Respond(c, fmt.Errorf("stored model size mismatch"))
		return
	}
	c.Header("Content-Type", "model/gltf-binary")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("ETag", `"`+info.SHA256+`"`)
	http.ServeContent(c.Writer, c.Request, asset, stat.ModTime(), f)
}
