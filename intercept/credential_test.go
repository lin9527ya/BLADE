package intercept

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/opsec"
)

// 预算判定的端到端验证在 server/acceptance_test.go（需要真库计数）。
// 这里只钉两件不依赖库的事：无库时不拦、以及"没抽出账号"要如实说。

func TestCredentialBudgetNilDBDoesNotGate(t *testing.T) {
	i := &Interceptor{} // db 为 nil：不能因此拦人，也不能因此 panic
	if _, hit := i.CheckCredentialBudget(context.TODO(), 1, "Bash",
		json.RawMessage(`{"command":"nxc smb 10.0.0.5 -u a -p b"}`)); hit {
		t.Fatal("无库时不该产生裁决（护栏不可用不等于动作被禁）")
	}
	if got := i.CredentialBudget(); got != defaultCredentialBudget {
		t.Fatalf("无库时预算应退回默认 %d，得到 %d", defaultCredentialBudget, got)
	}
}

func TestDescribeCredTargetSaysWhenAccountUnknown(t *testing.T) {
	// 抽不出账号时必须**明说抽不出来**：读审批卡的人要靠这句话判断平台看清了多少。
	// 含糊成"某账号"会让人以为平台认准了目标账号，那正是这个护栏最不该有的误导。
	got := describeCredTarget(opsec.Attempt{Host: "10.0.0.5", Tool: "redis-cli"})
	if !strings.Contains(got, "10.0.0.5") || !strings.Contains(got, "未抽出账号") {
		t.Fatalf("账号未知时的描述不对：%q", got)
	}
	got = describeCredTarget(opsec.Attempt{Host: "10.0.0.5", Account: "svc_backup"})
	if !strings.Contains(got, "svc_backup") || strings.Contains(got, "未抽出") {
		t.Fatalf("账号已知时的描述不对：%q", got)
	}
}
