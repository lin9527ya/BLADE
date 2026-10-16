package intercept

import (
	"context"
	"strings"
	"testing"
)

// 事前可检出性（P38）：判定时就把"这一手预计会留下什么"算好、随审计落库——
// 事后补算没有意义（那时命令早跑完了），而人要做的决定恰恰在这一刻。
func TestAuditCarriesOpsecNotes(t *testing.T) {
	ctx := WithCall(context.Background(), "call-1", []byte(`{"command":"procdump -ma lsass.exe"}`))
	a := auditFor(ctx, Decision{Action: "ask", Message: "需要审批"}, "Bash",
		[]byte(`{"command":"procdump -ma lsass.exe"}`), "pending")
	if len(a.OpsecNotes) == 0 {
		t.Fatalf("命中知识库的命令该带可检出性笔记：%+v", a)
	}
	found := false
	for _, n := range a.OpsecNotes {
		if n.ID == "lsass-access" {
			found = true
		}
	}
	if !found {
		t.Errorf("该命中 LSASS 那一类：%+v", a.OpsecNotes)
	}
	if a.OpsecUncovered {
		t.Error("命中时不该标 uncovered")
	}
	if a.OpsecCommand == "" {
		t.Error("该记下抽出来的命令原文（复盘时要看判定针对的是哪条命令）")
	}
}

func TestAuditOpsecUncoveredIsExplicit(t *testing.T) {
	cmd := []byte(`{"command":"echo hello"}`)
	a := auditFor(context.Background(), Decision{Action: "allow"}, "Bash", cmd, "allowed")
	if !a.OpsecUncovered {
		t.Fatalf("知识库没收录时该显式标出（「没有提示」不能被读成「没有日志」）：%+v", a)
	}
	if len(a.OpsecNotes) != 0 {
		t.Errorf("没命中不该硬塞笔记：%+v", a.OpsecNotes)
	}
}

// 非动作类工具不产生对目标的动作，谈可检出性没有意义——也不该在审计里多出一段空话。
func TestAuditSkipsOpsecForNonActionTools(t *testing.T) {
	a := auditFor(context.Background(), Decision{Action: "allow"}, "Read",
		[]byte(`{"file_path":"/etc/hosts"}`), "allowed")
	if len(a.OpsecNotes) != 0 || a.OpsecUncovered || a.OpsecCommand != "" {
		t.Errorf("非动作工具不该带可检出性标注：%+v", a)
	}
	if strings.Contains(string(a.OpsecCommand), "hosts") {
		t.Error("不该把非命令输入当命令抽出来")
	}
}
