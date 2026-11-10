package agent

import (
	"github.com/lin9527ya/blade/db"
)

// 通路台账（agent 侧）：每轮只算一次，供态势与 chain_detail 工具共用。

func (t *ToolSet) chainReport() *db.ChainReport {
	if t.ts == nil || t.taskID <= 0 {
		return nil
	}
	if t.chainsLoaded {
		return t.chainsCached
	}
	t.chainsLoaded = true
	if rep, err := t.ts.ChainLedger(); err == nil {
		t.chainsCached = rep
	}
	return t.chainsCached
}
