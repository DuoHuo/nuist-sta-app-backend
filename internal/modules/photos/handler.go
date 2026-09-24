package photos

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/config"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/platform/admintoken"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// MaxFileSize 单张图片上限 8MiB。
	MaxFileSize int64 = 8 << 20
	// MaxRequestSize 上传请求整体上限（含 multipart 开销）；server.bodyLimit 按 UploadRoute 单独放宽。
	MaxRequestSize int64 = 9 << 20
	// maxFieldSize caption/source 等文本字段上限。
	maxFieldSize int64 = 1 << 16
	// UploadRoute 上传路由；server 的限流中间件按此路径放宽请求体上限。
	UploadRoute = "/api/v1/admin/buildings/:buildingId/photos"
	// StaticRoute 图片静态路由；StaticPrefix 是对外 URL 前缀。
	StaticRoute  = "/photos/*filepath"
	StaticPrefix = "/photos/"
)

// 允许的图片扩展名（小写）。
var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

// 允许的 multipart 字段名。
var fieldNames = map[string]bool{"file": true, "caption": true, "source": true, "taken_at": true, "sort_order": true}

type Handler struct {
	repo       repository
	storageDir string
	write      gin.HandlerFunc
}

// Register 挂载实拍图片模块：/api/v1 下的读写接口，以及 /photos 下的图片静态文件（不走 /api/v1）。
func Register(rg *gin.RouterGroup, engine *gin.Engine, cfg *config.Config, pool *pgxpool.Pool) {
	dir := cfg.Photos.StorageDir
	if dir == "" {
		dir = "data/photos"
	}
	h := &Handler{
		repo:       &Repo{pool: pool},
		storageDir: dir,
		write:      admintoken.FromSpec(cfg.Server.CollectToken).Middleware(),
	}
	h.register(rg, engine)
}

func (h *Handler) register(rg *gin.RouterGroup, engine *gin.Engine) {
	rg.GET("/buildings/:buildingId/photos", h.list)
	rg.POST("/admin/buildings/:buildingId/photos", h.write, h.upload)
	rg.DELETE("/admin/photos/:photoId", h.write, h.remove)
	// 文件名是内容哈希：同一 URL 的内容永不改变。
	engine.GET(StaticRoute, h.static)
	engine.HEAD(StaticRoute, h.static)
}

func (h *Handler) list(c *gin.Context) {
	list, err := h.repo.List(c.Request.Context(), c.Param("buildingId"))
	if err != nil {
		httpx.Respond(c, err)
		return
	}
	if list == nil {
		list = []Photo{}
	}
	httpx.OK(c, gin.H{"photos": list, "count": len(list)})
}

func tooLarge() error {
	return httpx.NewError(http.StatusRequestEntityTooLarge, "body_too_large", "单张图片最大8MiB，总请求最大9MiB")
}

func multipartError(err error) error {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return tooLarge()
	}
	return httpx.BadRequest("无效的 multipart 请求: " + err.Error())
}

func (h *Handler) upload(c *gin.Context) {
	p, err := h.storeUpload(c)
	if err != nil {
		log.Printf("photo upload: %v", err)
		httpx.Respond(c, err)
		return
	}
	httpx.OK(c, p)
}

