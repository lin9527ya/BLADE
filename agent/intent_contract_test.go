package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestAddIntentPersistsStructuredContractAndDependency(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("intent-contract-%d", time.Now().UnixNano()), "goal", db.TaskCreateOptions{AssetScopePolicy: db.AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "test")
	tools.SetTaskID(task.ID)

	preOut := callTool(t, tools.PlannerTools(), "add_intent", `{"summary":"先拿凭据"}`)
	var preID int64
	if _, err := fmt.Sscanf(preOut, "intent created: %d", &preID); err != nil || preID <= 0 {
		t.Fatalf("parse pre intent id from %q: %v", preOut, err)
	}
	out := callTool(t, tools.PlannerTools(), "add_intent", fmt.Sprintf(`{
		"summary":"用凭据横向",
		"hypothesis":"如果凭据有效则能登录 3389",
		"success_criteria":"拿到交互会话",
		"mechanism":"rdp-cred",
		"route":"cred->rdp",
		"produces":["foothold"],
		"acceptance":"可执行 whoami",
		"risk_area":"auth",
		"depends_on":[%d]
	}`, preID))
	var id int64
	if _, err := fmt.Sscanf(out, "intent created: %d", &id); err != nil || id <= 0 {
		t.Fatalf("parse intent id from %q: %v", out, err)
	}
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		t.Fatalf("get intent: %v %v", n, err)
	}
	var p map[string]any
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["hypothesis"] != "如果凭据有效则能登录 3389" || p["success_criteria"] != "拿到交互会话" {
		t.Fatalf("structured fields missing: %+v", p)
	}
	c, _ := p["contract"].(map[string]any)
	if c["mechanism"] != "rdp-cred" || c["route"] != "cred->rdp" {
		t.Fatalf("contract missing: %+v", p["contract"])
	}
	deps, _ := p["depends_on"].([]any)
	if len(deps) != 1 || int64(deps[0].(float64)) != preID {
		t.Fatalf("depends_on missing: %+v", p["depends_on"])
	}
}
