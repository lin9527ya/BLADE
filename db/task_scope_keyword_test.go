package db

import (
	"strings"
	"testing"
)

// keyword 范围没有匹配谓词：接受它只会让一条永远命中不到资产的 scope 混进范围账。
// 必须当场拒绝并给出替代做法。
func TestAddAgentScopeRejectsKeyword(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTask("keyword scope", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	if _, err := as.AddAgentScope(task.ID, "keyword", "国家电网", "范围", "agent"); err == nil || !strings.Contains(err.Error(), "keyword 范围暂不支持") {
		t.Fatalf("keyword scope 应被拒并说明替代做法：%v", err)
	}
}