func (h *Handler) storeUpload(c *gin.Context) (*Photo, error) {
	if c.Request.ContentLength > MaxRequestSize {
		return nil, tooLarge()
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxRequestSize)
	p := &Photo{BuildingID: c.Param("buildingId")}
	if err := h.repo.Exists(c.Request.Context(), p.BuildingID); err != nil {
		return nil, err
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return nil, multipartError(err)
	}
	if err = os.MkdirAll(h.storageDir, 0750); err != nil {
		return nil, err
	}
	// 先落到存储目录内的临时文件（同目录保证改名是原子操作），
	// 校验通过后才按内容哈希改名，静态路由不会读到半截文件。
	stage, err := os.CreateTemp(h.storageDir, ".upload-*")
	if err != nil {
		return nil, err
	}
	staged := stage.Name()
	// Windows 上已打开的文件无法删除，反序注册：先关闭再清理临时文件。
	defer func() {
		if err := os.Remove(staged); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("photo upload cleanup %s: %v", staged, err)
		}
	}()
	defer func() {
		if err := stage.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			log.Printf("photo upload close %s: %v", staged, err)
		}
	}()
	seen := map[string]bool{}
	var ext, sum string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, multipartError(err)
		}
		name := part.FormName()
		if seen[name] || !fieldNames[name] {
			return nil, httpx.BadRequest("重复或未知的 multipart 字段")
		}
		seen[name] = true
		switch name {
		case "file":
			ext = strings.ToLower(filepath.Ext(part.FileName()))
			if !imageExts[ext] {
				return nil, httpx.BadRequest("file 仅支持 jpg/jpeg/png/webp 图片")
			}
			hash := sha256.New()
			n, err := io.Copy(io.MultiWriter(stage, hash), io.LimitReader(part, MaxFileSize+1))
			if err != nil {
				return nil, multipartError(err)
			}
			if n > MaxFileSize {
				return nil, tooLarge()
			}
			if n == 0 {
				return nil, httpx.BadRequest("file 不能为空")
			}
			if err = stage.Sync(); err != nil {
				return nil, err
			}
			sum = hex.EncodeToString(hash.Sum(nil))
		case "caption":
			if p.Caption, err = readField(part); err != nil {
				return nil, err
			}
		case "source":
			if p.Source, err = readField(part); err != nil {
				return nil, err
			}
		case "taken_at":
			raw, err := readField(part)
			if err != nil {
				return nil, err
			}
			if raw = strings.TrimSpace(raw); raw != "" {
				taken, err := time.Parse(time.RFC3339, raw)
				if err != nil {
					return nil, httpx.BadRequest("taken_at 应为 RFC3339 时间")
				}
				p.TakenAt = &taken
			}
		case "sort_order":
			raw, err := readField(part)
			if err != nil {
				return nil, err
			}
			if raw = strings.TrimSpace(raw); raw != "" {
				n, err := strconv.ParseInt(raw, 10, 32)
				if err != nil {
					return nil, httpx.BadRequest("sort_order 应为 32 位整数")
				}
				p.SortOrder = int32(n)
			}
		}
	}
	// 记账整个 HTTP body（含 multipart epilogue），超限请求即使 Content-Length 不可知也会被拒绝。
	if _, err = io.Copy(io.Discard, c.Request.Body); err != nil {
		return nil, multipartError(err)
	}
	if !seen["file"] {
		return nil, httpx.BadRequest("file 为必填字段")
	}
	// Windows 上已打开的文件无法改名，需先关闭临时文件。
	if err = stage.Close(); err != nil {
		return nil, err
	}
	name, err := photoFileName(sum, ext)
	if err != nil {
		return nil, err
	}
	created, err := h.commitFile(staged, name, sum)
	if err != nil {
		return nil, err
	}
	p.setFileName(name)
	if err = h.repo.Save(c.Request.Context(), p); err != nil {
		// 元数据未写入时只清理本次新建的文件；复用他人文件的情况下必须保留。
		if created {
			if rmErr := os.Remove(filepath.Join(h.storageDir, name)); rmErr != nil {
				log.Printf("photo upload cleanup %s: %v", name, rmErr)
			}
		}
		return nil, err
	}
	return p, nil
}

// readField 读取一个文本字段；整体受请求体上限约束，单字段另有上限。
func readField(part io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(part, maxFieldSize+1))
	if err != nil {
		return "", multipartError(err)
	}
	if int64(len(raw)) > maxFieldSize {
		return "", httpx.BadRequest("文本字段过长")
	}
	return string(raw), nil
}

// commitFile 把临时文件搬到 <storage>/<name>，返回文件是否由本次请求新建。
// 内容寻址：同名即同内容，已存在则直接复用、绝不覆盖；摘要前 128 位碰撞（同名不同内容）时报错。
func (h *Handler) commitFile(staged, name, sum string) (bool, error) {
	target := filepath.Join(h.storageDir, name)
	switch _, err := os.Stat(target); {
	case err == nil:
		existing, err := hashFile(target)
		if err != nil {
			return false, err
		}
		if existing != sum {
			return false, errors.New("图片文件名冲突: " + name)
		}
		return false, nil
	case errors.Is(err, os.ErrNotExist):
		return true, os.Rename(staged, target)
	default:
		return false, err
	}
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (h *Handler) remove(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("photoId"), 10, 64)
	if err != nil {
		httpx.Err(c, http.StatusBadRequest, "bad_request", "photoId 应为数字")
		return
	}
	name, err := h.repo.Delete(c.Request.Context(), id)
	if err != nil {
		log.Printf("photo delete %d: %v", id, err)
		httpx.Respond(c, err)
		return
	}
	if name != "" {
		// 文件不属于数据库事务：元数据已删除，文件删除失败只记录日志。
		if err = os.Remove(filepath.Join(h.storageDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("photo delete file %s: %v", name, err)
		}
	}
	httpx.OK(c, gin.H{"deleted": true})
}

// static 服务 /photos/<name>：只接受内容哈希形状的文件名，因此不存在路径穿越。
func (h *Handler) static(c *gin.Context) {
	name := strings.TrimPrefix(c.Param("filepath"), "/")
	if !validFileName(name) {
		httpx.Respond(c, httpx.NotFound("图片不存在"))
		return
	}
	f, err := os.Open(filepath.Join(h.storageDir, name))
	if errors.Is(err, os.ErrNotExist) {
		httpx.Respond(c, httpx.NotFound("图片不存在"))
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
	if !stat.Mode().IsRegular() {
		httpx.Respond(c, httpx.NotFound("图片不存在"))
		return
	}
	c.Header("Content-Type", contentType(name))
	c.Header("X-Content-Type-Options", "nosniff")
	// 文件名为内容哈希，内容不可变，可安全长期缓存。
	c.Header("Cache-Control", "public, max-age=86400")
	http.ServeContent(c.Writer, c.Request, name, stat.ModTime(), f)
}

// contentType 由扩展名给出图片类型（文件名已通过 validFileName 校验）。
func contentType(name string) string {
	switch filepath.Ext(name) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
