package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

const (
	MaxFileSize     int64 = 64 << 20
	MaxRequestSize  int64 = 128 << 20
	maxManifestSize int64 = 1 << 20
	maxFloors             = 256
	UploadRoute           = "/api/v1/admin/buildings/:buildingId/model"
)

type Origin struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

type File struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size_bytes"`
}

type Floor struct {
	LevelIndex  int32   `json:"level_index"`
	DisplayName string  `json:"display_name"`
	ElevationM  float64 `json:"elevation_m"`
	NodeName    string  `json:"node_name"`
	File        *File   `json:"file,omitempty"`
}

type Manifest struct {
	SchemaVersion    int     `json:"schema_version"`
	CoordinateSystem string  `json:"coordinate_system"`
	Origin           Origin  `json:"origin"`
	RotationDeg      float64 `json:"rotation_deg"`
	Floors           []Floor `json:"floors"`
}

type Model struct {
	Manifest
	BuildingID string          `json:"building_id"`
	Version    string          `json:"version"`
	CreatedAt  time.Time       `json:"created_at"`
	Raw        json.RawMessage `json:"-"`
	File       File            `json:"file"`
}

func parseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	// Pointers distinguish required zero-valued fields from omitted fields.
	var input struct {
		SchemaVersion    *int   `json:"schema_version"`
		CoordinateSystem string `json:"coordinate_system"`
		Origin           *struct {
			Longitude *float64 `json:"longitude"`
			Latitude  *float64 `json:"latitude"`
		} `json:"origin"`
		RotationDeg *float64 `json:"rotation_deg"`
		Floors      []struct {
			LevelIndex  *int32   `json:"level_index"`
			DisplayName string   `json:"display_name"`
			ElevationM  *float64 `json:"elevation_m"`
			NodeName    string   `json:"node_name"`
		} `json:"floors"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	// Unknown fields are preserved in Model.Raw for provenance metadata.
	if err := d.Decode(&input); err != nil {
		return m, fmt.Errorf("invalid manifest JSON: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return m, fmt.Errorf("manifest must contain one JSON object")
	}
	if input.SchemaVersion == nil || *input.SchemaVersion != 1 || input.CoordinateSystem != "building-local-meters-y-up" {
		return m, fmt.Errorf("schema_version must be 1 and coordinate_system must be building-local-meters-y-up")
	}
	if input.Origin == nil || input.Origin.Longitude == nil || input.Origin.Latitude == nil || input.RotationDeg == nil {
		return m, fmt.Errorf("origin.longitude, origin.latitude and rotation_deg are required")
	}
	lon, lat, rotation := *input.Origin.Longitude, *input.Origin.Latitude, *input.RotationDeg
	if !finite(lon) || !finite(lat) || !finite(rotation) || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return m, fmt.Errorf("invalid origin or rotation_deg")
	}
	if input.Floors == nil || len(input.Floors) == 0 || len(input.Floors) > maxFloors {
		return m, fmt.Errorf("floors must be a nonempty array with at most %d entries", maxFloors)
	}
	m = Manifest{SchemaVersion: 1, CoordinateSystem: input.CoordinateSystem, Origin: Origin{lon, lat}, RotationDeg: rotation, Floors: make([]Floor, 0, len(input.Floors))}
	levels, names := map[int32]bool{}, map[string]bool{}
	for _, f := range input.Floors {
		if f.LevelIndex == nil || f.ElevationM == nil || !finite(*f.ElevationM) || strings.TrimSpace(f.DisplayName) == "" || strings.TrimSpace(f.NodeName) == "" || len(f.DisplayName) > 256 || len(f.NodeName) > 1024 {
			return m, fmt.Errorf("each floor requires level_index, display_name, finite elevation_m and node_name")
		}
		if levels[*f.LevelIndex] || names[f.NodeName] {
			return m, fmt.Errorf("duplicate floor level_index or node_name")
		}
		levels[*f.LevelIndex], names[f.NodeName] = true, true
		m.Floors = append(m.Floors, Floor{LevelIndex: *f.LevelIndex, DisplayName: f.DisplayName, ElevationM: *f.ElevationM, NodeName: f.NodeName})
	}
	return m, nil
}

func (m Model) MarshalJSON() ([]byte, error) {
	type plain Model
	base, err := json.Marshal(plain(m))
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if len(m.Raw) > 0 {
		if err = json.Unmarshal(m.Raw, &fields); err != nil {
			return nil, err
		}
	}
	canonical := map[string]json.RawMessage{}
	_ = json.Unmarshal(base, &canonical)
	var originalFloors []map[string]json.RawMessage
	_ = json.Unmarshal(fields["floors"], &originalFloors)
	floors := make([]map[string]json.RawMessage, len(m.Floors))
	for i, f := range m.Floors {
		item := map[string]json.RawMessage{}
		if i < len(originalFloors) {
			item = originalFloors[i]
		}
		// Never echo client-provided download metadata as a persisted file.
		for _, k := range []string{"file", "url", "sha256", "size_bytes", "version"} {
			delete(item, k)
		}
		encoded, _ := json.Marshal(f)
		typed := map[string]json.RawMessage{}
		_ = json.Unmarshal(encoded, &typed)
		for k, v := range typed {
			item[k] = v
		}
		if f.File != nil {
			item["url"], _ = json.Marshal(f.File.URL)
			item["sha256"], _ = json.Marshal(f.File.SHA256)
			item["size_bytes"], _ = json.Marshal(f.File.Size)
			item["version"], _ = json.Marshal(m.Version)
		}
		floors[i] = item
	}
	for k, v := range canonical {
		fields[k] = v
	}
	fields["floors"], _ = json.Marshal(floors)
	fields["url"], _ = json.Marshal(m.File.URL)
	fields["sha256"], _ = json.Marshal(m.File.SHA256)
	fields["size_bytes"], _ = json.Marshal(m.File.Size)
	return json.Marshal(fields)
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
