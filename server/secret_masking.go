package server

import (
	"encoding/json"
	"strings"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/notify"
)

// maskSecret 返回 secret 的掩码值（沿用通知渠道的语义与标记前缀）。
//
// 复用 notify.MaskedValue / notify.IsMasked 而不是各写一套：这两处必须能互认——
// 前端把掩码值原样提交回来时，服务端要认得出「这是没改」。
func maskSecret(secret string) string { return notify.MaskedValue(secret) }

// isMaskedSecret 报告接口收到的是否为掩码值（= 保持库中原值不变）。
func isMaskedSecret(v string) bool { return notify.IsMasked(v) }

// maskedKeys 是 MCP env / HTTP 工具 headers 里**一律掩码**的键名（大小写不敏感、
// 子串匹配）。刻意用"疑似凭据"的宽匹配：宁可多遮一个无害的头，也不要把 token 吐回去。
var maskedKeyFragments = []string{
	"key", "token", "secret", "password", "passwd", "auth", "credential", "cookie", "signature",
}

func looksSecretKey(k string) bool {
	low := strings.ToLower(k)
	for _, f := range maskedKeyFragments {
		if strings.Contains(low, f) {
			return true
		}
	}
	return false
}

// maskEnvJSON 把 MCP env（同时也是 http/sse 的请求头）里疑似凭据的值替换成掩码。
//
// 为什么必须做：GET /api/mcp 直接序列化 db.MCPServer，env 里就是 Authorization /
// X-API-Key 这类真凭据。对比通知渠道走的是 notify.MaskConfig——同一类数据，两套待遇。
// 一次管理会话读取（或一次 XSS）即可导出全部 MCP token。
//
// 非凭据键原样保留：UI 需要看到它们才能正常编辑。
func maskEnvJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		// 解析不了就整份遮掉，不把未知形态的内容原样回显。
		return json.RawMessage(`{}`)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if looksSecretKey(k) && v != "" {
			out[k] = maskSecret(v)
			continue
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// maskMCPForUI 返回用于 API 回显的 MCP 副本（env 中的凭据已掩码）。
func maskMCPForUI(m *db.MCPServer) *db.MCPServer {
	if m == nil {
		return nil
	}
	cp := *m
	cp.Env = maskEnvJSON(m.Env)
	return &cp
}

// maskToolExecForUI 掩码自定义工具 exec 规格里可能携带凭据的部分：
//   - http：headers 中疑似凭据的值
//   - command / script：整段代码按原样返回（它是工具逻辑本身，不是凭据容器；
//     但若里面硬编码了 token，那是配置者的选择，UI 必须能看到才能编辑）
func maskToolExecForUI(kind string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || kind != "http" {
		return raw
	}
	var spec struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil || len(spec.Headers) == 0 {
		return raw
	}
	// 只改 headers 子树，其余字段原样保留（用通用 map 重建）。
	var whole map[string]json.RawMessage
	if err := json.Unmarshal(raw, &whole); err != nil {
		return raw
	}
	masked := make(map[string]string, len(spec.Headers))
	for k, v := range spec.Headers {
		if looksSecretKey(k) && v != "" {
			masked[k] = maskSecret(v)
			continue
		}
		masked[k] = v
	}
	hb, err := json.Marshal(masked)
	if err != nil {
		return raw
	}
	whole["headers"] = hb
	out, err := json.Marshal(whole)
	if err != nil {
		return raw
	}
	return out
}

// mergeMaskedEnv 把「提交回来的掩码值」换回库里的原值。
//
// 读取接口回显 env 时把凭据换成 __masked__:…；前端编辑后整份提交回来，未改动的
// 字段仍是掩码。若不还原，一次「只改 name」的保存就会把真 token 覆盖成字面量掩码。
// 键被删除 = 用户有意删掉该键，保留删除。
func mergeMaskedEnv(prev, submitted json.RawMessage) json.RawMessage {
	if len(submitted) == 0 {
		return submitted
	}
	var next map[string]string
	if err := json.Unmarshal(submitted, &next); err != nil {
		return submitted // 形态不认识就原样交给调用方（SaveMCP 会存什么就是什么）
	}
	var old map[string]string
	_ = json.Unmarshal(prev, &old)
	changed := false
	for k, v := range next {
		if !isMaskedSecret(v) {
			continue
		}
		if orig, ok := old[k]; ok {
			next[k] = orig
			changed = true
		} else {
			delete(next, k) // 掩码却无原值：这个键本来就不存在，别把掩码当凭据存进去
			changed = true
		}
	}
	if !changed {
		return submitted
	}
	b, err := json.Marshal(next)
	if err != nil {
		return submitted
	}
	return b
}
