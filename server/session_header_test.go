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
