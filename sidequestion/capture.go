// Package sidequestion captures immutable main-agent checkpoints. It never owns
// an agent session or a tool executor.
package sidequestion

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
)

type Parent struct {
	ConversationID int64 `json:"conversation_id,omitempty"`
	TaskID         int64 `json:"task_id,omitempty"`
	ExplorationID  int64 `json:"exploration_id,omitempty"`
	IntentID       int64 `json:"intent_id,omitempty"`
}

func (p Parent) Key() string {
	if p.ConversationID > 0 {
		return fmt.Sprintf("conv-%d", p.ConversationID)
	}
	if p.IntentID > 0 {
		return fmt.Sprintf("task-%d-exp-%d-worker-i%d", p.TaskID, p.ExplorationID, p.IntentID)
	}
	return fmt.Sprintf("task-%d-exp-%d-main", p.TaskID, p.ExplorationID)
}

// Model contains only configuration identity, never credentials or proxy URLs.
type Model struct {
	ProfileID    int64  `json:"profile_id"`
	Name         string `json:"name"`
	Format       string `json:"format"`
	Model        string `json:"model"`
	Identity     string `json:"identity"`
	Streaming    bool   `json:"streaming"`
	WindowTokens int    `json:"window_tokens"`
}

type Snapshot struct {
	Parent     Parent                `json:"parent"`
	RunID      int64                 `json:"run_id"`
	Version    int64                 `json:"version"`
	CapturedAt time.Time             `json:"captured_at"`
	Model      Model                 `json:"model"`
	Request    llm.CompletionRequest `json:"request"`
}

// CloneRequest 返回请求的深拷贝，保证旁路快照与主会话的可变结构完全隔离。
//
// 此前用 JSON 编解码实现：每次主模型请求（工具循环里每一轮）都要把整段会话
// 序列化再反序列化一次，上下文越大开销越高。改为结构化拷贝——字符串在 Go 里
// 不可变、可以安全共享；切片/map/字节逐层复制，隔离语义与 JSON 往返一致。
func CloneRequest(req llm.CompletionRequest) (llm.CompletionRequest, error) {
	out := req
	out.System = append([]string(nil), req.System...)
	out.Stop = append([]string(nil), req.Stop...)
	if req.Temperature != nil {
		t := *req.Temperature
		out.Temperature = &t
	}
	out.Messages = cloneMessages(req.Messages)
	if req.Tools != nil {
		out.Tools = make([]llm.ToolSchema, len(req.Tools))
		for i, tool := range req.Tools {
			out.Tools[i] = tool
			out.Tools[i].InputSchema = cloneJSONMap(tool.InputSchema)
		}
	}
	return out, nil
}

func cloneMessages(in []llm.Message) []llm.Message {
	if in == nil {
		return nil
	}
	out := make([]llm.Message, len(in))
	for i, msg := range in {
		out[i] = msg
		out[i].Content = cloneBlocks(msg.Content)
	}
	return out
}

func cloneBlocks(in []llm.ContentBlock) []llm.ContentBlock {
	if in == nil {
		return nil
	}
	out := make([]llm.ContentBlock, len(in))
	for i, block := range in {
		out[i] = block
		if block.Input != nil {
			out[i].Input = append(json.RawMessage(nil), block.Input...)
		}
		if block.Content != nil {
			out[i].Content = cloneBlocks(block.Content)
		}
	}
	return out
}

// cloneJSONMap 深拷贝工具 schema 的 JSON 结构（json.Unmarshal 的取值类型：
// nil/bool/float64/string/[]any/map[string]any/json.Number）。
func cloneJSONMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneJSONValue(v)
	}
	return out
}

func cloneJSONValue(v any) any {
	switch x := v.(type) {
	case nil, bool, float64, string, json.Number:
		return x
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneJSONValue(item)
		}
		return out
	case map[string]any:
		return cloneJSONMap(x)
	default:
		// 自定义类型的工具 schema 不是 JSON 往返约定的一部分；保守地原样保留，
		// 与 JSON 版的行为差异只在非 JSON 类型上（实际不会出现）。
		return x
	}
}

