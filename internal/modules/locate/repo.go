// Package locate Wi-Fi RSSI 指纹数据管理与定位基线（加权最近邻 WKNN）。
//
// 设计要点（对应技术路线）：
//   - 指纹 = 已知位置 + 该处可见的一组 (BSSID, RSSI)；用 BSSID 区分 AP；
//   - 定位输出"楼栋 + 楼层 + 候选位置 + 可信程度"，不输出假装精确的坐标；
//   - 算法基线为 WKNN，后续可替换为更复杂模型而不影响接口。
package locate

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/geo"
	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Candidate 一个指纹会话（位置 + 全部 AP 观测）。
type Candidate struct {
	SessionID  int64
	BuildingID string
	FloorID    int64
	Lng        float64
	Lat        float64
	Obs        map[string]int // bssid -> rssi
}

// CampusCS 读取全局本地坐标系锚点。
func (r *Repo) CampusCS(ctx context.Context) (geo.LocalCS, error) {
	var cs geo.LocalCS
	var desc *string
	err := r.pool.QueryRow(ctx,
		`SELECT origin_lon, origin_lat, rotation_deg, description FROM campus_cs WHERE id=1`).
		Scan(&cs.OriginLon, &cs.OriginLat, &cs.RotationDeg, &desc)
	if err == pgx.ErrNoRows {
		return geo.LocalCS{}, httpx.Unprocessable("campus_cs 未初始化，请先运行 seed 或插入坐标系锚点")
	}
	return cs, err
}

// ResolveFloor 由 building_id + level_index 找到 floor_id。
func (r *Repo) ResolveFloor(ctx context.Context, buildingID string, levelIndex int) (int64, error) {
	var floorID int64
	err := r.pool.QueryRow(ctx,
		`SELECT floor_id FROM floors WHERE building_id=$1 AND level_index=$2`,
		buildingID, levelIndex).Scan(&floorID)
	if err == pgx.ErrNoRows {
		return 0, httpx.NotFound("建筑不存在或没有该楼层")
	}
	return floorID, err
}

// InsertSession 写入一次指纹采集。
func (r *Repo) InsertSession(ctx context.Context, in InsertInput) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sessionID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO fp_sessions
			(building_id, floor_id, x_m, y_m, geom, captured_at, device_model, orientation_deg, note)
		VALUES ($1,$2,$3,$4,ST_SetSRID(ST_MakePoint($5,$6),4326),$7,$8,$9,$10)
		RETURNING session_id`,
		in.BuildingID, in.FloorID, in.XM, in.YM, in.Lng, in.Lat,
		in.CapturedAt, in.DeviceModel, in.OrientationDeg, in.Note).Scan(&sessionID)
	if err != nil {
		return 0, err
	}
	for _, o := range in.Observations {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fp_observations (session_id, bssid, ssid, rssi, freq_mhz)
			VALUES ($1,$2,$3,$4,$5)`,
			sessionID, o.BSSID, o.SSID, o.RSSI, o.FreqMHz); err != nil {
			return 0, err
		}
	}
	return sessionID, tx.Commit(ctx)
}

type InsertInput struct {
	BuildingID     string
	FloorID        int64
	XM, YM         *float64
	Lng, Lat       float64
	CapturedAt     time.Time
	DeviceModel    string
	OrientationDeg *float64
	Note           string
	Observations   []ObsInput
}

type ObsInput struct {
	BSSID    string
	SSID     string
	RSSI     int
	FreqMHz  *int
}

// FingerprintSummary 采集记录列表项。
type FingerprintSummary struct {
	SessionID    int64      `json:"session_id"`
	BuildingID   string     `json:"building_id"`
	BuildingName string     `json:"building_name,omitempty"`
	FloorName    string     `json:"floor_name,omitempty"`
	XM           *float64   `json:"x_m,omitempty"`
	YM           *float64   `json:"y_m,omitempty"`
	CapturedAt   time.Time  `json:"captured_at"`
	DeviceModel  string     `json:"device_model,omitempty"`
	ObsCount     int        `json:"obs_count"`
}

// ListSessions 按建筑/楼层列出采集记录。
func (r *Repo) ListSessions(ctx context.Context, buildingID string, floorID *int64, limit int) ([]FingerprintSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT s.session_id, COALESCE(s.building_id,''), COALESCE(b.name,''),
		       COALESCE(f.display_name,''), s.x_m, s.y_m, s.captured_at,
		       COALESCE(s.device_model,''), count(o.bssid)
		FROM fp_sessions s
		LEFT JOIN buildings b ON b.building_id = s.building_id
		LEFT JOIN floors f    ON f.floor_id    = s.floor_id
		LEFT JOIN fp_observations o ON o.session_id = s.session_id
		WHERE ($1 = '' OR s.building_id = $1)
		  AND ($2::bigint IS NULL OR s.floor_id = $2)
		GROUP BY s.session_id, b.name, f.display_name
		ORDER BY s.captured_at DESC
		LIMIT $3`, buildingID, floorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FingerprintSummary{}
	for rows.Next() {
		var s FingerprintSummary
		if err := rows.Scan(&s.SessionID, &s.BuildingID, &s.BuildingName, &s.FloorName,
			&s.XM, &s.YM, &s.CapturedAt, &s.DeviceModel, &s.ObsCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Candidates 取出观测到任一 BSSID 的指纹会话（含该会话全部观测）。
// buildingID 为空表示不过滤。
func (r *Repo) Candidates(ctx context.Context, bssids []string, buildingID string) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.session_id, COALESCE(s.building_id,''), COALESCE(s.floor_id,0),
		       ST_X(s.geom), ST_Y(s.geom), o.bssid, o.rssi
		FROM fp_sessions s
		JOIN fp_observations o ON o.session_id = s.session_id
		WHERE s.geom IS NOT NULL
		  AND o.bssid = ANY($1)
		  AND ($2 = '' OR s.building_id = $2)
		ORDER BY s.session_id`, bssids, buildingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Candidate
	var cur *Candidate
	lastID := int64(-1)
	for rows.Next() {
		var sid int64
		var bid, bssid string
		var fid int64
		var lng, lat float64
		var rssi int
		if err := rows.Scan(&sid, &bid, &fid, &lng, &lat, &bssid, &rssi); err != nil {
			return nil, err
		}
		if sid != lastID {
			out = append(out, Candidate{
				SessionID: sid, BuildingID: bid, FloorID: fid,
				Lng: lng, Lat: lat, Obs: map[string]int{},
			})
			cur = &out[len(out)-1]
			lastID = sid
		}
		cur.Obs[bssid] = rssi
	}
	return out, rows.Err()
}
