package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 启动播种：内置领域模板首插入，并把同名场景分类的默认模板指过去。
// 第二次播种必须完全 no-op（模板不复活、分类预设不被改写）。
func TestSeedPlannerDomainTemplates(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg

	for name := range agent.PlannerDomainSeeds() {
		t.Cleanup(func() {
			_, _ = pg.Exec(`DELETE FROM task_categories WHERE nkey=$1`, name)
			_, _ = pg.Exec(`DELETE FROM planner_templates WHERE nkey=$1`, name)
		})
	}
	seedPlannerDomainTemplates(pg)

	for name, body := range agent.PlannerDomainSeeds() {
		tpl, err := pg.GetPlannerTemplateByNKey(name)
		if err != nil || tpl == nil {
			t.Fatalf("%s 模板未播种: %v", name, err)
		}
		if !tpl.Builtin || tpl.IsDefault {
			t.Fatalf("%s 应为 builtin 且非默认: %+v", name, tpl)
		}
		if tpl.Body != body {
			t.Fatalf("%s 正文与代码不一致（%d vs %d 字节）", name, len(tpl.Body), len(body))
		}
		cat, err := pg.GetTaskCategoryByNKey(name)
		if err != nil || cat == nil {
			t.Fatalf("%s 场景分类未播种: %v", name, err)
		}
		if cat.DefaultPlannerTemplateID != tpl.ID {
			t.Fatalf("%s 分类默认模板应指向 %d，实际 %d", name, tpl.ID, cat.DefaultPlannerTemplateID)
		}
		if !cat.DefaultCoverageEnabled {
			t.Fatalf("%s 分类应默认开启覆盖度", name)
		}
	}

	// 用户把分类预设清空后再次播种：不覆盖。
	first, err := pg.GetTaskCategoryByNKey("内网渗透")
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if _, err := pg.UpdateTaskCategoryPreset(first.ID, db.TaskCategoryPreset{DefaultCoverageEnabled: false}); err != nil {
		t.Fatal(err)
	}
	seedPlannerDomainTemplates(pg)
	again, err := pg.GetTaskCategoryByNKey("内网渗透")
	if err != nil || again == nil {
		t.Fatal(err)
	}
	if again.DefaultPlannerTemplateID != 0 || again.DefaultCoverageEnabled {
		t.Fatalf("重复播种覆盖了用户预设: %+v", again)
	}
}

// 端到端：选中「内网渗透」分类建任务 → 模板正文快照进 tasks.planner_prompt_override，
// 且快照内容就是该领域模板（不是全局默认模板）。
func TestDomainCategorySnapshotsTemplateIntoTask(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg
	for name := range agent.PlannerDomainSeeds() {
		t.Cleanup(func() {
			_, _ = pg.Exec(`DELETE FROM task_categories WHERE nkey=$1`, name)
			_, _ = pg.Exec(`DELETE FROM planner_templates WHERE nkey=$1`, name)
		})
	}
	seedPlannerDomainTemplates(pg)

	cat, err := pg.GetTaskCategoryByNKey("内网渗透")
	if err != nil || cat == nil {
		t.Fatal(err)
	}
	w := request(http.MethodPost, "/api/tasks", fmt.Sprintf(`{"description":"intranet-task","goal":"拿下内网域控","category_id":%d}`, cat.ID))
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("create task: %d %s", w.Code, w.Body)
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.DeleteTask(task.ID, DeleteTaskOptions{}) })

	var snapshot string
	if err := pg.QueryRow(`SELECT planner_prompt_override FROM tasks WHERE id=$1`, task.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != agent.PlannerDomainSeeds()["内网渗透"] {
		t.Fatalf("任务快照不是领域模板（%d 字节）", len(snapshot))
	}
}
