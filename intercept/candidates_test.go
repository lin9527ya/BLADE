package intercept

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lin9527ya/artex/db"
)

// TestCandidateTextsExpandsSurfaces pins the 遮挡面 expansion: nested string
// values, map keys, the whole raw input, and up to three rounds of percent
// decoding — with surfaces labelled for the audit trail.
func TestCandidateTextsExpandsSurfaces(t *testing.T) {
	find := func(cands []candidate, text string) (candidate, bool) {
		for _, c := range cands {
			if c.Text == text {
				return c, true
			}
		}
		return candidate{}, false
	}

	// 嵌套 string + 整段原文
	cands := candidateTexts([]byte(`{"command":"curl http://evil.example/x"}`))
	if c, ok := find(cands, "curl http://evil.example/x"); !ok || c.Surface != "nested_string" {
		t.Fatalf("nested string missing: %+v", cands)
	}
	if _, ok := find(cands, `{"command":"curl http://evil.example/x"}`); !ok {
		t.Fatalf("raw fallback missing: %+v", cands)
	}

	// 键也要检（编码进 key 的目标）
	cands = candidateTexts([]byte(`{"http://gov.example":"x"}`))
	if c, ok := find(cands, "http://gov.example"); !ok || c.Surface != "nested_key" {
		t.Fatalf("nested key missing: %+v", cands)
	}

	// 百分号编码：一轮
	cands = candidateTexts([]byte(`{"command":"curl http://%65%76%69%6c%2e%65%78%61%6d%70%6c%65/next"}`))
	if c, ok := find(cands, "curl http://evil.example/next"); !ok || c.Surface != "percent_decoded:1" {
		t.Fatalf("percent decode missing: %+v", cands)
	}

	// 多轮编码：a%2525b → a%25b → a%b（第 3 轮无变化即停）
	cands = candidateTexts([]byte(`{"command":"echo a%2525b"}`))
	c1, ok1 := find(cands, "echo a%25b")
	c2, ok2 := find(cands, "echo a%b")
	if !ok1 || c1.Surface != "percent_decoded:1" || !ok2 || c2.Surface != "percent_decoded:2" {
		t.Fatalf("multi-round decode wrong: %+v", cands)
	}

	// 非法 JSON：只有原文兜底，不 panic
	cands = candidateTexts([]byte(`{not json`))
	if len(cands) != 1 || cands[0].Surface != "raw" {
		t.Fatalf("malformed json should keep raw only: %+v", cands)
	}

	// 孤立 % 不破坏其余部分
	if got := decodePercentEscapes("100%25 done %zz"); got != "100% done %zz" {
		t.Fatalf("decodePercentEscapes=%q", got)
	}
}

// TestMatchRuleAcrossSurfaces pins the rule engine side: a deny rule whose pattern
// is a plain domain now catches the percent-encoded form and reports the surface.
func TestMatchRuleAcrossSurfaces(t *testing.T) {
	rule := compiledRule{InterceptRule: db.InterceptRule{
		Name: "block-evil", MatchTarget: "tool_input", MatchType: "string", Pattern: "evil.example", Action: "deny",
	}}
	text, surface, ok := matchRule(rule, "Bash", []byte(`{"command":"curl http://%65%76%69%6c%2e%65%78%61%6d%70%6c%65/x"}`))
	if !ok || surface != "percent_decoded:1" || !strings.Contains(text, "evil.example") {
		t.Fatalf("encoded bypass not caught: ok=%v surface=%s text=%q", ok, surface, text)
	}
	if _, _, ok := matchRule(rule, "Bash", []byte(`{"command":"curl http://good.example"}`)); ok {
		t.Fatal("clean input must not match")
	}

	// tool_name 规则仍只匹配工具名（不进入候选面遍历）
	nameRule := compiledRule{InterceptRule: db.InterceptRule{
		Name: "nome-Bash", MatchTarget: "tool_name", MatchType: "string", Pattern: "Bash", Action: "deny",
	}}
	if text, surface, ok := matchRule(nameRule, "Bash", []byte(`{}`)); !ok || surface != "tool_name" || text != "Bash" {
		t.Fatalf("tool_name rule broken: %v %s %q", ok, surface, text)
	}

	// regex 规则同样走候选面
	reRule := compiledRule{InterceptRule: db.InterceptRule{
		Name: "re-evil", MatchTarget: "tool_input", MatchType: "regex", Pattern: `evil\.example`, Action: "deny",
	}}
	// compiledRule 的 re 字段在 loadLocked 里编译；测试直接构造时手动编译
	reRule.re = regexp.MustCompile(`evil\.example`)
	if _, surface, ok := matchRule(reRule, "Bash", []byte(`{"command":"curl http://%65%76%69%6c%2e%65%78%61%6d%70%6c%65"}`)); !ok || surface != "percent_decoded:1" {
		t.Fatalf("regex rule must also see decoded surfaces: ok=%v surface=%s", ok, surface)
	}
}

// TestRenderRuleMessageSinglePass pins the template renderer: {match}/{tool}/{rule}
// are substituted exactly once — placeholder text inside the matched content must
// not be re-substituted (二次模板注入防护).
func TestRenderRuleMessageSinglePass(t *testing.T) {
	rule := compiledRule{InterceptRule: db.InterceptRule{Name: "r1", Action: "deny"}}
	rule.Message = "命中 {rule}：{match} @ {tool}"
	got := renderRuleMessage(rule, "payload with {tool} literal", "Bash")
	if !strings.Contains(got, "payload with {tool} literal") {
		t.Fatalf("matched content was re-substituted: %q", got)
	}
	if !strings.Contains(got, "命中 r1") || !strings.HasSuffix(got, "@ Bash") {
		t.Fatalf("template placeholders not rendered: %q", got)
	}
	rule.Message = ""
	if got := renderRuleMessage(rule, "x", "Bash"); !strings.Contains(got, "拦截规则 [r1]") {
		t.Fatalf("default message missing: %q", got)
	}
}
