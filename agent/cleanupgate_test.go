package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 收官前必须处理留在目标上的东西：没清掉的 webshell 是留在客户系统上的后门。
func TestCleanupGateBlocksCompletion(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	goalID, err := ts.AddGoal(map[string]any{"summary": "拿下后台"}, "goals")
	if err != nil {
		t.Fatal(err)
	}
	factID, err := ts.AddNode(db.KindFact, map[string]any{"summary": "已拿到管理员会话"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ts.AddNode(db.KindArtifact, map[string]any{
		"summary": "在 web01 放了 webshell", "kind": "webshell", "host": "web01",
		"locator": "/var/www/html/x.php", "url": "http://web01/x.php", "cleanup": "rm -f /var/www/html/x.php",
	}, 0, db.StateArtifactActive, "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	prove := func() string {
		return callTool(t, tools.PlannerTools(), "prove_goal",
			fmt.Sprintf(`{"goal_id":%d,"evidence_id":%d,"reason":"证据充分"}`, goalID, factID))
	}

	out := prove()
	if !strings.Contains(out, "没销账的遗留物") || !strings.Contains(out, "accept_artifact_risk") {
		t.Fatalf("有未清理遗留物时应拒绝收官并给出两条出路：%s", out)
	}
	if !strings.Contains(out, "webshell") || !strings.Contains(out, "/var/www/html/x.php") {
		t.Fatalf("拒绝理由应点名是哪一件：%s", out)
	}

	// 显式接受风险后放行。
	acc := callTool(t, tools.PlannerTools(), "accept_artifact_risk", `{"reason":"客户要求保留 webshell 做后续排查"}`)
	if !strings.HasPrefix(acc, "risk accepted: ") {
		t.Fatalf("接受风险应成功：%s", acc)
	}
	if out := prove(); strings.Contains(out, "没销账的遗留物") {
		t.Fatalf("接受风险后不该再拦：%s", out)
	}
	// 接受记录是一条可核对的结论（会进报告语境）。
	n, err := ts.GetNode(lootNodeID(t, acc))
	if err != nil || n == nil {
		t.Fatalf("接受记录应落库：%v", err)
	}
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	if ok, _ := p["artifact_risk_accepted"].(bool); !ok {
		t.Fatalf("接受记录应带标记：%+v", p)
	}
	_ = artID
}

// 没有遗留物时不该拦；理由为空时接受风险应被拒。
func TestCleanupGateAllowsCleanTask(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	goalID, _ := ts.AddGoal(map[string]any{"summary": "拿下后台"}, "goals")
	factID, _ := ts.AddNode(db.KindFact, map[string]any{"summary": "已拿到管理员会话"}, 0, "confirmed", "worker", nil)
	out := callTool(t, tools.PlannerTools(), "prove_goal",
		fmt.Sprintf(`{"goal_id":%d,"evidence_id":%d,"reason":"证据充分"}`, goalID, factID))
	if strings.Contains(out, "没销账的遗留物") {
		t.Fatalf("没有遗留物时不该拦：%s", out)
	}
	bad := callTool(t, tools.PlannerTools(), "accept_artifact_risk", `{"reason":"  "}`)
	if !strings.Contains(bad, "reason 必填") {
		t.Fatalf("空理由应被拒：%s", bad)
	}
}
