package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"

	"github.com/Autumn-27/artex/db"
)

// 战果层（据点 / 凭据）的用例。落在三件事上，它们正是这个功能存在的理由与主要风险：
//   1. 记进去的战果能不能被规划层看见（graph_overview 的计数与窗口、intent→yields 血缘）；
//   2. 编不出证据的战果会不会被拒收（战果会被当作既定事实拿去规划，编一条会让后续意图跑空）；
//   3. 凭据本体有没有漏进概览（概览会被折进每一轮提示词、还可能进日志）。

// newLootWorker 建一个带意图的任务，返回可调用工具的 ToolSet、任务内的探索存储、意图 id。
func newLootWorker(t *testing.T) (*ToolSet, *db.ExplorationStore, int64) {
	t.Helper()
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions("loot task", "拿下一个据点", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	intent, err := ts.AddIntent(map[string]any{"summary": "打穿上传点"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(ts, "work#1")
	tools.SetTaskID(task.ID)
	return tools, ts, intent
}

// callLoot 按名字取工具并调用，返回渲染文本。
func callLoot(t *testing.T, tools []actool.CoreTool, name, input string) string {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() != name {
			continue
		}
		res, err := tl.Call(context.Background(), json.RawMessage(input), nil)
		if err != nil {
			t.Fatalf("%s 调用出错: %v", name, err)
		}
		return res.Flatten()
	}
	t.Fatalf("工具 %s 不在这套工具面里", name)
	return ""
}

func TestReportFootholdRecordsNodeAndLineage(t *testing.T) {
	tools, ts, intent := newLootWorker(t)
	out := callLoot(t, tools.WorkerTools(), "report_foothold",
		`{"summary":"在 web01 上拿到 www-data 的反弹 shell","access":"反弹 shell","privilege":"user",`+
			`"detail":"nc -lvnp 4444 接回 10.0.0.9:47112","evidence":"nc -lvnp 4444 → connect to 10.0.0.9:47112",`+
			`"confidence":"high","intent_id":`+strconv.FormatInt(intent, 10)+`}`)
	if !strings.HasPrefix(out, "foothold recorded: ") {
		t.Fatalf("据点没记上：%s", out)
	}

	nodes, err := ts.ListByKind(db.KindFoothold, 10)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("据点节点数 = %d (err=%v)，应为 1", len(nodes), err)
	}
	n := nodes[0]
	if n.Kind != db.KindFoothold {
		t.Errorf("kind = %q，应为 %q", n.Kind, db.KindFoothold)
	}
	if n.State != db.StateLootActive {
		t.Errorf("state = %q，应为 %q", n.State, db.StateLootActive)
	}
	var p map[string]any
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["access"] != "反弹 shell" || p["privilege"] != "user" {
		t.Errorf("payload 里的战果字段没落上：%v", p)
	}

	// 血缘：intent --yields--> 据点。planner 靠这条边把战果挂回"哪个方向打出来的"，
	// 也是 graph_overview 里 from_intent 的来源。
	edges, err := ts.Edges(100)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, e := range edges {
		if e.From == intent && e.To == n.ID && e.Rel == db.RelYields {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("没有 intent --yields--> 据点 的边：%+v", edges)
	}
}

// 战果必须带 evidence 与 confidence：它会被规划层当作既定事实拿去规划「能从这条通路
// 往下做什么」，一条编出来的 shell 会让后续所有意图跑空，而且看不出错在哪。被拒收的
// 调用一个字节都不该写进图。
func TestReportLootRejectsEvidenceFreeClaims(t *testing.T) {
	tools, ts, intent := newLootWorker(t)
	id := strconv.FormatInt(intent, 10)
	cases := []struct{ name, tool, in, want string }{
		{"据点缺 evidence", "report_foothold", `{"summary":"拿到 shell","confidence":"high","intent_id":` + id + `}`, "evidence"},
		{"据点缺 confidence", "report_foothold", `{"summary":"拿到 shell","evidence":"nc 回连成功","intent_id":` + id + `}`, "confidence"},
		{"据点 confidence 非法", "report_foothold", `{"summary":"拿到 shell","evidence":"nc 回连成功","confidence":"maybe","intent_id":` + id + `}`, "confidence"},
		{"据点缺 summary", "report_foothold", `{"summary":"  ","evidence":"nc 回连成功","confidence":"high","intent_id":` + id + `}`, "summary"},
		{"凭据缺 evidence", "report_credential", `{"summary":"读到 root 口令","kind":"password","secret":"x","confidence":"high","intent_id":` + id + `}`, "evidence"},
		{"凭据缺 confidence", "report_credential", `{"summary":"读到 root 口令","kind":"password","secret":"x","evidence":"cat /etc/shadow","intent_id":` + id + `}`, "confidence"},
	}
	for _, c := range cases {
		out := callLoot(t, tools.WorkerTools(), c.tool, c.in)
		if strings.HasPrefix(out, "foothold recorded: ") || strings.HasPrefix(out, "credential recorded: ") {
			t.Fatalf("%s：本该被拒，却记录成功：%s", c.name, out)
		}
		if !strings.Contains(out, c.want) {
			t.Fatalf("%s：拒绝理由没说清 %q：%s", c.name, c.want, out)
		}
	}

	for _, kind := range []string{db.KindFoothold, db.KindCredential} {
		if nodes, _ := ts.ListByKind(kind, 10); len(nodes) != 0 {
			t.Fatalf("被拒的调用往 %s 写了 %d 条节点", kind, len(nodes))
		}
	}
	if tools.Writes().Total() != 0 {
		t.Fatalf("被拒的调用不该计入写回数：%+v", tools.Writes())
	}
}

func TestReportCredentialRecords(t *testing.T) {
	tools, ts, intent := newLootWorker(t)
	out := callLoot(t, tools.WorkerTools(), "report_credential",
		`{"summary":"在 web01 的 wp-config.php 里读到数据库口令","kind":"password","username":"wp_user",`+
			`"secret":"db-pass-123","detail":"./wp-config.php: define('DB_PASSWORD', ...)","evidence":"grep DB_PASSWORD wp-config.php",`+
			`"confidence":"high","intent_id":`+strconv.FormatInt(intent, 10)+`}`)
	if !strings.HasPrefix(out, "credential recorded: ") {
		t.Fatalf("凭据没记上：%s", out)
	}
	nodes, err := ts.ListByKind(db.KindCredential, 10)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("凭据节点数 = %d (err=%v)，应为 1", len(nodes), err)
	}
	var p map[string]any
	_ = json.Unmarshal(nodes[0].Payload, &p)
	if p["username"] != "wp_user" || p["secret"] != "db-pass-123" {
		t.Errorf("凭据字段没落上：%v", p)
	}
	if got := tools.Writes().Credentials; got != 1 {
		t.Errorf("凭据写回计数 = %d，应为 1", got)
	}
}

// 凭据本体绝不能出现在概览里：概览会被折进每一轮 planner 与 worker 的提示词、还可能进日志。
// compactLoot 用的是字段**白名单**，所以这是结构性保证；本用例把它钉死——谁哪天把白名单
// 改成「拷一份 payload 再删 secret」，这里立刻红。同时钉住另一半：身份必须还在，
// 否则概览不带 secret 就不是"安全"，而是"没用"。
func TestGraphOverviewNeverLeaksCredentialSecret(t *testing.T) {
	tools, _, intent := newLootWorker(t)
	const secret = "Sup3rS3cret-db-passw0rd"
	out := callLoot(t, tools.WorkerTools(), "report_credential",
		`{"summary":"读到数据库口令","kind":"password","username":"wp_user","secret":"`+secret+`",`+
			`"evidence":"grep DB_PASSWORD wp-config.php","confidence":"high","intent_id":`+strconv.FormatInt(intent, 10)+`}`)
	if !strings.HasPrefix(out, "credential recorded: ") {
		t.Fatalf("凭据没记上：%s", out)
	}

	blob, err := json.Marshal(tools.graphOverviewData())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("凭据本体漏进了 graph_overview：%s", blob)
	}
	if !strings.Contains(string(blob), "wp_user") {
		t.Errorf("概览里该带上凭据身份（账号），否则带不出可用信息：%s", blob)
	}
}

