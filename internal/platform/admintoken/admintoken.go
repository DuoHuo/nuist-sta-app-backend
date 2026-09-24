// Package admintoken 解析并校验管理写接口的共享令牌。
//
// 配置项 server.collect_token（环境变量 CAMPUS_COLLECT_TOKEN）支持两种写法：
//
//	"secret"                        单个令牌，提交人标签为空
//	"张三:secretA,李四:secretB"      多个带标签的令牌，标签随写操作记入 created_by
//
// 未配置令牌时写接口一律拒绝（fail-closed）。此前的实现是"token 为空就放行"，
// 而 docker-compose.yml 从未设置该变量——等于把新建/删除建筑与 POI 的权限
// 交给了任何能访问到服务的人。
package admintoken

import (
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/DuoHuo/nuist-sta-app-backend/internal/httpx"
)

const contextKey = "campus_admin_editor"

type entry struct {
	label string
	value string
}

// Tokens 是解析后的令牌集合；零值表示"未配置任何令牌"。
type Tokens struct {
	entries []entry
	err     error
}

// FromSpec 解析配置字符串。永不 panic：写错格式时 Middleware 会拒绝所有写请求，
// 并由 Err 报告原因（服务启动时调用一次即可让配置错误当场暴露）。
func FromSpec(spec string) *Tokens {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return &Tokens{}
	}
	t := &Tokens{}
	seen := map[string]string{}
	for _, raw := range strings.Split(spec, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			return &Tokens{err: fmt.Errorf("令牌列表存在空项（多余逗号？）")}
		}
		label, value := "", part
		if i := strings.Index(part, ":"); i >= 0 {
			label, value = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
			if label == "" || value == "" {
				return &Tokens{err: fmt.Errorf("令牌项 %q 格式应为 名字:令牌", part)}
			}
		}
		if prev, dup := seen[value]; dup {
			return &Tokens{err: fmt.Errorf("令牌重复（%s 与 %s）", prev, label)}
		}
		seen[value] = label
		t.entries = append(t.entries, entry{label: label, value: value})
	}
	return t
}

// Err 返回配置解析错误；非 nil 时所有写请求都被拒绝。
func (t *Tokens) Err() error { return t.err }

// Configured 表示是否配置了至少一个令牌。
func (t *Tokens) Configured() bool { return len(t.entries) > 0 }

// Count 返回已配置的令牌个数。
func (t *Tokens) Count() int { return len(t.entries) }

// Check 校验一个令牌，返回它的提交人标签。比较用常数时间，避免按字符提前返回。
func (t *Tokens) Check(presented string) (string, bool) {
	for _, e := range t.entries {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(e.value)) == 1 {
			return e.label, true
		}
	}
	return "", false
}

// Middleware 校验 X-Collect-Token，并把提交人标签放进请求上下文（见 Editor）。
func (t *Tokens) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if t.err != nil {
			httpx.Err(c, 503, "write_disabled", "管理令牌配置有误："+t.err.Error())
			return
		}
		if !t.Configured() {
			httpx.Err(c, 503, "write_disabled",
				"服务器未配置管理令牌（server.collect_token / CAMPUS_COLLECT_TOKEN），写接口已禁用")
			return
		}
		label, ok := t.Check(c.GetHeader("X-Collect-Token"))
		if !ok {
			httpx.Err(c, 401, "unauthorized", "缺少或错误的 X-Collect-Token")
			return
		}
		c.Set(contextKey, label)
		c.Next()
	}
}

// Editor 返回当前请求的提交人标签（无标签或未鉴权时为空串）。
func Editor(c *gin.Context) string {
	if v, ok := c.Get(contextKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
