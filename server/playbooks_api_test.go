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
	found := false
	for _, tr := range triggers {
		if tr.OnToolCall && len(tr.ToolNames) == 1 && tr.ToolNames[0] == "report_foothold" {
			found = true
		}
	}
	if !found {
		t.Fatalf("没有 report_foothold 触发器：%+v", triggers)
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
