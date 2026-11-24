package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lin9527ya/Norma/transcript"
)

// CacheTraceEnv 默认关闭。开启时只记录请求结构哈希/长度和原始 usage 数值：
// 不输出密钥、目标内容、提示词或响应文本，不改变发给模型的请求。
const CacheTraceEnv = "BLADE_LLM_CACHE_DEBUG"
const cacheTraceLineLimit = 256 << 10

type cachePart struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type cacheSignature struct {
	Model        string      `json:"model"`
	SystemStatic cachePart   `json:"system_static,omitempty"`
	System       cachePart   `json:"system"`
	Tools        cachePart   `json:"tools"`
	Messages     []cachePart `json:"messages"`
	Breakpoints  []string    `json:"breakpoints"`
}

func cachePartOf(raw json.RawMessage) cachePart {
	// 保留数组顺序；编码 JSON 去掉无意义的对象键顺序/空白差异。
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if len(raw) > 0 && decoder.Decode(&value) == nil {
		raw, _ = json.Marshal(value)
	}
	sum := sha256.Sum256(raw)
	return cachePart{Bytes: len(raw), SHA256: hex.EncodeToString(sum[:])}
}

func requestCacheSignature(raw string) (cacheSignature, error) {
	var req struct {
		Model        string            `json:"model"`
		System       json.RawMessage   `json:"system"`
		Tools        json.RawMessage   `json:"tools"`
		Messages     []json.RawMessage `json:"messages"`
		Instructions json.RawMessage   `json:"instructions"`
		Input        json.RawMessage   `json:"input"`
	}
	var out cacheSignature
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return out, err
	}

	if req.System == nil {
		if req.Instructions != nil {
			req.System = req.Instructions
			if len(req.Input) > 0 {
				if err := json.Unmarshal(req.Input, &req.Messages); err != nil {
					req.Messages = []json.RawMessage{req.Input}
				}
			}
		} else {
			var systems, messages []json.RawMessage
			for _, raw := range req.Messages {
				var msg struct {
					Role string `json:"role"`
				}
				_ = json.Unmarshal(raw, &msg)
				if msg.Role == "system" || msg.Role == "developer" {
					systems = append(systems, raw)
				} else {
					messages = append(messages, raw)
				}
			}
			if len(systems) > 0 {
				req.System, _ = json.Marshal(systems)
				req.Messages = messages
			}
		}
	}
	out.Model = req.Model
	out.System = cachePartOf(req.System)
	out.SystemStatic = systemStaticPartOf(req.System, out.System)
	out.Tools = cachePartOf(req.Tools)
	for _, msg := range req.Messages {
		out.Messages = append(out.Messages, cachePartOf(msg))
	}
	// 断点只报标准结构位置，不遍历自定义工具入参或输出原文。
	for _, section := range []struct {
		name string
		raw  json.RawMessage
	}{{"system", req.System}, {"tools", req.Tools}} {
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(section.raw, &blocks) == nil {
			for i, b := range blocks {
				if _, ok := b["cache_control"]; ok {
					out.Breakpoints = append(out.Breakpoints, section.name+"["+strconv.Itoa(i)+"]")
				}
			}
		}
	}
	for i, raw := range req.Messages {
		var msg struct {
			Content []map[string]json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &msg) == nil {
			for j, b := range msg.Content {
				if _, ok := b["cache_control"]; ok {
					out.Breakpoints = append(out.Breakpoints, "messages["+strconv.Itoa(i)+"].content["+strconv.Itoa(j)+"]")
				}
			}
		}
	}
	return out, nil
}

// 有界保存上一份结构哈希。原文从不留在此 map 中，诊断不积累会话内容。
var cacheTraceHistory = struct {
	sync.Mutex
	last       map[string]cacheSignature
	lastByRole map[string]cacheSignature
}{last: map[string]cacheSignature{}, lastByRole: map[string]cacheSignature{}}

var cacheTraceSequence atomic.Uint64

func systemStaticPartOf(rawSystem json.RawMessage, full cachePart) cachePart {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(rawSystem, &blocks) != nil || len(blocks) == 0 {
		return full
	}
	lastCached := -1
	for i, b := range blocks {
		if _, ok := b["cache_control"]; ok {
			lastCached = i
		}
	}
	if lastCached < 0 {
		return full
	}
	prefixRaw, err := json.Marshal(blocks[:lastCached+1])
	if err != nil {
		return full
	}
	return cachePartOf(prefixRaw)
}

func sessionRole(sid string) string {
	if !strings.HasPrefix(sid, "exp") {
		return ""
	}
	rest := sid[3:]
	i := strings.IndexByte(rest, '-')
	if i < 0 {
		return ""
	}
	rest = rest[i+1:]
	if j := strings.IndexByte(rest, '-'); j >= 0 {
		return rest[:j]
	}
	return rest
}

