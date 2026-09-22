// Package config 加载地图后端配置：默认值 -> YAML -> 环境变量覆盖。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   Server   `yaml:"server"`
	Database Database `yaml:"database"`
	Map      Map      `yaml:"map"`
	Locate   Locate   `yaml:"locate"`
	Models   Models   `yaml:"models"`
	Photos   Photos   `yaml:"photos"`
}

type Models struct {
	StorageDir string `yaml:"storage_dir"`
}

type Photos struct {
	StorageDir string `yaml:"storage_dir"`
}

type Server struct {
	Addr         string   `yaml:"addr"`
	BaseURL      string   `yaml:"base_url"`
	CollectToken string   `yaml:"collect_token"`
	CORSOrigins  []string `yaml:"cors_origins"`
}

type Database struct {
	DSN      string `yaml:"dsn"`
	MaxConns int32  `yaml:"max_conns"`
}

type Map struct {
	TileURL     string     `yaml:"tile_url"`
	StyleURL    string     `yaml:"style_url"`
	GlyphsURL   string     `yaml:"glyphs_url"`
	SpritesURL  string     `yaml:"sprites_url"`
	Attribution string     `yaml:"attribution"`
	Bounds      [4]float64 `yaml:"bounds"` // [minLon, minLat, maxLon, maxLat]
}

type Locate struct {
	K                int     `yaml:"k"`
	MinMatchedAPs    int     `yaml:"min_matched_aps"`
	MissingPenaltyDB float64 `yaml:"missing_penalty_db"`
	MaxSnapM         float64 `yaml:"max_snap_m"`
}

func Default() *Config {
	return &Config{
		Models: Models{StorageDir: "data/models"},
		Photos: Photos{StorageDir: "data/photos"},
		Server: Server{
			Addr:        ":8080",
			CORSOrigins: nil,
		},
		Database: Database{
			DSN:      "postgres://campus:campus@localhost:5432/campus?sslmode=disable",
			MaxConns: 8,
		},
		Map: Map{
			TileURL:     "http://localhost:3000/campus/{z}/{x}/{y}",
			StyleURL:    "",
			Attribution: "© OpenStreetMap contributors",
			// 南信大主校区实测 bbox（OSM relation/13070911 外环，见 data/osm/README.md）
			Bounds: [4]float64{118.6910, 32.1956, 118.7229, 32.2099},
		},
		Locate: Locate{
			K:                4,
			MinMatchedAPs:    2,
			MissingPenaltyDB: 12,
			MaxSnapM:         150,
		},
	}
}

// Load 按顺序应用：默认值 -> path 指定的 YAML（可为空）-> 环境变量。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置 %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置 %s: %w", path, err)
		}
	}
	applyEnv(cfg)
	if cfg.Database.DSN == "" {
		return nil, fmt.Errorf("database.dsn 不能为空")
	}
	if cfg.Server.Addr == "" {
		return nil, fmt.Errorf("server.addr 不能为空")
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	set := func(key string, fn func(string)) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			fn(v)
		}
	}
	set("CAMPUS_MODEL_STORAGE_DIR", func(v string) { cfg.Models.StorageDir = v })
	set("CAMPUS_PHOTO_STORAGE_DIR", func(v string) { cfg.Photos.StorageDir = v })
	set("CAMPUS_SERVER_ADDR", func(v string) { cfg.Server.Addr = v })
	set("CAMPUS_SERVER_BASE_URL", func(v string) { cfg.Server.BaseURL = v })
	set("CAMPUS_COLLECT_TOKEN", func(v string) { cfg.Server.CollectToken = v })
	set("CAMPUS_DATABASE_DSN", func(v string) { cfg.Database.DSN = v })
	set("CAMPUS_MAP_TILE_URL", func(v string) { cfg.Map.TileURL = v })
	set("CAMPUS_MAP_STYLE_URL", func(v string) { cfg.Map.StyleURL = v })
	set("CAMPUS_MAP_GLYPHS_URL", func(v string) { cfg.Map.GlyphsURL = v })
}
