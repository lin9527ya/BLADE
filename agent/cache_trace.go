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
	Model       string      `json:"model"`
	System      cachePart   `json:"system"`
	Tools       cachePart   `json:"tools"`
	Messages    []cachePart `json:"messages"`
	Breakpoints []string    `json:"breakpoints"`
}

func cachePartOf(raw json.RawMessage) cachePart {
	// 保留数组顺序；编码 JSON 去掉无意义的对象键顺序/空白差异。
	var value any
	if len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
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
		Input        []json.RawMessage `json:"input"`
	}
	var out cacheSignature
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return out, err
	}

	if req.System == nil {
		if req.Instructions != nil {
			req.System = req.Instructions
			req.Messages = req.Input
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
	last map[string]cacheSignature
}{last: map[string]cacheSignature{}}

var cacheTraceSequence atomic.Uint64

func traceModelRequest(req *http.Request) uint64 {
	if !CacheTraceState().Enabled {
		return 0
	}
	sig, err := requestCacheSignature(requestBodySnapshot(req))
	if err != nil {
		return 0
	}
	sid := transcript.SessionIDFrom(req.Context())
	endpoint := sha256.Sum256([]byte(req.URL.String() + "|" + sid + "|" + sig.Model))
	key := hex.EncodeToString(endpoint[:])
	cacheTraceHistory.Lock()
	previous, seen := cacheTraceHistory.last[key]
	if !seen && len(cacheTraceHistory.last) >= 64 {
		cacheTraceHistory.last = map[string]cacheSignature{}
	}
	cacheTraceHistory.last[key] = sig
	cacheTraceHistory.Unlock()
	shared := 0
	if seen {
		for shared < len(sig.Messages) && shared < len(previous.Messages) && sig.Messages[shared] == previous.Messages[shared] {
			shared++
		}
	}
	traceID := cacheTraceSequence.Add(1)
	addCacheDiagnostic(CacheDiagnostic{
		ID: traceID, At: time.Now().UTC(), Session: sid, Model: sig.Model, Previous: seen,
		SystemSame: seen && previous.System == sig.System, ToolsSame: seen && previous.Tools == sig.Tools,
		SharedMessages: shared, Messages: len(sig.Messages), PreviousMessages: len(previous.Messages),
		System: sig.System, Tools: sig.Tools, Breakpoints: append([]string{}, sig.Breakpoints...), Frames: []CacheUsageFrame{},
	})
	encoded, _ := json.Marshal(sig)
	log.Printf("[cache-trace] trace_id=%d session=%q previous=%t system_same=%t tools_same=%t shared_messages=%d structure=%s",
		traceID, sid, seen, seen && previous.System == sig.System, seen && previous.Tools == sig.Tools, shared, encoded)
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
