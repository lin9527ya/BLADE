package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

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
