package intercept

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNarrowRejectsWidening pins the safety half of 改参放行: anything that adds,
// lengthens, enlarges, flips false→true, replaces array items, or swaps in a host
// outside the original target set is refused outright.
func TestNarrowRejectsWidening(t *testing.T) {
	orig := json.RawMessage(`{"command":"nmap -sV -p 1-1000 example.com","timeout_ms":30000,"force":false,"hosts":["a.example.com","b.example.com"],"opts":{"deep":false,"n":5}}`)
	cases := map[string]string{
		"新增键":         `{"command":"nmap -sV -p 1-1000 example.com","extra":"rm -rf /"}`,
		"字符串加长":       `{"command":"nmap -sV -p 1-1000 example.com --script vuln"}`,
		"字符串改写":       `{"command":"nmap -sV -p 1-500 example.com"}`,
		"字符串重排":       `{"command":"example.com nmap -sV"}`,
		"数值放大":        `{"command":"nmap -sV -p 1-1000 example.com","timeout_ms":60000}`,
		"布尔反向":        `{"command":"nmap -sV -p 1-1000 example.com","force":true}`,
		"数组加项":        `{"command":"nmap -sV -p 1-1000 example.com","hosts":["a.example.com","b.example.com","c.example.com"]}`,
		"数组换项":        `{"command":"nmap -sV -p 1-1000 example.com","hosts":["evil.example.com"]}`,
		"嵌套对象加键":      `{"command":"nmap -sV -p 1-1000 example.com","opts":{"deep":false,"n":5,"x":1}}`,
		"换 scope 外目标": `{"command":"nmap -sV -p 1-1000 evil.example.com"}`,
		"改成非对象":       `["nmap"]`,
		"非法 JSON":     `{`,
	}
	for name, edited := range cases {
		out, ok := Narrow("Bash", orig, json.RawMessage(edited))
		if ok {
			t.Fatalf("%s：必须被拒绝，实际通过（diff=%v）", name, out.Diff)
		}
		if len(out.Diff) == 0 || !strings.HasPrefix(out.Diff[0], "禁止：") {
			t.Fatalf("%s：拒绝必须给出可读原因，实际 %v", name, out.Diff)
		}
	}
}

// TestNarrowAcceptsShrinking pins the permissive half: dropping keys, shortening
// strings, lowering numbers, true→false, shorter arrays, fewer targets and whole
// subtrees removed are all legal narrowings, and each is spelled out in the diff.
func TestNarrowAcceptsShrinking(t *testing.T) {
	orig := json.RawMessage(`{"command":"nmap -sV -p 1-1000 -T4 example.com","timeout_ms":30000,"force":true,"hosts":["a.example.com","b.example.com"],"opts":{"deep":true,"n":5}}`)

	// 删键 + 缩短字符串 + 下调数值 + true→false + 数组变短 + 删子树。
	edited := json.RawMessage(`{"command":"nmap -sV example.com","timeout_ms":10000,"force":false,"hosts":["a.example.com"]}`)
	out, ok := Narrow("Bash", orig, edited)
	if !ok {
		t.Fatalf("合法收窄被拒: %v", out.Diff)
	}
	if !json.Valid(out.Input) {
		t.Fatalf("收窄结果必须是合法 JSON: %s", out.Input)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Input, &got); err != nil {
		t.Fatal(err)
	}
	if got["command"] != "nmap -sV example.com" || got["timeout_ms"].(float64) != 10000 {
		t.Fatalf("收窄结果不对: %v", got)
	}
	joined := strings.Join(out.Diff, "\n")
	for _, want := range []string{"收窄", "下调", "关闭", "缩短", "删除键 opts"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diff 缺少 %q:\n%s", want, joined)
		}
	}
	// 目标只减不增：删掉一个 host 合法。
	if _, ok := Narrow("Bash", orig, json.RawMessage(`{"command":"nmap -sV example.com","hosts":["b.example.com"]}`)); !ok {
		t.Fatal("目标集合收缩必须放行")
	}
	// 原样放行也合法（未改动）。
	if out, ok := Narrow("Bash", orig, orig); !ok || !strings.Contains(strings.Join(out.Diff, " "), "未改动") {
		t.Fatalf("未改动的参数应放行并标注: %v", out.Diff)
	}
}

// TestNarrowTargetSubsetAcrossTools pins the second line of defense for non-shell
// tools: the target set is extracted from whatever field carries it, so a shortened
// URL that points at a different host is still refused.
func TestNarrowTargetSubsetAcrossTools(t *testing.T) {
	orig := json.RawMessage(`{"url":"https://app.example.com/a/b/c?x=1"}`)
	// 同 host、更短的 URL → 放行。
	if _, ok := Narrow("WebFetch", orig, json.RawMessage(`{"url":"https://app.example.com/a"}`)); !ok {
		t.Fatal("同目标缩短 URL 必须放行")
	}
	// 换 host（字符在原文里都能找到、顺序也对，字符串规则放行）→ 目标子集规则必须拦下。
	out, ok := Narrow("WebFetch",
		json.RawMessage(`{"url":"http://a.com/x","note":"a.com"}`),
		json.RawMessage(`{"url":"http://a.co/x"}`))
	if ok || !strings.Contains(strings.Join(out.Diff, " "), "目标") {
		t.Fatalf("换 host 必须被目标子集规则拒绝: ok=%v diff=%v", ok, out.Diff)
	}
}
