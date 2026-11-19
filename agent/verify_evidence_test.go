package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

func verifyTool(t *testing.T, tools []actool.CoreTool) actool.CoreTool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == "verify_evidence" {
			return tl
		}
	}
	t.Fatal("verify_evidence tool missing")
	return nil
}

// deep 复核必须接调用方 ctx 和短超时，不能再用 Background 绕过暂停/删除/单轮 deadline。
func TestVerifyEvidenceDeepUsesCallerContext(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	fact, err := ts.AddNode(db.KindFact, map[string]any{
		"summary": "证据事实", "evidence": "uid=0(root)", "confidence": "observed",
	}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	tools.SetLLMVerifier(func(ctx context.Context, _ string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("deep verifier 未收到超时 deadline")
		}
		if err := ctx.Err(); err != nil {
			t.Errorf("deep verifier ctx 在调用时应仍有效: %v", err)
		}
		return `{"verdict":"supported","reason":"fake-review"}`, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := verifyTool(t, tools.PlannerTools())
	res, err := tool.Call(ctx, json.RawMessage(`{"fact_id":`+jsonInt(fact)+`,"deep":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "fake-review") {
		t.Fatalf("deep review missing: %s", res.Flatten())
	}
}

// 调用方取消后，复核拿到的 ctx 也必须是取消态，不能继续烧 provider 调用。
func TestVerifyEvidenceDeepHonorsCanceledContext(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	fact, err := ts.AddNode(db.KindFact, map[string]any{
		"summary": "证据事实", "evidence": "uid=0(root)", "confidence": "observed",
	}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	tools.SetLLMVerifier(func(ctx context.Context, _ string) (string, error) {
		if ctx.Err() != context.Canceled {
			t.Errorf("want canceled ctx, got %v", ctx.Err())
		}
		return "canceled-review", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 给工具一个极短的真实 deadline 之外还要保留取消态；WithTimeout 会继承它。
	tool := verifyTool(t, tools.PlannerTools())
	if _, err := tool.Call(ctx, json.RawMessage(`{"fact_id":`+jsonInt(fact)+`,"deep":true}`), nil); err != nil {
		t.Fatal(err)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
