package models

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
)

// validateGLB accepts self-contained glTF 2.0 GLB files. Resources must use the
// embedded BIN chunk (URI resources, including data URIs, are not accepted).
func validateGLB(path string) (map[string]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	if size < 20 || size > MaxFileSize {
		return nil, fmt.Errorf("invalid GLB size")
	}
	var header [12]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return nil, err
	}
	if string(header[:4]) != "glTF" || binary.LittleEndian.Uint32(header[4:8]) != 2 || int64(binary.LittleEndian.Uint32(header[8:])) != size {
		return nil, fmt.Errorf("invalid GLB magic, version or declared length")
	}
	var raw []byte
	var binSize int64 = -1
	for offset, index := int64(12), 0; offset < size; index++ {
		var h [8]byte
		if size-offset < 8 {
			return nil, fmt.Errorf("truncated GLB chunk header")
		}
		if _, err = io.ReadFull(f, h[:]); err != nil {
			return nil, err
		}
		n, kind := int64(binary.LittleEndian.Uint32(h[:4])), binary.LittleEndian.Uint32(h[4:])
		if n%4 != 0 || n > size-offset-8 {
			return nil, fmt.Errorf("invalid GLB chunk length")
		}
		switch {
		case index == 0 && kind == 0x4e4f534a && n > 0:
			raw = make([]byte, int(n))
			if _, err = io.ReadFull(f, raw); err != nil {
				return nil, err
			}
		case index == 1 && kind == 0x004e4942:
			binSize = n
			if _, err = f.Seek(n, io.SeekCurrent); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("GLB must contain JSON followed by at most one BIN chunk")
		}
		offset += 8 + n
	}
	var tree any
	if err = json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("invalid GLB JSON: %w", err)
	}
	if err = validateJSONValues(tree); err != nil {
		return nil, err
	}
	var doc struct {
		Asset struct {
			Version    string `json:"version"`
			MinVersion string `json:"minVersion"`
		} `json:"asset"`
		Buffers []struct {
			ByteLength int64 `json:"byteLength"`
		} `json:"buffers"`
		BufferViews []struct {
			Buffer     int   `json:"buffer"`
			ByteOffset int64 `json:"byteOffset"`
			ByteLength int64 `json:"byteLength"`
			ByteStride int   `json:"byteStride"`
		} `json:"bufferViews"`
		Accessors []struct {
			BufferView    *int            `json:"bufferView"`
			ByteOffset    int64           `json:"byteOffset"`
			ComponentType int             `json:"componentType"`
			Count         int64           `json:"count"`
			Type          string          `json:"type"`
			Sparse        json.RawMessage `json:"sparse"`
		} `json:"accessors"`
		Nodes []struct {
			Name        string    `json:"name"`
			Matrix      []float64 `json:"matrix"`
			Translation []float64 `json:"translation"`
			Rotation    []float64 `json:"rotation"`
			Scale       []float64 `json:"scale"`
			Children    []int     `json:"children"`
			Mesh        *int      `json:"mesh"`
		} `json:"nodes"`
		Meshes []struct {
			Primitives []struct {
				Attributes map[string]int `json:"attributes"`
				Indices    *int           `json:"indices"`
				Mode       *int           `json:"mode"`
				Material   *int           `json:"material"`
			} `json:"primitives"`
		} `json:"meshes"`
		Materials []json.RawMessage `json:"materials"`
		Images    []struct {
			BufferView *int   `json:"bufferView"`
			MimeType   string `json:"mimeType"`
		} `json:"images"`
		Scenes []struct {
			Nodes []int `json:"nodes"`
		} `json:"scenes"`
		Scene *int `json:"scene"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid glTF document: %w", err)
	}
	if doc.Asset.Version != "2.0" || (doc.Asset.MinVersion != "" && doc.Asset.MinVersion != "2.0") {
		return nil, fmt.Errorf("glTF asset.version must be 2.0")
	}
	if len(doc.Buffers) > 1 {
		return nil, fmt.Errorf("GLB supports one embedded buffer")
	}
	if len(doc.Buffers) == 1 {
		n := doc.Buffers[0].ByteLength
		if n <= 0 || binSize < n || binSize-n > 3 {
			return nil, fmt.Errorf("buffer byteLength does not match BIN chunk")
		}
	} else if binSize >= 0 {
		return nil, fmt.Errorf("BIN chunk has no buffer")
	}
	for _, v := range doc.BufferViews {
		if v.Buffer != 0 || len(doc.Buffers) != 1 || v.ByteOffset < 0 || v.ByteLength <= 0 || v.ByteOffset > doc.Buffers[0].ByteLength || v.ByteLength > doc.Buffers[0].ByteLength-v.ByteOffset || (v.ByteStride != 0 && (v.ByteStride < 4 || v.ByteStride > 252 || v.ByteStride%4 != 0)) {
			return nil, fmt.Errorf("invalid bufferView range or stride")
		}
	}
	for _, a := range doc.Accessors {
		component := map[int]int64{5120: 1, 5121: 1, 5122: 2, 5123: 2, 5125: 4, 5126: 4}[a.ComponentType]
		dims := map[string]int64{"SCALAR": 1, "VEC2": 2, "VEC3": 3, "VEC4": 4, "MAT2": 4, "MAT3": 9, "MAT4": 16}[a.Type]
		if component == 0 || dims == 0 || a.Count <= 0 || a.ByteOffset < 0 || a.ByteOffset%component != 0 {
			return nil, fmt.Errorf("invalid accessor")
		}
		element := component * dims
		// Matrix columns are aligned to four-byte boundaries.
		if a.Type == "MAT2" {
			element = ((2*component + 3) / 4 * 4) * 2
		}
		if a.Type == "MAT3" {
			element = ((3*component + 3) / 4 * 4) * 3
		}
		if a.BufferView != nil {
			if !validIndex(*a.BufferView, len(doc.BufferViews)) {
				return nil, fmt.Errorf("invalid accessor bufferView")
			}
			v := doc.BufferViews[*a.BufferView]
			stride := element
			if v.ByteStride > 0 {
				stride = int64(v.ByteStride)
			}
			if stride < element || a.ByteOffset > v.ByteLength || element > v.ByteLength-a.ByteOffset || a.Count-1 > (v.ByteLength-a.ByteOffset-element)/stride {
				return nil, fmt.Errorf("accessor exceeds bufferView")
			}
		}
	}
	names := map[string]int{}
	parents := make([]int, len(doc.Nodes))
	for _, n := range doc.Nodes {
		if n.Name != "" {
			names[n.Name]++
		}
		for _, t := range []struct {
			v []float64
			n int
		}{{n.Matrix, 16}, {n.Translation, 3}, {n.Rotation, 4}, {n.Scale, 3}} {
			if t.v != nil && len(t.v) != t.n {
				return nil, fmt.Errorf("invalid node transform length")
			}
			for _, x := range t.v {
				if !finite(x) {
					return nil, fmt.Errorf("non-finite node transform")
				}
			}
		}
		if n.Matrix != nil && (n.Translation != nil || n.Rotation != nil || n.Scale != nil) {
			return nil, fmt.Errorf("node cannot mix matrix and TRS")
		}
		if n.Rotation != nil {
			q := 0.0
			for _, x := range n.Rotation {
				q += x * x
			}
			if math.Abs(q-1) > 1e-3 {
				return nil, fmt.Errorf("node rotation must be a unit quaternion")
			}
		}
		if n.Mesh != nil && !validIndex(*n.Mesh, len(doc.Meshes)) {
			return nil, fmt.Errorf("invalid node mesh")
		}
		for _, child := range n.Children {
			if !validIndex(child, len(doc.Nodes)) {
				return nil, fmt.Errorf("invalid child node")
			}
			parents[child]++
			if parents[child] > 1 {
				return nil, fmt.Errorf("node has multiple parents")
			}
		}
	}
	// Iterative traversal avoids stack overflow on maliciously deep hierarchies.
	queue := make([]int, 0, len(doc.Nodes))
	for i, p := range parents {
		if p == 0 {
			queue = append(queue, i)
		}
	}
	for i := 0; i < len(queue); i++ {
		queue = append(queue, doc.Nodes[queue[i]].Children...)
	}
	if len(queue) != len(doc.Nodes) {
		return nil, fmt.Errorf("cyclic node hierarchy")
	}
	for _, s := range doc.Scenes {
		seen := map[int]bool{}
		for _, n := range s.Nodes {
			if !validIndex(n, len(doc.Nodes)) || parents[n] != 0 || seen[n] {
				return nil, fmt.Errorf("invalid scene root")
			}
			seen[n] = true
		}
	}
	if doc.Scene != nil && !validIndex(*doc.Scene, len(doc.Scenes)) {
		return nil, fmt.Errorf("invalid default scene")
	}
	for _, m := range doc.Meshes {
		if len(m.Primitives) == 0 {
			return nil, fmt.Errorf("mesh has no primitives")
		}
		for _, p := range m.Primitives {
			if len(p.Attributes) == 0 {
				return nil, fmt.Errorf("primitive has no attributes")
			}
			for _, a := range p.Attributes {
				if !validIndex(a, len(doc.Accessors)) {
					return nil, fmt.Errorf("invalid attribute accessor")
				}
			}
			if p.Indices != nil && !validIndex(*p.Indices, len(doc.Accessors)) {
				return nil, fmt.Errorf("invalid indices accessor")
			}
			if p.Material != nil && !validIndex(*p.Material, len(doc.Materials)) {
				return nil, fmt.Errorf("invalid material")
			}
			if p.Mode != nil && (*p.Mode < 0 || *p.Mode > 6) {
				return nil, fmt.Errorf("invalid primitive mode")
			}
		}
	}
	for _, im := range doc.Images {
		if im.BufferView == nil || !validIndex(*im.BufferView, len(doc.BufferViews)) || (im.MimeType != "image/png" && im.MimeType != "image/jpeg" && im.MimeType != "image/webp" && im.MimeType != "image/ktx2") {
			return nil, fmt.Errorf("images must use an embedded bufferView and supported mimeType")
		}
	}
	return names, nil
}

func validIndex(i, n int) bool { return i >= 0 && i < n }
func validateJSONValues(v any) error {
	switch value := v.(type) {
	case map[string]any:
		for k, child := range value {
			if k == "uri" {
				return fmt.Errorf("URI resources are forbidden; embed resources in the GLB BIN chunk")
			}
			if err := validateJSONValues(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateJSONValues(child); err != nil {
				return err
			}
		}
	case float64:
		if !finite(value) {
			return fmt.Errorf("non-finite JSON number")
		}
	}
	return nil
}
