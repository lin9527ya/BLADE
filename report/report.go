// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
	// Retracted lists findings retracted after retesting disproved them; they
	// stay on file (墓碑) but leave the findings body.
	Retracted []Retracted
	// NotProbed lists intents that ended blocked/exhausted — 没测成不得当作已测干净。
	NotProbed []NotProbed
}

// Retracted is one finding retracted after retesting disproved it.
type Retracted struct {
	Node   *db.Node
	Reason string
}

// NotProbed is one intent that ended without a real probe (blocked/exhausted).
type NotProbed struct {
	Node *db.Node
}

// nodeLabel extracts a human-readable label from a node payload (intent uses
// title/summary; falls back through common keys).
func nodeLabel(n *db.Node) string {
	var m map[string]any
	if err := json.Unmarshal(n.Payload, &m); err != nil {
		return ""
	}
	for _, k := range []string{"title", "summary", "name", "description", "goal"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{p.VulnClass, p.Name, p.Severity, p.Summary, p.Evidence.PoC}
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 渗透测试报告 — %s\n\n", nz(in.Title, "未命名任务"))
	fmt.Fprintf(&b, "- **任务目标**：%s\n", nz(in.Goal, "（未指定）"))
	fmt.Fprintf(&b, "- **生成时间**：%s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## 摘要\n\n")
	fmt.Fprintf(&b, "- 确认发现：**%d** 个\n", len(in.Findings))
	fmt.Fprintf(&b, "- 资产：")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// findings
	fmt.Fprintf(&b, "## 发现\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("_本次未确认漏洞。_\n\n")
	} else {
		fs := make([]findingView, 0, len(in.Findings))
		for _, n := range in.Findings {
			fs = append(fs, parseFinding(n))
		}
		sort.SliceStable(fs, func(i, j int) bool { return sevRank[fs[i].Severity] < sevRank[fs[j].Severity] })
		for i, f := range fs {
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")))
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / 证据：**\n\n```\n%s\n```\n\n", f.PoC)
			}
		}
	}

	// 未测成（blocked/exhausted）：不得当作已测干净。
	fmt.Fprintf(&b, "## 未测成（blocked/exhausted，不得当作已测干净）\n\n")
	if len(in.NotProbed) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, np := range in.NotProbed {
			reason := ""
			if np.Node.BlockedReason != "" {
				reason = " — " + np.Node.BlockedReason
			}
			label := nodeLabel(np.Node)
			if label == "" {
				label = "（无标题）"
			}
			fmt.Fprintf(&b, "- #%d [%s] %s%s\n", np.Node.ID, np.Node.State, label, reason)
		}
		b.WriteString("\n")
	}

	// 已撤回（复测推翻；不进正文，保留在案）。
	fmt.Fprintf(&b, "## 已撤回（复测推翻；不进正文，保留在案）\n\n")
	if len(in.Retracted) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, r := range in.Retracted {
			f := parseFinding(r.Node)
			fmt.Fprintf(&b, "- #%d [%s] %s — 原因：%s\n", r.Node.ID, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")), nz(r.Reason, "（未记录）"))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
