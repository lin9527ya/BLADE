package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 拿到一台机器，域内图也要知道：对应的 AD 计算机对象标成 owned，
// 攻击路径的起点才会是"我们打下来的"而不是只认用户手标的。
func TestFootholdMarksADComputerOwned(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	web01, err := ts.AddNode(db.KindADObject, map[string]any{"name": "WEB01.CORP.LOCAL", "category": "computer"}, 0, "active", "ad_import", nil)
	if err != nil {
		t.Fatal(err)
	}
	web02, err := ts.AddNode(db.KindADObject, map[string]any{"name": "WEB02.CORP.LOCAL", "category": "computer"}, 0, "active", "ad_import", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 同名的用户对象不该被标（口径限定 computer）。
	user, err := ts.AddNode(db.KindADObject, map[string]any{"name": "WEB01", "category": "user"}, 0, "active", "ad_import", nil)
	if err != nil {
		t.Fatal(err)
	}

	credID := lootNodeID(t, callTool(t, tools.WorkerTools(), "report_credential",
		`{"summary":"web01 的口令","kind":"password","username":"app","secret":"x",`+
			`"evidence":"cat","confidence":"high","targets":[{"host":"web01","port":22,"service":"ssh"}]}`))
	credJSON, _ := json.Marshal(credID)
	out := callTool(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"web01 上的 root","evidence":"id","confidence":"high","protocol":"ssh","host":"web01",`+
			`"credential_id":`+string(credJSON)+`}`)
	if !strings.Contains(out, "foothold recorded:") || !strings.Contains(out, "AD 计算机对象标为 owned") {
		t.Fatalf("应提示已标记 AD 对象：%s", out)
	}

	owned := func(id int64) bool {
		n, err := ts.GetNode(id)
		if err != nil || n == nil {
			t.Fatalf("读节点 %d: %v", id, err)
		}
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		b, _ := p["owned"].(bool)
		return b
	}
	if !owned(web01) {
		t.Fatal("WEB01 应被标为 owned（host=web01 ↔ WEB01.CORP.LOCAL）")
	}
	if owned(web02) {
		t.Fatal("其它主机不该被连坐")
	}
	if owned(user) {
		t.Fatal("同名的用户对象不该被标")
	}

	overview := tools.graphOverviewData()
	if got, _ := overview["ad_owned_total"].(int); got != 1 {
		t.Fatalf("概览应报 1 个已控 AD 对象：%#v", overview["ad_owned_total"])
	}
}
