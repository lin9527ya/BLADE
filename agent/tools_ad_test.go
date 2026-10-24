package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// ad_paths 工具：域内采集数据导进来之后，规划/执行侧要**看得见**这条路。
// 只有人能在页面上看，规划层就还是每轮从外面重新打一遍——那正是战果层要解决的问题。

// 云上枚举结果也要被 ad_paths 看见：路径算法与 AD 共用，只读 AD 会让云侧对象
// 在 agent 视角里消失（页面却能看到）。
func TestADPathsToolIncludesCloudObjects(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	user, err := ts.AddNode(db.KindCloudObject, map[string]any{"category": "user", "name": "alice"}, 0, db.StateADActive, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	role, err := ts.AddNode(db.KindCloudObject, map[string]any{"category": "role", "name": "admin-role", "highvalue": true}, 0, db.StateADActive, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(user, db.RelCan, role); err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.WorkerTools(), "ad_paths", `{}`)
	for _, want := range []string{"alice", "admin-role"} {
		if !strings.Contains(out, want) {
			t.Fatalf("云上对象 %q 应出现在路径里：%s", want, out)
		}
	}
}

func TestADPathsToolReadsImportedGraph(t *testing.T) {
	tools, ts, _ := newToolWorker(t)

	// 空图：给一句能照着做的说明，而不是一个空对象。
	out := callTool(t, tools.WorkerTools(), "ad_paths", `{}`)
	if !strings.Contains(out, "还没有域内/云上数据") {
		t.Errorf("空图该说明原因与下一步：%s", out)
	}

	// 导入最小的一套：jdoe 是 IT Support 成员，IT Support 是 WS01 本机管理员，
	// da 在 WS01 上有会话，da 属于 Domain Admins。
	user, _ := ts.AddNode(db.KindADObject, map[string]any{"category": "user", "name": "JDOE@LAB.LOCAL"}, 0, db.StateADActive, "test", nil)
	itg, _ := ts.AddNode(db.KindADObject, map[string]any{"category": "group", "name": "IT Support"}, 0, db.StateADActive, "test", nil)
	ws, _ := ts.AddNode(db.KindADObject, map[string]any{"category": "computer", "name": "WS01.LAB.LOCAL"}, 0, db.StateADActive, "test", nil)
	da, _ := ts.AddNode(db.KindADObject, map[string]any{"category": "user", "name": "DA@LAB.LOCAL"}, 0, db.StateADActive, "test", nil)
	dag, _ := ts.AddNode(db.KindADObject, map[string]any{"category": "group", "name": "DOMAIN ADMINS@LAB.LOCAL", "highvalue": true}, 0, db.StateADActive, "test", nil)
	for _, e := range [][3]int64{{user, itg, 0}, {itg, ws, 0}, {da, ws, 0}, {da, dag, 0}} {
		rel := db.RelMemberOf
		switch {
		case e[1] == ws && e[0] == itg:
			rel = db.RelAdminTo
		case e[1] == ws:
			rel = db.RelSessionOn
		}
		if err := ts.Link(e[0], rel, e[1]); err != nil {
			t.Fatal(err)
		}
	}

	out = callTool(t, tools.PlannerTools(), "ad_paths", `{}`)
	var res struct {
		Paths []struct {
			Start  string `json:"start"`
			Target string `json:"target"`
			Steps  []struct {
				Rel  string `json:"rel"`
				Note string `json:"note"`
			} `json:"steps"`
		} `json:"paths"`
		HighValue int `json:"highvalue_targets"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("工具输出不是 JSON：%v raw=%s", err, out)
	}
	if len(res.Paths) != 1 || !strings.Contains(res.Paths[0].Target, "DOMAIN ADMINS") {
		t.Fatalf("该读出到 Domain Admins 的那条路：%s", out)
	}
	if res.Paths[0].Start != "JDOE@LAB.LOCAL" {
		t.Errorf("起点该是非特权的 JDOE：%+v", res.Paths[0])
	}
	var rels []string
	for _, s := range res.Paths[0].Steps {
		rels = append(rels, s.Rel)
	}
	if got := strings.Join(rels, ">"); got != "member_of>admin_to>session_on>member_of" {
		t.Errorf("跳序不对：%s", got)
	}

	// 概览里也该有这条摘要（不给全量，但要让规划层知道图上有域内数据）。
	ov := callTool(t, tools.PlannerTools(), "graph_overview", `{}`)
	if !strings.Contains(ov, "ad_graph") || !strings.Contains(ov, "highvalue_targets") {
		t.Errorf("graph_overview 该带 ad_graph 摘要：%s", ov)
	}
}
