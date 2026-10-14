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

// lootNode 造一个战果节点（据点 / 凭据）。
func lootNode(id int64, kind, state string, payload map[string]any) *db.Node {
	b, _ := json.Marshal(payload)
	return &db.Node{ID: id, Kind: kind, State: state, Payload: b}
}

// TestMarkdownLootSection 钉住战果段的渲染：据点把「怎么进去的」讲清楚（那是报告该讲的事），
// 凭据只列身份与来源——**本体一个字都不印**。报告是要交出去的文档，还会被打进 P19 的批量
// 交付包；在里面印可用的凭据，等于把战果随交付物一起发走。
func TestMarkdownLootSection(t *testing.T) {
	const secret = "s3cret-do-not-print"
	foothold := lootNode(1, db.KindFoothold, db.StateLootActive, map[string]any{
		"summary": "在 web01 上拿到 www-data 的反弹 shell",
		"access":  "反弹 shell", "privilege": "user",
		"detail":   "nc -lvnp 4444 接回 10.0.0.9:47112",
		"evidence": "nc -lvnp 4444 → connect to 10.0.0.9:47112",
	})
	cred := lootNode(2, db.KindCredential, db.StateLootActive, map[string]any{
		"summary": "在 web01 的 wp-config.php 里读到数据库口令", "kind": "password",
		"username": "wp_user", "secret": secret,
		"evidence": "grep DB_PASSWORD wp-config.php",
	})
	md := Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		Footholds: []*db.Node{foothold}, Credentials: []*db.Node{cred},
	})
	for _, want := range []string{
		"## 战果（已攻陷的通路与取得的凭据）",
		"- 战果：据点 **1** 个、凭据 **1** 条",
		"### 据点（1）",
		"在 web01 上拿到 www-data 的反弹 shell",
		"- 通路：反弹 shell · 权限：user · 当前可用",
		"- 细节：nc -lvnp 4444 接回 10.0.0.9:47112",
		"### 凭据（1）",
		"- 类型：password · 账号：wp_user · 本体：已取得（原文不写入报告） · 当前可用",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("报告缺少 %q\n---\n%s", want, md)
		}
	}
	if strings.Contains(md, secret) {
		t.Fatalf("凭据本体进了报告：\n%s", md)
	}
}

// 没有战果时也如实交代一句，而不是留一个空壳标题（空标题容易被读成"渲染坏了"）。
func TestMarkdownLootSectionEmpty(t *testing.T) {
	md := Markdown(Input{Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0)})
	for _, want := range []string{
		"## 战果（已攻陷的通路与取得的凭据）",
		"_本次未取得据点或凭据。_",
		"- 战果：据点 **0** 个、凭据 **0** 条",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("空战果段缺少 %q\n---\n%s", want, md)
		}
	}
}
