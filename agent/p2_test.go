package agent

import (
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 图健康回流：有死依赖时 overview 应带 graph_health。
func TestGraphHealthInOverview(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	pre, err := ts.AddIntent(map[string]any{"summary": "前置方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	down, err := ts.AddIntent(map[string]any{"summary": "依赖前置的下游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(down, db.RelDependsOn, pre); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetIntentState(pre, "blocked"); err != nil {
		t.Fatal(err)
	}
	data := tools.graphOverviewData()
	health, _ := data["graph_health"].(map[string]any)
	counts, _ := health["counts"].(map[string]int)
	if counts["dead_dependency"] == 0 {
		t.Fatalf("graph_health 应报 dead_dependency：%+v", data["graph_health"])
	}
}

// 真计数：overview 给出 intents_total / edges_total，不再只给窗口长度。
func TestOverviewTrueCounts(t *testing.T) {
	tools, _, _ := newToolWorker(t)
	data := tools.graphOverviewData()
	if _, ok := data["intents_total"]; !ok {
		t.Fatal("overview 缺 intents_total")
	}
	if _, ok := data["edges_total"]; !ok {
		t.Fatal("overview 缺 edges_total")
	}
}

// DeepSeek 直连搜索工具：关闭或非 deepseek 后端不注册，开启且有 key 时注册两个。
func TestDeepSeekToolsGated(t *testing.T) {
	if got := deepSeekTools(WebSearchOpts{}); len(got) != 0 {
		t.Fatalf("关闭时不应注册：%d", len(got))
	}
	got := deepSeekTools(WebSearchOpts{Enabled: true, Backend: "deepseek", DeepSeekAPIKey: "k"})
	if len(got) != 2 || got[0].Name() != "deepseek_search" || got[1].Name() != "deepseek_fetch" {
		t.Fatalf("开启时应注册 search/fetch：%+v", got)
	}
}
