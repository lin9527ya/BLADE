package server

import (
	"context"
	"iter"
	"testing"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
)

// sessionCaptureProvider 记录调用方 context 上的会话 id（provider 的发头逻辑读它）。
type sessionCaptureProvider struct{ seen string }

func (p *sessionCaptureProvider) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.seen = transcript.SessionIDFrom(ctx)
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "ok"}, nil)
	}
}

func (p *sessionCaptureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

// 单发 LLM 调用（CVE 报告导入、拦截评审）必须带 session id：配置了
// SessionHeaderKey 的端点（opencode zen 的 x-opencode-session）缺头直接 400。
func TestOneShotCompletionAttachesSessionID(t *testing.T) {
	p := &sessionCaptureProvider{}
	if _, err := streamCollectTextLimit(context.Background(), p, "s", "u", 16); err != nil {
		t.Fatal(err)
	}
	if p.seen == "" {
		t.Fatal("one-shot completion must attach a session id for SessionHeaderKey")
	}

	// 已有会话 id（从任务/对话上下文进来）不能被覆盖。
	ctx := transcript.WithSessionID(context.Background(), "conv-42")
	p = &sessionCaptureProvider{}
	if _, err := streamCollectTextLimit(ctx, p, "s", "u", 16); err != nil {
		t.Fatal(err)
	}
	if p.seen != "conv-42" {
		t.Fatalf("existing session id overwritten: %q", p.seen)
	}
}

type oneshotCaptureProvider struct {
	reqs []llm.CompletionRequest
	sids []string
}

func (p *oneshotCaptureProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.reqs = append(p.reqs, req)
	p.sids = append(p.sids, transcript.SessionIDFrom(ctx))
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "ok"}, nil)
	}
}

func (p *oneshotCaptureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

func TestOneShotDeterminismAndDynamicBoundary(t *testing.T) {
	p := &oneshotCaptureProvider{}
	if _, err := streamCollectTextLimit(context.Background(), p, "judge-sys-v1", "u1", 16); err != nil {
		t.Fatal(err)
	}
	if _, err := streamCollectTextLimit(context.Background(), p, "judge-sys-v1", "u2", 16); err != nil {
		t.Fatal(err)
	}
	if _, err := streamCollectTextLimit(context.Background(), p, "other-sys-v2", "u3", 16); err != nil {
		t.Fatal(err)
	}
	if len(p.sids) != 3 || p.sids[0] == "" || p.sids[0] != p.sids[1] {
		t.Fatalf("identical system prompt must share deterministic oneshot session id: %v", p.sids)
	}
	if p.sids[2] == p.sids[0] {
		t.Fatalf("distinct system prompt should not collide: %v", p.sids)
	}
	for i, req := range p.reqs {
		if req.DynamicBoundary != 1 || req.PromptCacheKey != p.sids[i] {
			t.Fatalf("req[%d]: DynamicBoundary=%d PromptCacheKey=%q, want 1 and %q", i, req.DynamicBoundary, req.PromptCacheKey, p.sids[i])
		}
	}
}
