package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// TestPlaybooksAPI：列表/详情/废弃的鉴权与纪律——废弃必须给理由；未认证不可达。
func TestPlaybooksAPI(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	p, _, err := s.m.Assets().UpsertPlaybook(db.Playbook{
		Fingerprint:  db.PlaybookFingerprint{Technologies: []string{"wordpress", "php"}, FaviconMMH3: "-555"},
		TechniqueKey: "api-test", Title: "test-playbook: API 卡片", Summary: "一行摘要",
		Steps: "## 识别\n/wp-admin", Evidence: "curl ...", Confidence: db.ConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 本测试最后会把这张卡片**废弃**掉；不清理的话下一轮 UpsertPlaybook 复用它时
	// 状态仍是 deprecated，于是 status=active 的列表过滤为空——测试第二次必挂。
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM attack_playbooks WHERE id=$1`, p.ID) })

	w := request("GET", "/api/playbooks?q=test-playbook&status=active", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "API 卡片") {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var list struct {
		Playbooks []db.Playbook `json:"playbooks"`
		Total     int           `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Total != 1 || len(list.Playbooks) != 1 {
		t.Fatalf("list body: %v %s", err, w.Body)
	}

	w = request("GET", fmt.Sprintf("/api/playbooks/%d", p.ID), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "wp-admin") {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}

	// 废弃必须给理由（与公司知识/发现撤回同款纪律）。
	w = request("PATCH", fmt.Sprintf("/api/playbooks/%d", p.ID), `{"status":"deprecated"}`)
	if w.Code != 400 {
		t.Fatalf("废弃无理由应 400，得 %d %s", w.Code, w.Body)
	}
	w = request("PATCH", fmt.Sprintf("/api/playbooks/%d", p.ID), `{"status":"deprecated","reason":"打法已过期"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "deprecated") {
		t.Fatalf("废弃: %d %s", w.Code, w.Body)
	}

	// 未认证：401（路由挂在 requireAuth 下）。
	r := httptest.NewRequest("GET", "/api/playbooks", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != 401 {
		t.Fatalf("未认证访问应 401，得 %d", rec.Code)
	}
}

// TestSeedPlaybookAgent：启动迁移预置了「打法沉淀」agent、报告据点触发器与工具绑定。
func TestSeedPlaybookAgent(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	a, err := pg.GetAgentByKey("playbook")
	if err != nil || a == nil {
		t.Fatalf("playbook agent 未预置：%v", err)
	}
	// 同一测试库可能被 TestReseedPlaybookPromptForGoalTrigger 留成用户自定义正文；
	// 本条用例验证的是"默认正文被 seed"，先恢复默认，避免依赖测试执行顺序。
	if _, err := pg.SavePrompt(a.ID, agent.PlaybookDefaultPrompt, "restore test fixture", "test"); err != nil {
		t.Fatal(err)
	}
	tmpl, err := pg.CurrentPrompt(a.ID)
	if err != nil || !strings.Contains(tmpl, "打法沉淀") || !strings.Contains(tmpl, "record_playbook") {
		t.Fatalf("playbook 提示词未 seed：%v %s", err, tmpl)
	}
	triggers, err := pg.ListTriggersFor("playbook")
	if err != nil {
		t.Fatal(err)
	}
	footholdTrigger, goalTrigger := false, false
	for _, tr := range triggers {
		if tr.OnToolCall && len(tr.ToolNames) == 1 && tr.ToolNames[0] == "report_foothold" {
			footholdTrigger = true
		}
		if tr.OnGoalMet && strings.Contains(tr.GoalMessage, "无新打法") {
			goalTrigger = true
		}
	}
	if !footholdTrigger {
		t.Fatalf("没有 report_foothold 触发器：%+v", triggers)
	}
	if !goalTrigger {
		t.Fatalf("没有目标达成触发器（打法库会漏掉靠确认漏洞完成的目标）：%+v", triggers)
	}
	tool, err := pg.GetTool("record_playbook")
	if err != nil || tool == nil || !contains(tool.Agents, "playbook") {
		t.Fatalf("record_playbook 没绑给 playbook agent：%+v %v", tool, err)
	}
	lookup, _ := pg.GetTool("lookup_playbooks")
	if lookup == nil || !contains(lookup.Agents, "worker") || !contains(lookup.Agents, "planner") {
		t.Fatalf("lookup_playbooks 默认绑定不对：%+v", lookup)
	}
}

// TestSeedPlaybookGoalTriggerBackfill：存量库只有 report_foothold 触发时，
// 一次性迁移必须补上目标达成触发；已有 on_goal_met 配置则保持不动。
func TestSeedPlaybookGoalTriggerBackfill(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	// 模拟老库：删掉目标达成触发与迁移标记。
	if _, err := pg.Exec(`DELETE FROM agent_triggers WHERE agent_key='playbook' AND on_goal_met`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key='playbook_goal_trigger_v1'`); err != nil {
		t.Fatal(err)
	}
	s.seedPlaybookGoalTrigger()
	triggers, err := pg.ListTriggersFor("playbook")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tr := range triggers {
		if tr.OnGoalMet && tr.GoalMessage != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("存量库回填后仍没有目标达成触发器：%+v", triggers)
	}
	// 再跑一次不应重复创建。
	before := len(triggers)
	if _, err := pg.Exec(`DELETE FROM settings WHERE key='playbook_goal_trigger_v1'`); err != nil {
		t.Fatal(err)
	}
	s.seedPlaybookGoalTrigger()
	after, _ := pg.ListTriggersFor("playbook")
	if len(after) != before {
		t.Fatalf("回填应幂等，触发数 %d → %d", before, len(after))
	}
}

// TestReseedPlaybookPromptForGoalTrigger：旧默认提示词（只讲 report_foothold）
// 要升级成兼容目标达成的版本；用户改过的正文不能被覆盖。
func TestReseedPlaybookPromptForGoalTrigger(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	a, err := pg.GetAgentByKey("playbook")
	if err != nil || a == nil {
		t.Fatalf("playbook agent 未预置：%v", err)
	}
	// 这个用例会把提示词改成用户自定义版本；同一包后续用例（TestSeedPlaybookAgent）
	// 依赖默认正文，必须恢复，否则单独/全量跑的先后顺序会改变结果。
	t.Cleanup(func() {
		_, _ = pg.SavePrompt(a.ID, agent.PlaybookDefaultPrompt, "restore test fixture", "test")
	})
	// 模拟仍带旧开头的默认提示词。
	if _, err := pg.SavePrompt(a.ID, "你是一个授权渗透测试系统里的「打法沉淀 agent」。刚刚有 worker 拿到了据点（report_foothold）。旧正文。", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key='playbook_prompt_goal_v1'`); err != nil {
		t.Fatal(err)
	}
	s.reseedPlaybookPrompt()
	cur, _ := pg.CurrentPrompt(a.ID)
	if !strings.Contains(cur, "目标达成") || !strings.Contains(cur, "on_goal_met") {
		t.Fatalf("旧提示词应升级为兼容目标达成：%s", cur)
	}

	// 用户改掉旧开头后，迁移不得覆盖。
	const custom = "这是用户自定义的打法沉淀提示词，不包含旧开头。"
	if _, err := pg.SavePrompt(a.ID, custom, "user", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key='playbook_prompt_goal_v1'`); err != nil {
		t.Fatal(err)
	}
	s.reseedPlaybookPrompt()
	cur, _ = pg.CurrentPrompt(a.ID)
	if cur != custom {
		t.Fatalf("用户自定义提示词被覆盖：%s", cur)
	}
}

// 人工编辑：内容/指纹/手法键可改，revision 递增；统计与「最近验证」不变；
// 改成另一张卡已有的（指纹+手法键）必须 409。
func TestUpdatePlaybookAPI(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	as := s.m.Assets()
	p, _, err := as.UpsertPlaybook(db.Playbook{
		Fingerprint:  db.PlaybookFingerprint{Technologies: []string{"WordPress", "PHP"}},
		TechniqueKey: "edit-test", Title: "old title", Summary: "old summary",
		Steps: "old steps", Evidence: "old evidence", Confidence: db.ConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM attack_playbooks WHERE id=$1`, p.ID) })
	other, _, err := as.UpsertPlaybook(db.Playbook{
		Fingerprint:  db.PlaybookFingerprint{Technologies: []string{"nginx"}},
		TechniqueKey: "other-key", Title: "other", Steps: "other steps",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM attack_playbooks WHERE id=$1`, other.ID) })

	// 制造统计数据，确认编辑不会清掉它们。
	if _, err := as.RecordPlaybookFeedback(p.ID, "worked", "verified once"); err != nil {
		t.Fatal(err)
	}
	// uses 由 lookup 单独累加；这里直接落一个非零值，验证编辑不会清统计。
	if _, err := s.m.pg.Exec(`UPDATE attack_playbooks SET uses=5 WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	before, err := as.GetPlaybook(p.ID)
	if err != nil || before == nil {
		t.Fatal(err)
	}
	if before.Uses == 0 || before.Successes == 0 || before.LastVerifiedAt == nil {
		t.Fatalf("fixture stats missing: %+v", before)
	}

	body, _ := json.Marshal(map[string]any{
		"fingerprint": map[string]any{
			"technologies": []string{"php", "wordpress", "mysql"},
			"favicon_mmh3": "-777", "service_name": "HTTPS", "port": 443, "page_title": "Admin",
		},
		"technique_key": "edit-test-v2",
		"title":         "edited title",
		"summary":       "edited summary",
		"steps":         "## edited\nnew steps",
		"evidence":      "new evidence",
		"confidence":    "medium",
	})
	w := request("PUT", fmt.Sprintf("/api/playbooks/%d", p.ID), string(body))
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	var updated db.Playbook
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "edited title" || updated.TechniqueKey != "edit-test-v2" ||
		updated.Confidence != db.ConfidenceMedium || updated.Evidence != "new evidence" {
		t.Fatalf("fields not updated: %+v", updated)
	}
	if got := updated.Fingerprint.Technologies; len(got) != 3 || got[0] != "mysql" || got[1] != "php" || got[2] != "wordpress" {
		t.Fatalf("fingerprint not normalized: %v", got)
	}
	if updated.Fingerprint.ServiceName != "https" || updated.Fingerprint.Port != 443 {
		t.Fatalf("fingerprint service/port: %+v", updated.Fingerprint)
	}
	if updated.Revision != before.Revision+1 {
		t.Fatalf("revision = %d, want %d", updated.Revision, before.Revision+1)
	}
	if updated.Uses != before.Uses || updated.Successes != before.Successes || updated.Failures != before.Failures {
		t.Fatalf("stats changed by edit: before=%+v after=%+v", before, updated)
	}
	if updated.LastVerifiedAt == nil || !updated.LastVerifiedAt.Equal(*before.LastVerifiedAt) {
		t.Fatalf("last_verified_at must not change on manual edit: before=%v after=%v", before.LastVerifiedAt, updated.LastVerifiedAt)
	}

	// 撞另一张卡（指纹+手法键）→ 409。
	conflict, _ := json.Marshal(map[string]any{
		"fingerprint":   other.Fingerprint,
		"technique_key": other.TechniqueKey,
		"title":         "collision",
		"steps":         "collision steps",
	})
	if w := request("PUT", fmt.Sprintf("/api/playbooks/%d", p.ID), string(conflict)); w.Code != 409 {
		t.Fatalf("conflict = %d %s, want 409", w.Code, w.Body)
	}

	// 必填校验与不存在。
	empty, _ := json.Marshal(map[string]any{"title": "no steps", "steps": ""})
	if w := request("PUT", fmt.Sprintf("/api/playbooks/%d", p.ID), string(empty)); w.Code != 400 {
		t.Fatalf("empty steps = %d, want 400", w.Code)
	}
	if w := request("PUT", "/api/playbooks/999999999", string(body)); w.Code != 404 {
		t.Fatalf("missing = %d, want 404", w.Code)
	}
}
