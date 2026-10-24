package agent

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 一行式渲染：类型标签、置信度、来源任务都要出现（后来人靠这三样判断可信度与出处）。
func TestRenderKnowledgeLine(t *testing.T) {
	line := renderKnowledgeLine(db.CompanyKnowledge{
		Kind: db.KnowledgeKindConclusion, Summary: "后台列表接口未校验角色",
		Confidence: "high", SourceTaskID: "12",
	})
	for _, want := range []string{"[结论]", "后台列表接口未校验角色", "置信度 high", "来自任务 #12"} {
		if !strings.Contains(line, want) {
			t.Fatalf("缺少 %q: %s", want, line)
		}
	}
	// 没标定置信度/没来源时不该凭空补（不猜）。
	bare := renderKnowledgeLine(db.CompanyKnowledge{Kind: db.KnowledgeKindFact, Summary: "x"})
	if strings.Contains(bare, "置信度") || strings.Contains(bare, "来自任务") {
		t.Fatalf("没标的字段不该出现: %s", bare)
	}
	// 未知 kind 原样显示（向前兼容），长摘要截断。
	long := renderKnowledgeLine(db.CompanyKnowledge{Kind: "future_kind", Summary: strings.Repeat("很", 300)})
	if !strings.Contains(long, "[future_kind]") || !strings.HasSuffix(long, "…") {
		t.Fatalf("未知 kind 应原样显示且长摘要截断: %s", long)
	}
}

// 常驻索引里的"本公司已知"段：≤5 行 + 截断提示 + 只读先验的定性说明。
func TestBuildContextIndexCompanyLines(t *testing.T) {
	block := BuildContextIndex(ContextIndexVars{Store: nil, CompanyLines: nil})
	if block != "" {
		t.Fatalf("没有 store 时不该有索引: %q", block)
	}
	// 用真 store 的路径在验收里覆盖；这里只钉住"给了行就渲染、超 5 行给提示"的渲染规则。
	lines := []string{"[结论] a", "[结论] b", "[结论] c", "[结论] d", "[结论] e", "[结论] f", "[结论] g"}
	rendered := renderCompanySection(lines, true)
	for _, want := range []string{"本公司已知", "只读先验", "[结论] a", "[结论] e", "还有 2 条"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("缺少 %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "[结论] f") {
		t.Fatalf("超过 5 行不该逐条展开:\n%s", rendered)
	}
}