func classifyCacheMiss(seen bool, prev cacheSignature, roleSeen bool, rolePrev cacheSignature, sig cacheSignature, shared int) string {
	if seen {
		if prev.Tools != sig.Tools {
			return "tools_changed"
		}
		if prev.SystemStatic != sig.SystemStatic {
			return "system_static_changed"
		}
		if prev.System != sig.System {
			return "system_dynamic_changed"
		}
		if shared == 0 {
			return "history_rewritten"
		}
		return "prefix_hit"
	}
	if !roleSeen {
		return "cold_role"
	}
	if rolePrev.Tools != sig.Tools {
		return "tools_changed"
	}
	if rolePrev.SystemStatic != sig.SystemStatic {
		return "system_static_changed"
	}
	if len(sig.Messages) > 0 && len(rolePrev.Messages) > 0 && sig.Messages[0] == rolePrev.Messages[0] {
		return "cross_session_prefix_hit"
	}
	return "cross_session_turn1_diverged"
}

func traceModelRequest(req *http.Request) uint64 {
	if !CacheTraceState().Enabled {
		return 0
	}
	sig, err := requestCacheSignature(requestBodySnapshot(req))
	if err != nil {
		return 0
	}
	sid := transcript.SessionIDFrom(req.Context())
	role := sessionRole(sid)
	endpoint := sha256.Sum256([]byte(req.URL.String() + "|" + sid + "|" + sig.Model))
	key := hex.EncodeToString(endpoint[:])
	roleDigest := sha256.Sum256([]byte(req.URL.String() + "|role:" + role + "|" + sig.Model))
	roleKey := hex.EncodeToString(roleDigest[:])
	cacheTraceHistory.Lock()
	previous, seen := cacheTraceHistory.last[key]
	if !seen && len(cacheTraceHistory.last) >= 64 {
		cacheTraceHistory.last = map[string]cacheSignature{}
	}
	cacheTraceHistory.last[key] = sig
	var rolePrevious cacheSignature
	var roleSeen bool
	if role != "" {
		rolePrevious, roleSeen = cacheTraceHistory.lastByRole[roleKey]
		if !roleSeen && len(cacheTraceHistory.lastByRole) >= 64 {
			cacheTraceHistory.lastByRole = map[string]cacheSignature{}
		}
		cacheTraceHistory.lastByRole[roleKey] = sig
	}
	cacheTraceHistory.Unlock()
	shared := 0
	if seen {
		for shared < len(sig.Messages) && shared < len(previous.Messages) && sig.Messages[shared] == previous.Messages[shared] {
			shared++
		}
	}
	missCause := classifyCacheMiss(seen, previous, roleSeen, rolePrevious, sig, shared)
	rolePrefixSame := roleSeen && rolePrevious.Tools == sig.Tools && rolePrevious.SystemStatic == sig.SystemStatic
	traceID := cacheTraceSequence.Add(1)
	addCacheDiagnostic(CacheDiagnostic{
		ID: traceID, At: time.Now().UTC(), Session: sid, Role: role, Model: sig.Model, Previous: seen,
		SystemStaticSame: seen && previous.SystemStatic == sig.SystemStatic,
		SystemSame:       seen && previous.System == sig.System, ToolsSame: seen && previous.Tools == sig.Tools,
		RolePrefixSame: rolePrefixSame, MissCause: missCause,
		SharedMessages: shared, Messages: len(sig.Messages), PreviousMessages: len(previous.Messages),
		SystemStatic: sig.SystemStatic, System: sig.System, Tools: sig.Tools,
		Breakpoints: append([]string{}, sig.Breakpoints...), Frames: []CacheUsageFrame{},
	})
	encoded, _ := json.Marshal(sig)
	log.Printf("[cache-trace] trace_id=%d session=%q role=%q previous=%t system_static_same=%t system_same=%t tools_same=%t role_prefix_same=%t miss_cause=%s shared_messages=%d structure=%s",
		traceID, sid, role, seen, seen && previous.SystemStatic == sig.SystemStatic, seen && previous.System == sig.System, seen && previous.Tools == sig.Tools, rolePrefixSame, missCause, shared, encoded)
	return traceID
}

