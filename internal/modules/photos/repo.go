package photos

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 图片文件存放在 photos.storage_dir 下，数据库只保存元数据。
// 文件名固定为 32 位十六进制（内容 sha256 摘要前 128 位）+ 小写扩展名，
// 静态路由只服务该形状的名字，天然拒绝路径穿越；该正则与迁移中的 CHECK 保持一致。
var fileNamePattern = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|jpeg|png|webp)$`)

const hashPrefixLen = 32

// Photo 是一条实拍图片元数据；文件本体不在数据库中，URL 由存储文件名派生。
type Photo struct {
	PhotoID    int64      `json:"photo_id"`
	BuildingID string     `json:"building_id"`
	URL        string     `json:"url"`
	Caption    string     `json:"caption"`
	Source     string     `json:"source"`
	TakenAt    *time.Time `json:"taken_at"`
	SortOrder  int32      `json:"sort_order"`
	FileName   string     `json:"-"`
}

// setFileName 记录存储文件名并派生对外 URL。
func (p *Photo) setFileName(name string) {
	p.FileName = name
	p.URL = photoURL(name)
}

func photoURL(name string) string { return StaticPrefix + name }

// photoFileName 由内容摘要与小写扩展名生成存储文件名（内容寻址：同名即同内容）。
func photoFileName(sum, ext string) (string, error) {
	if len(sum) < hashPrefixLen {
		return "", fmt.Errorf("sha256 摘要长度不足 %d 位: %q", hashPrefixLen*4, sum)
	}
	name := sum[:hashPrefixLen] + ext
	if !fileNamePattern.MatchString(name) {
		return "", fmt.Errorf("非法的图片文件名: %q", name)
	}
	return name, nil
}

// validFileName 判断文件名是否可直接映射到存储目录内的文件（无路径分隔符、已知扩展名）。
func validFileName(name string) bool { return fileNamePattern.MatchString(name) }

type repository interface {
	Exists(context.Context, string) error
	List(context.Context, string) ([]Photo, error)
	Save(context.Context, *Photo) error
	Delete(context.Context, int64) (string, error)
}

type Repo struct{ pool *pgxpool.Pool }

// Exists 校验建筑存在；不存在返回 404。
func (r *Repo) Exists(ctx context.Context, id string) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM buildings WHERE building_id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return httpx.NotFound("建筑不存在")
	}
	return nil
}

// List 返回建筑的实拍图片（按 sort_order、photo_id 稳定排序）；建筑不存在返回 404。
func (r *Repo) List(ctx context.Context, buildingID string) ([]Photo, error) {
	if err := r.Exists(ctx, buildingID); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT photo_id,building_id,file_name,caption,source,taken_at,sort_order
 FROM building_photos WHERE building_id=$1 ORDER BY sort_order,photo_id`, buildingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Photo{}
	for rows.Next() {
		var p Photo
		if err := rows.Scan(&p.PhotoID, &p.BuildingID, &p.FileName, &p.Caption, &p.Source, &p.TakenAt, &p.SortOrder); err != nil {
			return nil, err
		}
		p.URL = photoURL(p.FileName)
		list = append(list, p)
	}
	return list, rows.Err()
}

// Save 写入一条元数据；建筑不存在返回 404。
func (r *Repo) Save(ctx context.Context, p *Photo) error {
	if err := r.Exists(ctx, p.BuildingID); err != nil {
		return err
	}
	if err := r.pool.QueryRow(ctx, `INSERT INTO building_photos(building_id,file_name,caption,source,taken_at,sort_order)
 VALUES($1,$2,$3,$4,$5,$6) RETURNING photo_id`,
		p.BuildingID, p.FileName, p.Caption, p.Source, p.TakenAt, p.SortOrder).Scan(&p.PhotoID); err != nil {
		return err
	}
	p.URL = photoURL(p.FileName)
	return nil
}

// Delete 删除一条元数据，返回需要清理的存储文件名；
// 内容相同的图片共享同一个文件，仍有其他记录引用时返回空串，避免删掉共享文件。
func (r *Repo) Delete(ctx context.Context, id int64) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.Background())
	var name string
	if err = tx.QueryRow(ctx, `DELETE FROM building_photos WHERE photo_id=$1 RETURNING file_name`, id).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", httpx.NotFound("图片不存在")
		}
		return "", err
	}
	var referenced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM building_photos WHERE file_name=$1)`, name).Scan(&referenced); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	if referenced {
		return "", nil
	}
	return name, nil
}
