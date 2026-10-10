package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func findingNode(id int64, state, vulnclass, name, severity string) *db.Node {
	payload, _ := json.Marshal(map[string]any{
		"vulnclass": vulnclass, "name": name, "severity": severity, "summary": name + " 摘要",
		"evidence": map[string]any{"by": "work#1", "poc": "curl -s http://t/x"},
	})
	return &db.Node{ID: id, Kind: db.KindFinding, State: state, Payload: payload}
}

// TestMarkdownCandidateClosureSections pins the three-state accounting in the
// report: in-body findings carry their confidence label (未标定 named, not
// guessed), ruled_out candidates land in the excluded section with their
// counterevidence, and open_proof_gap candidates land in the 存疑 section.
func TestMarkdownCandidateClosureSections(t *testing.T) {
	live := findingNode(1, "confirmed", "sqli", "搜索接口注入", "high")
	ruled := findingNode(2, db.FindingRuledOut, "ssrf", "图片代理疑似 SSRF", "high")
	gap := findingNode(3, db.FindingOpenProofGap, "xss", "搜索框疑似存储型 XSS", "medium")
	md := Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		Findings: []*db.Node{live},
		ConfidenceByNode: map[int64]string{
			1: db.ConfidenceHigh,
		},
		RuledOut:  []CandidateNote{{Node: ruled, Counterevidence: "应用层域名白名单：内网地址 403", Confidence: db.ConfidenceHigh}},
		ProofGaps: []CandidateNote{{Node: gap, ProofGap: "缺登录态，无法验证渲染执行", Confidence: db.ConfidenceLow}},
	})
	for _, want := range []string{
		"- 确认发现：**1** 个",
		"- 已排除候选：**1** 个",
		"- 存疑待补证：**1** 个",
		"**置信度**：高（可复现触发链）",
		"## 已排除候选（有反证；不进正文，保留在案）",
		"图片代理疑似 SSRF — 反证：应用层域名白名单：内网地址 403（置信度：高（可复现触发链））",
		"## 存疑待补证（缺口未闭合，不得视为安全）",
		"搜索框疑似存储型 XSS — 缺口：缺登录态，无法验证渲染执行（置信度：低（据指纹/版本/现象推断））",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("report missing %q:\n%s", want, md)
		}
	}
	// 未标定必须被点名而不是猜一个档位。
	md = Markdown(Input{Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0), Findings: []*db.Node{live}})
	if !strings.Contains(md, "**置信度**：未标定") {
		t.Fatalf("unlabeled confidence must be named:\n%s", md)
	}
	// 空段仍然渲染（对账面完整），而不是省略。
	if !strings.Contains(md, "## 已排除候选（有反证；不进正文，保留在案）\n\n_无。_") {
		t.Fatalf("empty closure sections must render as 无:\n%s", md)
	}
}