// traceUsageFields 只选择白名单数值（含协议原始字段），不输出 message/content/error。
func traceUsageFields(raw []byte) map[string]any {
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil {
		return nil
	}
	usage := frame["usage"]
	if usage == nil {
		for _, key := range []string{"message", "response"} {
			var nested map[string]json.RawMessage
			_ = json.Unmarshal(frame[key], &nested)
			if nested["usage"] != nil {
				usage = nested["usage"]
				break
			}
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(usage, &fields) != nil {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "prompt_tokens", "completion_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens"} {
		var n json.Number
		if value, ok := fields[key]; ok && json.Unmarshal(value, &n) == nil {
			if _, err := n.Float64(); err == nil {
				out[key] = n
			}
		}
	}
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		var detail struct {
			CachedTokens *int64 `json:"cached_tokens"`
		}
		if json.Unmarshal(fields[key], &detail) == nil && detail.CachedTokens != nil {
			out[key+".cached_tokens"] = *detail.CachedTokens
		}
	}
	// 部分网关把实际用量同时放在扩展字段；记录数值差异，不把它们与顶层重复相加。
	var gateway struct {
		Tokens map[string]json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(fields["opencode"], &gateway) == nil {
		for _, key := range []string{"input", "output", "reasoning", "cache_read", "cache_write_5m", "cache_write_1h"} {
			var n json.Number
			if value, ok := gateway.Tokens[key]; ok && json.Unmarshal(value, &n) == nil {
				if _, err := n.Float64(); err == nil {
					out["opencode.tokens."+key] = n
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type cacheTraceBody struct {
	traceID uint64
	io.ReadCloser
	session      string
	jsonResponse bool
	buffer       []byte
	skipping     bool
	finished     bool
}

func (b *cacheTraceBody) emit(raw []byte) {
	fields := traceUsageFields(raw)
	if len(fields) == 0 {
		return
	}
	encoded, _ := json.Marshal(fields)
	var frame struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &frame)
	event := "usage"
	switch frame.Type {
	case "message_start", "message_delta", "message", "response.completed", "response.incomplete":
		event = frame.Type
	}
	log.Printf("[cache-trace] trace_id=%d session=%q event=%s raw_usage=%s", b.traceID, b.session, event, encoded)
	updateCacheDiagnostic(b.traceID, func(row *CacheDiagnostic) {
		row.ObservedFrames++
		frame := CacheUsageFrame{Event: event, Values: fields}
		if len(row.Frames) >= 16 {
			copy(row.Frames[1:], row.Frames[2:])
			row.Frames = row.Frames[:15]
		}
		row.Frames = append(row.Frames, frame)
	})
}

func (b *cacheTraceBody) observe(chunk []byte) {
	if b.jsonResponse {
		if b.skipping {
			return
		}
		if len(b.buffer)+len(chunk) > cacheTraceLineLimit {
			b.skipping = true
			updateCacheDiagnostic(b.traceID, func(row *CacheDiagnostic) { row.Truncated = true })
			b.buffer = nil
			return
		}
		b.buffer = append(b.buffer, chunk...)
		return
	}
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		part := chunk
		if i >= 0 {
			part = chunk[:i]
		}
		if !b.skipping {
			if len(b.buffer)+len(part) > cacheTraceLineLimit {
				b.buffer = nil
				b.skipping = true
				updateCacheDiagnostic(b.traceID, func(row *CacheDiagnostic) { row.Truncated = true })
			} else {
				b.buffer = append(b.buffer, part...)
			}
		}
		if i < 0 {
			return
		}
		if !b.skipping {
			line := bytes.TrimSpace(b.buffer)
			if bytes.HasPrefix(line, []byte("data:")) {
				b.emit(bytes.TrimSpace(line[5:]))
			}
		}
		b.buffer = nil
		b.skipping = false
		chunk = chunk[i+1:]
	}
}

func (b *cacheTraceBody) finish() {
	if b.finished {
		return
	}
	b.finished = true
	if !b.skipping {
		if b.jsonResponse {
			b.emit(b.buffer)
		} else {
			line := bytes.TrimSpace(b.buffer)
			if bytes.HasPrefix(line, []byte("data:")) {
				b.emit(bytes.TrimSpace(line[5:]))
			}
		}
	}
	b.buffer = nil
	updateCacheDiagnostic(b.traceID, func(row *CacheDiagnostic) { row.Complete = true; row.Truncated = row.Truncated || b.skipping })
}
func (b *cacheTraceBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.observe(p[:n])
	if err == io.EOF {
		b.finish()
	}
	return n, err
}
func (b *cacheTraceBody) Close() error { b.finish(); return b.ReadCloser.Close() }

func wrapCacheTraceResponse(req *http.Request, resp *http.Response, traceID uint64) {
	updateCacheDiagnostic(traceID, func(row *CacheDiagnostic) {
		if resp != nil {
			row.Status = resp.StatusCode
		}
	})
	if resp == nil || resp.Body == nil {
		return
	}
	resp.Body = &cacheTraceBody{ReadCloser: resp.Body, traceID: traceID, session: transcript.SessionIDFrom(req.Context()),
		jsonResponse: strings.Contains(resp.Header.Get("Content-Type"), "application/json")}
}
