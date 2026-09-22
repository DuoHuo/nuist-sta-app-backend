package models

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGLBValidation(t *testing.T) {
	cases := []struct {
		name, doc string
		mutate    func([]byte) []byte
	}{
		{"magic", "", func(b []byte) []byte { b[0] = 'X'; return b }},
		{"version", "", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:8], 1); return b }},
		{"declared length", "", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:12], 1); return b }},
		{"truncated chunk", "", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[12:16], uint32(len(b))); return b }},
		{"unaligned chunk", "", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[12:16], 3); return b }},
		{"wrong chunk type", "", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[16:20], 0x004e4942); return b }},
		{"invalid json", `{broken`, nil},
		{"trailing json", `{"asset":{"version":"2.0"}} {}`, nil},
		{"null", "null", nil},
		{"asset version", `{"asset":{"version":"1.0"}}`, nil},
		{"external buffer", `{"asset":{"version":"2.0"},"buffers":[{"uri":"https://example.org/data.bin","byteLength":4}]}`, nil},
		{"external image", `{"asset":{"version":"2.0"},"images":[{"uri":"../texture.png"}]}`, nil},
		{"extension uri", `{"asset":{"version":"2.0"},"extensions":{"custom":{"uri":"file:///secret"}}}`, nil},
		{"bad buffer range", `{"asset":{"version":"2.0"},"bufferViews":[{"buffer":0,"byteLength":10}]}`, nil},
		{"cycle", `{"asset":{"version":"2.0"},"nodes":[{"children":[1]},{"children":[0]}]}`, nil},
		{"nonfinite", `{"asset":{"version":"2.0"},"nodes":[{"translation":[1e999,0,0]}]}`, nil},
		{"bad reference", `{"asset":{"version":"2.0"},"nodes":[{"mesh":1}]}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := sampleGLB(t)
			if tc.doc != "" {
				b = makeGLB(t, tc.doc)
			}
			if tc.mutate != nil {
				b = tc.mutate(b)
			}
			path := filepath.Join(t.TempDir(), "model.glb")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := validateGLB(path); err == nil {
				t.Fatal("invalid GLB accepted")
			}
		})
	}
}
func TestManifestValidation(t *testing.T) {
	cases := []string{
		"null", sampleManifest + " {}",
		strings.Replace(sampleManifest, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(sampleManifest, `"longitude":118.7`, `"longitude":181`, 1),
		strings.Replace(sampleManifest, `"rotation_deg":0,`, "", 1),
		strings.Replace(sampleManifest, `"level_index":0,`, "", 1),
		strings.Replace(sampleManifest, `"elevation_m":0,`, "", 1),
		strings.Replace(sampleManifest, `"node_name":"floor_0"`, `"node_name":" "`, 1),
		strings.Replace(sampleManifest, `"elevation_m":0`, `"elevation_m":1e999`, 1),
		`{"schema_version":1,"coordinate_system":"building-local-meters-y-up","origin":{"longitude":0,"latitude":0},"rotation_deg":0,"floors":[]}`,
	}
	for _, raw := range cases {
		if _, err := parseManifest([]byte(raw)); err == nil {
			t.Errorf("invalid manifest accepted: %s", raw)
		}
	}
	if _, err := parseManifest([]byte(sampleManifest)); err != nil {
		t.Fatal(err)
	}
}
