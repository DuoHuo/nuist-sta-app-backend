package admintoken

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFromSpec(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		want    map[string]string // 令牌 -> 标签
		wantErr string
	}{
		{name: "未配置", spec: "", want: map[string]string{}},
		{name: "单个无标签令牌", spec: "secret", want: map[string]string{"secret": ""}},
		{name: "带名字的令牌", spec: "张三:aaa", want: map[string]string{"aaa": "张三"}},
		{name: "多个令牌含空格", spec: " a:1 , b:2 ", want: map[string]string{"1": "a", "2": "b"}},
		{name: "令牌里带冒号", spec: "a:1:2", want: map[string]string{"1:2": "a"}},
		{name: "重复令牌", spec: "a:1,b:1", wantErr: "令牌重复"},
		{name: "空项", spec: "a:1,", wantErr: "空项"},
		{name: "缺名字", spec: ":secret", wantErr: "格式应为"},
		{name: "缺令牌", spec: "label:", wantErr: "格式应为"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens := FromSpec(tc.spec)
			if tc.wantErr != "" {
				if tokens.Err() == nil {
					t.Fatalf("期望报错包含 %q，实际无错", tc.wantErr)
				}
				if !strings.Contains(tokens.Err().Error(), tc.wantErr) {
					t.Fatalf("错误信息 %q 不含 %q", tokens.Err(), tc.wantErr)
				}
				if tokens.Configured() {
					t.Fatal("解析失败时不应视为已配置")
				}
				return
			}
			if err := tokens.Err(); err != nil {
				t.Fatalf("非预期错误: %v", err)
			}
			if tokens.Count() != len(tc.want) {
				t.Fatalf("令牌数 %d，期望 %d", tokens.Count(), len(tc.want))
			}
			for token, label := range tc.want {
				got, ok := tokens.Check(token)
				if !ok || got != label {
					t.Fatalf("令牌 %q -> (%q, %v)，期望 (%q, true)", token, got, ok, label)
				}
			}
		})
	}
}

func TestMiddlewareFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := func(spec, header string) (int, string) {
		engine := gin.New()
		engine.POST("/write", FromSpec(spec).Middleware(), func(c *gin.Context) {
			c.String(http.StatusOK, "editor=%s", Editor(c))
		})
		req := httptest.NewRequest(http.MethodPost, "/write", nil)
		if header != "" {
			req.Header.Set("X-Collect-Token", header)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// 未配置令牌：写接口一律拒绝（旧实现是直接放行，公网部署等于不设防）。
	if code, body := request("", "anything"); code != http.StatusServiceUnavailable {
		t.Fatalf("未配置令牌 -> %d %s，期望 503", code, body)
	}
	// 配置写错：同样拒绝，并说明原因。
	if code, _ := request("a:1,", "1"); code != http.StatusServiceUnavailable {
		t.Fatalf("配置错误 -> %d，期望 503", code)
	}
	// 配置正确：缺令牌、错令牌都 401。
	if code, _ := request("admin:secret", ""); code != http.StatusUnauthorized {
		t.Fatalf("缺令牌 -> %d，期望 401", code)
	}
	if code, _ := request("admin:secret", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("错令牌 -> %d，期望 401", code)
	}
	// 正确令牌：放行，并把标签带进上下文。
	code, body := request("admin:secret", "secret")
	if code != http.StatusOK {
		t.Fatalf("正确令牌 -> %d %s，期望 200", code, body)
	}
	if body != "editor=admin" {
		t.Fatalf("提交人标签 %q，期望 editor=admin", body)
	}
	// 无标签令牌：放行，标签为空。
	if _, body := request("secret", "secret"); body != "editor=" {
		t.Fatalf("无标签令牌 -> %q，期望空标签", body)
	}
}
