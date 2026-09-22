package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type storedFile struct {
	Asset  string
	SHA256 string
	Size   int64
}
type repository interface {
	Exists(context.Context, string) error
	Save(context.Context, *Model, []storedFile) error
	Active(context.Context, string) (*Model, error)
	Asset(context.Context, string, string, string) (storedFile, error)
}
type Repo struct{ pool *pgxpool.Pool }

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

// Serialize writers on the building row, then publish the new version and its
// asset allowlist atomically. Files are synced/closed before entering here.
func (r *Repo) Save(ctx context.Context, m *Model, files []storedFile) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var id string
	if err = tx.QueryRow(ctx, `SELECT building_id FROM buildings WHERE building_id=$1 FOR UPDATE`, m.BuildingID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("建筑不存在")
		}
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE building_model_versions SET active=FALSE WHERE building_id=$1 AND active`, id); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO building_model_versions(version,building_id,manifest,active) VALUES($1,$2,$3,TRUE) RETURNING created_at`, m.Version, id, m.Raw).Scan(&m.CreatedAt); err != nil {
		return err
	}
	for _, f := range files {
		if _, err = tx.Exec(ctx, `INSERT INTO building_model_files(version,asset,sha256,size) VALUES($1,$2,$3,$4)`, m.Version, f.Asset, f.SHA256, f.Size); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		// A lost commit acknowledgement must not delete a successfully committed
		// version's files. Check via a fresh connection before reporting failure.
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var exists bool
		checkErr := r.pool.QueryRow(checkCtx, `SELECT EXISTS(SELECT 1 FROM building_model_versions WHERE version=$1)`, m.Version).Scan(&exists)
		if checkErr == nil && exists {
			return nil
		}
		if checkErr != nil {
			return &uncertainCommit{err}
		}
		return err
	}
	return nil
}

type uncertainCommit struct{ error }

func (r *Repo) Active(ctx context.Context, id string) (*Model, error) {
	// One SQL snapshot ensures manifest and files belong to the same committed version.
	var raw, assets []byte
	m := &Model{BuildingID: id}
	err := r.pool.QueryRow(ctx, `SELECT v.version,v.created_at,v.manifest,
 (SELECT jsonb_agg(jsonb_build_object('Asset',f.asset,'SHA256',f.sha256,'Size',f.size)) FROM building_model_files f WHERE f.version=v.version)
 FROM building_model_versions v WHERE v.building_id=$1 AND v.active`, id).Scan(&m.Version, &m.CreatedAt, &raw, &assets)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("建筑模型不存在")
	}
	if err != nil {
		return nil, err
	}
	if m.Manifest, err = parseManifest(raw); err != nil {
		return nil, err
	}
	m.Raw = raw
	var files []storedFile
	if err = json.Unmarshal(assets, &files); err != nil {
		return nil, err
	}
	m.setFiles(files)
	return m, nil
}

func (r *Repo) Asset(ctx context.Context, id, version, asset string) (storedFile, error) {
	var f storedFile
	f.Asset = asset
	err := r.pool.QueryRow(ctx, `SELECT f.sha256,f.size FROM building_model_files f JOIN building_model_versions v USING(version) WHERE v.building_id=$1 AND v.version=$2 AND f.asset=$3`, id, version, asset).Scan(&f.SHA256, &f.Size)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, httpx.NotFound("模型文件不存在")
	}
	return f, err
}

func (m *Model) setFiles(files []storedFile) {
	for _, f := range files {
		info := File{URL: "/api/v1/buildings/" + url.PathEscape(m.BuildingID) + "/model/versions/" + m.Version + "/files/" + f.Asset, SHA256: f.SHA256, Size: f.Size}
		if f.Asset == "building.glb" {
			m.File = info
			continue
		}
		for i := range m.Floors {
			if f.Asset == "floor_"+strconv.FormatInt(int64(m.Floors[i].LevelIndex), 10)+".glb" {
				copy := info
				m.Floors[i].File = &copy
			}
		}
	}
}