type Publisher func(Snapshot)
type publisherKey struct{}
type captureKey struct{}
type attemptKey struct{}

func WithPublisher(ctx context.Context, publish Publisher) context.Context {
	return context.WithValue(ctx, publisherKey{}, publish)
}

type Capture struct {
	mu      sync.Mutex
	parent  Parent
	runID   int64
	version int64
	publish Publisher
	last    *Snapshot
}

// Attach uses QueryDeps rather than a global provider hook so compaction and
// other auxiliary completions cannot replace the main conversation checkpoint.
func Attach(ctx context.Context, parent Parent, deps harness.QueryDeps, provider llm.Provider) (context.Context, harness.QueryDeps) {
	publish, _ := ctx.Value(publisherKey{}).(Publisher)
	if publish == nil {
		return ctx, deps
	}
	c := &Capture{parent: parent, runID: time.Now().UnixNano(), publish: publish}
	stream, complete := deps.CallModel, deps.CallModelSync
	if stream == nil {
		stream = provider.Stream
	}
	if complete == nil {
		complete = provider.Complete
	}
	deps.CallModel = func(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return stream(context.WithValue(ctx, attemptKey{}, c), req)
	}
	deps.CallModelSync = func(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		return complete(context.WithValue(ctx, attemptKey{}, c), req)
	}
	return context.WithValue(ctx, captureKey{}, c), deps
}

func (c *Capture) save(req llm.CompletionRequest, model Model) {
	copy, err := CloneRequest(req)
	if err != nil {
		return
	} // Observability must not break the main run.
	c.mu.Lock()
	c.version++
	s := Snapshot{Parent: c.parent, RunID: c.runID, Version: c.version, CapturedAt: time.Now().UTC(), Model: model, Request: copy}
	c.last = &s
	c.mu.Unlock()
	c.publish(s)
}

// Finish adds tool results that landed after the final model request. Norma's
// first API message is its host reminder, absent from Terminal.Messages.
func Finish(ctx context.Context, messages []llm.Message) {
	c, _ := ctx.Value(captureKey{}).(*Capture)
	if c == nil {
		return
	}
	c.mu.Lock()
	s := c.last
	c.mu.Unlock()
	if s == nil {
		return
	}
	req := s.Request
	var prefix []llm.Message
	if len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Text(), "<system-reminder>") {
		prefix = append(prefix, req.Messages[0])
	}
	req.Messages = append(prefix, llm.MessagesForAPI(messages)...)
	c.save(req, s.Model)
}

type boundProvider struct {
	inner llm.Provider
	model Model
}

// Bind belongs immediately around a concrete provider, inside any routing pool.
func Bind(inner llm.Provider, model Model) llm.Provider { return &boundProvider{inner, model} }

func (p *boundProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		c, _ := ctx.Value(attemptKey{}).(*Capture)
		if c == nil {
			for ev, err := range p.inner.Stream(ctx, req) {
				if !yield(ev, err) {
					return
				}
			}
			return
		}
		c.save(req, p.model)
		acc := llm.NewAccumulator()
		failed := false
		complete := false
		for ev, err := range p.inner.Stream(ctx, req) {
			if err != nil {
				failed = true
			} else {
				acc.Add(ev)
				if ev.Type == llm.SEMessageStop {
					complete = true
				}
			}
			if !yield(ev, err) {
				return
			}
		}
		if !failed && complete && ctx.Err() == nil {
			msg := acc.Message()
			req.Messages = llm.MessagesForAPI(append(append([]llm.Message{}, req.Messages...), msg))
			c.save(req, p.model)
		}
	}
}

func (p *boundProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	c, _ := ctx.Value(attemptKey{}).(*Capture)
	if c != nil {
		c.save(req, p.model)
	}
	msg, stop, usage, err := p.inner.Complete(ctx, req)
	if c != nil && err == nil && ctx.Err() == nil {
		req.Messages = llm.MessagesForAPI(append(append([]llm.Message{}, req.Messages...), msg))
		c.save(req, p.model)
	}
	return msg, stop, usage, err
}