// 计数必须是真的 COUNT 而不是展示窗口的长度：窗口就是用来截断的，
// 「手上有几个据点」是 planner 判断能不能继续往深的依据，被封顶的数字会静默低估战果。
func TestGraphOverviewReportsTrueLootTotals(t *testing.T) {
	tools, ts, _ := newLootWorker(t)
	const want = 12 // 超过 lootListCap(8)
	for i := 0; i < want; i++ {
		if _, err := ts.AddNode(db.KindCredential, map[string]any{
			"summary": fmt.Sprintf("credential %d", i), "secret": "s",
		}, 8, db.StateLootActive, "work#1", nil); err != nil {
			t.Fatal(err)
		}
	}
	out := tools.graphOverviewData()
	got, ok := out["credentials_total"].(int)
	if !ok {
		t.Fatalf("credentials_total 缺失或不是 int：%#v", out["credentials_total"])
	}
	if got != want {
		t.Fatalf("credentials_total=%d want=%d —— 总数被展示窗口截断了", got, want)
	}
	list, _ := out["credentials"].([]map[string]any)
	if len(list) >= want {
		t.Fatalf("凭据窗口 len=%d 未短于总数 %d——用例前提被破坏", len(list), want)
	}
}

// 战果工具要在 worker 与 main 两套工具面里都有——worker 是产出方（必须能当场记），
// main 是人机对话的补记口。
func TestLootToolsAreOnBothToolFaces(t *testing.T) {
	ts := NewToolSet(nil, "")
	faces := []struct {
		name  string
		tools []actool.CoreTool
	}{
		{"worker", ts.WorkerTools()},
		{"mainagent", ts.MainAgentTools()},
	}
	for _, face := range faces {
		names := map[string]bool{}
		for _, tl := range face.tools {
			names[tl.Name()] = true
		}
		for _, want := range []string{"report_foothold", "report_credential"} {
			if !names[want] {
				t.Errorf("%s 工具面缺少 %s", face.name, want)
			}
		}
	}
}
