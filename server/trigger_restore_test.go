package server

import (
	"testing"

	"github.com/lin9527ya/blade/agent"
)

// 规划失败时触发事件要放回队列：它们是"哪个 work 完成了"的唯一线索，丢了下一轮
// planner 只看到图变了，不知道是什么变了。
func TestRestoreTriggersPutsEventsBack(t *testing.T) {
	var tsk Task
	tsk.restoreTriggers([]agent.TriggerEvent{{Kind: "done", IntentID: 7, Summary: "探完了"}})
	got := tsk.drainTriggers()
	if len(got) != 1 || got[0].IntentID != 7 || got[0].Kind != "done" {
		t.Fatalf("触发事件应原样放回：%+v", got)
	}
	// 已有事件时放回的是"更早的那批"，顺序不能反（drain 按先来后到给 planner）。
	tsk.restoreTriggers([]agent.TriggerEvent{{Kind: "done", IntentID: 8}})
	tsk.restoreTriggers([]agent.TriggerEvent{{Kind: "finding", IntentID: 9}})
	got = tsk.drainTriggers()
	if len(got) != 2 || got[0].IntentID != 9 || got[1].IntentID != 8 {
		t.Fatalf("放回的事件应排在现有事件之前：%+v", got)
	}
	if rest := tsk.drainTriggers(); len(rest) != 0 {
		t.Fatalf("drain 之后应为空：%+v", rest)
	}
}
