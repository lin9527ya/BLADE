package db

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSearchPrecedentsFindsCrossTaskFacts(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tag := fmt.Sprintf("precedent-%d", time.Now().UnixNano())
	a, err := d.CreateTaskWithOptions("source "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.CreateTaskWithOptions("consumer "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(a.ID); _ = d.DeleteTask(b.ID) })
	if _, err := d.Exploration(a.ExplorationID).AddFactGraph(map[string]any{
		"summary":  "unique-precedent-" + tag,
		"detail":   "negative result from another task",
		"evidence": "curl -> 403",
	}, "worker", nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	res, err := d.Exploration(b.ExplorationID).SearchPrecedents("unique-precedent-"+tag, 10, PrecedentSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].TaskID != a.ID || res.Hits[0].Summary == "" {
		t.Fatalf("precedent search miss: %+v", res)
	}
	if !res.EvidenceOmitted || res.Hits[0].Evidence != "" {
		t.Fatalf("默认应省略 evidence：%+v", res)
	}
}

func TestSearchPrecedentsExcludesCurrentTask(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tag := fmt.Sprintf("precedent-self-%d", time.Now().UnixNano())
	a, err := d.CreateTaskWithOptions("self "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(a.ID) })
	ts := d.Exploration(a.ExplorationID)
	if _, err := ts.AddFactGraph(map[string]any{"summary": "self-only-" + tag}, "worker", nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	res, err := ts.SearchPrecedents("self-only-"+tag, 10, PrecedentSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("当前任务的事实不应作为先例返回：%+v", res.Hits)
	}
}

func TestSearchPrecedentsCampaignScope(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tag := fmt.Sprintf("precedent-camp-%d", time.Now().UnixNano())
	c1, err := d.CreateCampaign("camp1 "+tag, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := d.CreateCampaign("camp2 "+tag, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	src1, _ := d.CreateTaskWithOptions("src1 "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	src2, _ := d.CreateTaskWithOptions("src2 "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	consumer, _ := d.CreateTaskWithOptions("consumer "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	t.Cleanup(func() {
		_ = d.DeleteTask(src1.ID)
		_ = d.DeleteTask(src2.ID)
		_ = d.DeleteTask(consumer.ID)
		_, _ = d.DeleteCampaign(c1.ID)
		_, _ = d.DeleteCampaign(c2.ID)
	})
	if err := d.SetTaskCampaign(src1.ID, &c1.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTaskCampaign(consumer.ID, &c1.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTaskCampaign(src2.ID, &c2.ID); err != nil {
		t.Fatal(err)
	}
	needle := "campaign-needle-" + tag
	if _, err := d.Exploration(src1.ExplorationID).AddFactGraph(map[string]any{"summary": needle + " same"}, "worker", nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(src2.ExplorationID).AddFactGraph(map[string]any{"summary": needle + " other"}, "worker", nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	res, err := d.Exploration(consumer.ExplorationID).SearchPrecedents(needle, 10, PrecedentSearchOptions{Scope: PrecedentScopeCampaign})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].TaskID != src1.ID {
		t.Fatalf("campaign scope 应只返回同战役任务：%+v", res.Hits)
	}
	if res.Scope != PrecedentScopeCampaign {
		t.Fatalf("resolved scope=%q", res.Scope)
	}
}

func TestSearchPrecedentsGlobalRedactsSecrets(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tag := fmt.Sprintf("precedent-redact-%d", time.Now().UnixNano())
	src, _ := d.CreateTaskWithOptions("source "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	consumer, _ := d.CreateTaskWithOptions("consumer "+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	t.Cleanup(func() { _ = d.DeleteTask(src.ID); _ = d.DeleteTask(consumer.ID) })
	if _, err := d.Exploration(src.ExplorationID).AddFactGraph(map[string]any{
		"summary":  "leaked password=supersecret-" + tag,
		"evidence": "Authorization: Bearer abc123-" + tag,
	}, "worker", nil, 0, nil); err != nil {
		t.Fatal(err)
	}
	res, err := d.Exploration(consumer.ExplorationID).SearchPrecedents("leaked", 10, PrecedentSearchOptions{Scope: PrecedentScopeGlobal, IncludeEvidence: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("global precedent missing: %+v", res)
	}
	h := res.Hits[0]
	if strings.Contains(h.Summary, "supersecret-"+tag) || !strings.Contains(h.Summary, "[REDACTED]") {
		t.Fatalf("summary 未脱敏：%q", h.Summary)
	}
	if strings.Contains(h.Evidence, "abc123-"+tag) || !strings.Contains(h.Evidence, "[REDACTED]") {
		t.Fatalf("evidence 未脱敏：%q", h.Evidence)
	}
	if h.TaskName != fmt.Sprintf("task#%d", src.ID) {
		t.Fatalf("global task_name 应别名化：%q", h.TaskName)
	}
}
