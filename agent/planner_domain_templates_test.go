package agent

import (
	"strings"
	"testing"
)

// 领域模板是「内置正文 + 五块领域差异」的整份副本。这里钉住「骨架不漂移」：
// plannerDefaultTmpl 的每一行都必须按原顺序出现在领域正文里。
//
// 为什么用子序列而不是 diff 相似度：领域模板只允许**插入**领域段落，不允许改写
// 骨架。内置正文一改（比如 intent_queue 背压措辞变了），这条断言就会失败，逼着
// 领域模板同步——否则它会在用户无感知的情况下退化成旧口径。
func TestPlannerDomainTemplatesKeepSkeleton(t *testing.T) {
	base := strings.Split(plannerDefaultTmpl, "\n")
	for name, body := range PlannerDomainSeeds() {
		lines := strings.Split(body, "\n")
		i := 0
		for _, want := range base {
			if strings.TrimSpace(want) == "" {
				continue
			}
			for i < len(lines) && lines[i] != want {
				i++
			}
			if i == len(lines) {
				t.Fatalf("%s: 骨架行在内置正文改动后没同步：%q", name, truncate(want, 80))
			}
			i++
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// 领域模板必须是可渲染的 Go template，且只引用目录内变量。
func TestPlannerDomainTemplatesRender(t *testing.T) {
	vars := PlannerVars{Goal: "拿下内网域控", Scope: "10.0.0.0/8", AssetSummary: "dc01", DataDir: "/data/tasks/1", Now: "2026-10-29"}
	for name, body := range PlannerDomainSeeds() {
		out, err := renderTmpl(body, vars)
		if err != nil {
			t.Fatalf("%s 渲染失败: %v", name, err)
		}
		if !strings.Contains(out, "拿下内网域控") {
			t.Fatalf("%s 没有渲染 {{.Goal}}", name)
		}
		if len(body) > 32<<10 {
			t.Fatalf("%s 超过 32KB 上限：%d", name, len(body))
		}
	}
}

// 每套领域模板都必须带上它自己的五块领域差异，且不能把 BLADE 的机制口径丢掉。
func TestPlannerDomainSections(t *testing.T) {
	seeds := PlannerDomainSeeds()
	wantDomains := []string{"内网渗透", "权限提升", "漏洞利用", "漏洞挖掘", "代码审计"}
	if len(seeds) != len(wantDomains) {
		t.Fatalf("领域模板数量 = %d，期望 %d", len(seeds), len(wantDomains))
	}
	// 骨架里 BLADE 独有的机制，任何一套都不能改写掉。
	skeletonMechanisms := []string{
		"intent_queue", "depends_on", "breadth.debt", "kind（完成口径）",
		"matching_playbooks", "detection_hotspots", "tunnels", "loot_upload",
		"outstanding", "no_output",
	}
	// 审核流与 executor_key 不在 BLADE，模板里不能出现它们的工具名。
	banned := []string{"add_review_intent", "update_intent", "cancel_intent", "executor_key"}

	for _, name := range wantDomains {
		body := seeds[name]
		if body == "" {
			t.Fatalf("缺少「%s」模板", name)
		}
		for _, want := range []string{
			"领域视角（" + name + "）", "本领域高价值 fact（" + name + "）",
			"本领域达成判据（" + name + "）", "本领域的方向生成侧重（" + name + "）",
			"本领域禁忌（" + name + "）",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("「%s」缺少领域段落 %q", name, want)
			}
		}
		for _, want := range skeletonMechanisms {
			if !strings.Contains(body, want) {
				t.Fatalf("「%s」缺少骨架机制 %q", name, want)
			}
		}
		for _, bad := range banned {
			if strings.Contains(body, bad) {
				t.Fatalf("「%s」引用了 BLADE 不存在的机制：%s", name, bad)
			}
		}
	}
}

// 领域模板被选为任务快照时，代码持有块仍然追加在后面。
func TestPlannerDomainTemplateAsTaskOverride(t *testing.T) {
	for name, body := range PlannerDomainSeeds() {
		got := plannerSystem("拿下内网域控", "/d", "/w", body)
		if !strings.HasPrefix(got, "你是一个网络安全平台授权渗透测试系统的") {
			t.Fatalf("%s: 任务快照没有生效: %q", name, truncate(got, 60))
		}
		for _, want := range []string{"领域视角（" + name + "）", "广度纪律", "战略提示（hints）的采纳纪律", "中间产物输出规约"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: 缺少 %q", name, want)
			}
		}
	}
}
