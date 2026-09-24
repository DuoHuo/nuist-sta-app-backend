package admin

import (
	"encoding/json"
	"testing"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

// 这些校验不碰数据库，可以在 CI 里直接跑；需要 PostGIS 的部分（ST_IsValid、
// 面积上限）留给服务器上的端到端验收。
func TestValidateKind(t *testing.T) {
	for _, kind := range featureKinds {
		if err := validateKind(kind); err != nil {
			t.Fatalf("白名单里的 %q 被拒绝: %v", kind, err)
		}
	}
	if err := validateKind("spaceship"); err == nil {
		t.Fatal("白名单外的类别应被拒绝")
	}
}

func TestNormalizeStatus(t *testing.T) {
	cases := map[string]string{"": "published", "published": "published", "draft": "draft"}
	for in, want := range cases {
		got, err := normalizeStatus(in)
		if err != nil || got != want {
			t.Fatalf("normalizeStatus(%q) = (%q, %v)，期望 %q", in, got, err, want)
		}
	}
	if _, err := normalizeStatus("hidden"); err == nil {
		t.Fatal("未知状态应被拒绝")
	}
}

func TestNormalizeProps(t *testing.T) {
	for _, in := range []string{"", "null", "{}", `{"open":"08:00"}`} {
		got, err := normalizeProps(json.RawMessage(in))
		if err != nil {
			t.Fatalf("normalizeProps(%q) 报错: %v", in, err)
		}
		if string(got) == "" {
			t.Fatalf("normalizeProps(%q) 返回空", in)
		}
	}
	// 数组、字符串不是对象，必须拒绝而不是静默丢弃。
	for _, in := range []string{"[]", `"text"`, "123"} {
		if _, err := normalizeProps(json.RawMessage(in)); err == nil {
			t.Fatalf("normalizeProps(%q) 应被拒绝", in)
		}
	}
	// 空值归一为 {}，避免入库 NULL 撞上 NOT NULL 默认值语义。
	got, err := normalizeProps(nil)
	if err != nil || string(got) != "{}" {
		t.Fatalf("空 props -> (%s, %v)，期望 {}", got, err)
	}
}

func TestEachPositionWalksEveryNesting(t *testing.T) {
	parse := func(raw string) any {
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	cases := map[string]int{
		`[118.7, 32.2]`:                  1,
		`[[118.7, 32.2], [118.8, 32.3]]`: 2,
		`[[[118.7, 32.2], [118.8, 32.2], [118.8, 32.3], [118.7, 32.2]]]`: 4,
		`[[[[118.7, 32.2]]], [[[118.8, 32.3]]]]`:                         2, // MultiPolygon 内层
	}
	for raw, want := range cases {
		count := 0
		eachPosition(parse(raw), func([]any) bool { count++; return true })
		if count != want {
			t.Fatalf("eachPosition(%s) 遍历 %d 个坐标，期望 %d", raw, count, want)
		}
	}
	// 提前短路：返回 false 后不再继续遍历。
	count := 0
	eachPosition(parse(`[[118.7, 32.2], [118.8, 32.3]]`), func([]any) bool {
		count++
		return false
	})
	if count != 1 {
		t.Fatalf("短路后遍历了 %d 次，期望 1", count)
	}
}

func TestCreateFeatureRejectsNonObjectProps(t *testing.T) {
	// 走不到数据库：props 校验在几何校验之前失败。
	repo := &Repo{}
	_, err := repo.CreateFeature(t.Context(), FeatureCreate{
		Kind:     "green",
		Name:     "草坪",
		Geometry: json.RawMessage(`{"type":"Point","coordinates":[118.7,32.2]}`),
		Props:    json.RawMessage(`[1,2]`),
	}, "tester")
	var apiErr *httpx.Error
	if !asError(err, &apiErr) || apiErr.Status != 422 {
		t.Fatalf("非对象 props -> %v，期望 422", err)
	}
}

// asError 是 errors.As 的小包装，避免测试文件再引一次 errors。
func asError(err error, target **httpx.Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*httpx.Error)
	if ok {
		*target = e
	}
	return ok
}
