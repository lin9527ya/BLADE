package agent

import (
	"testing"

	"github.com/Autumn-27/artex/guard"
)

// TestMainAgentAttachesGuardHooks 钉住「挂了 guard 就必须产出 hooks」。
//
// 这条接线一旦断掉，任务内的人在环路（mainagent）会**三样门全无**——拦截规则、LLM 兜底裁判、
// RoE 授权范围门——而且不会有任何报错：它同样带 Bash/WebFetch，调用照常执行。属于最该被钉住的
// 静默失效。此前就漏挂过（guard 是任务自带的，却没接到 mainagent 的 run 上）。
//
// 纯函数用例：不跑模型、不连库。（注：服务端把 t.Guard 传进来的那一行由 agentsForTask 完成，
// 本用例覆盖不到它——那需要驱动完整 run，成本不成比例。）
func TestMainAgentAttachesGuardHooks(t *testing.T) {
	m := NewMainAgent(nil, "test-model", "", nil, 0, 0)
	if h := m.guardHooks(); h != nil {
		t.Fatal("未挂 guard 时不该产出 hooks")
	}

	m.SetGuard(guard.New())
	if h := m.guardHooks(); h == nil {
		t.Fatal("挂了 guard 却没产出 hooks——任务内的人在环路会三样门（规则/裁判/RoE）全无，且无任何报错")
	}
}
