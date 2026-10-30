package agent

import (
	"strings"
	"testing"
)

// 触发器必须把「终态」和「退回续跑」讲清楚：no_output 与 done 对下一轮的含义完全
// 相反；requeued 更不是完成——旧版对执行观察者发 done，planner 会去取一个并不存在
// 的输出结论。
func TestRenderTriggersCarriesTerminalState(t *testing.T) {
	_, ts, intent := newToolWorker(t)

	got := renderTriggers(ts, []TriggerEvent{{Kind: "done", IntentID: intent, State: "no_output"}})
	if !strings.Contains(got, "no_output") || !strings.Contains(got, "没有写回任何产出") {
		t.Fatalf("no_output 应明确提示不要当成已覆盖：%s", got)
	}
	if strings.Contains(got, "输出结论") {
		t.Fatalf("no_output 不应让 planner 去取输出结论：%s", got)
	}

	got = renderTriggers(ts, []TriggerEvent{{Kind: "done", IntentID: intent, State: "negative"}})
	if !strings.Contains(got, "negative") || !strings.Contains(got, "否定结论") {
		t.Fatalf("negative 应带终态标签：%s", got)
	}

	got = renderTriggers(ts, []TriggerEvent{{Kind: "requeued", IntentID: intent, Detail: "租约到期"}})
	if !strings.Contains(got, "退回 open") || !strings.Contains(got, "租约到期") {
		t.Fatalf("requeued 应说明退回续跑及原因：%s", got)
	}
	if strings.Contains(got, "worker 结束") {
		t.Fatalf("requeued 不能被写成已结束：%s", got)
	}

	got = renderTriggers(ts, []TriggerEvent{{Kind: "done", IntentID: intent, State: "stopped"}})
	if !strings.Contains(got, "被终止") {
		t.Fatalf("stopped 应带中文标签：%s", got)
	}
}
