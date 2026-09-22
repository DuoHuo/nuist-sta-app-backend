package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 轻量迁移器：migrations/NNNN_name.{up,down}.sql，按版本号顺序执行，
// 已应用版本记录在 schema_migrations 表。

var upFileRe = regexp.MustCompile(`^(\d+)_[\w-]+\.up\.sql$`)
var downFileRe = regexp.MustCompile(`^(\d+)_[\w-]+\.down\.sql$`)

func ensureTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    BIGINT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int64]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// MigrateUp 应用所有未执行的 up 迁移，返回本次应用的版本数。
func MigrateUp(ctx context.Context, pool *pgxpool.Pool, dir string) (int, error) {
	if err := ensureTable(ctx, pool); err != nil {
		return 0, fmt.Errorf("创建 schema_migrations: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("读取迁移目录 %s: %w", dir, err)
	}
	type mig struct {
		version int64
		path    string
	}
	var migs []mig
	for _, e := range entries {
		if m := upFileRe.FindStringSubmatch(e.Name()); m != nil {
			v, _ := strconv.ParseInt(m[1], 10, 64)
			migs = append(migs, mig{v, filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	done, err := appliedVersions(ctx, pool)
	if err != nil {
		return 0, err
	}
	applied := 0
	for _, m := range migs {
		if done[m.version] {
			continue
		}
		sql, err := os.ReadFile(m.path)
		if err != nil {
			return applied, err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return applied, err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("迁移 %s 失败: %w", filepath.Base(m.path), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, m.version); err != nil {
			_ = tx.Rollback(ctx)
			return applied, err
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// MigrateDown 回滚最近 steps 个已应用版本。
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, dir string, steps int) error {
	if err := ensureTable(ctx, pool); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读取迁移目录 %s: %w", dir, err)
	}
	type mig struct {
		version int64
		path    string
	}
	byVersion := map[int64]string{}
	for _, e := range entries {
		if m := downFileRe.FindStringSubmatch(e.Name()); m != nil {
			v, _ := strconv.ParseInt(m[1], 10, 64)
			byVersion[v] = filepath.Join(dir, e.Name())
		}
	}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT $1`, steps)
	if err != nil {
		return err
	}
	var versions []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, v := range versions {
		path, ok := byVersion[v]
		if !ok {
			return fmt.Errorf("版本 %d 缺少 down 迁移文件", v)
		}
		sql, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("回滚 %s 失败: %w", filepath.Base(path), err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, v); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// MigrationStatus 返回已应用版本与待应用数量。
func MigrationStatus(ctx context.Context, pool *pgxpool.Pool, dir string) (applied []int64, pending int, err error) {
	if err := ensureTable(ctx, pool); err != nil {
		return nil, 0, err
	}
	done, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	for v := range done {
		applied = append(applied, v)
	}
	sort.Slice(applied, func(i, j int) bool { return applied[i] < applied[j] })
	for _, e := range entries {
		if m := upFileRe.FindStringSubmatch(e.Name()); m != nil {
			v, _ := strconv.ParseInt(m[1], 10, 64)
			if !done[v] {
				pending++
			}
		}
	}
	return applied, pending, nil
}
