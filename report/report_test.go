package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/artex/db"
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

// 战果段要讲清「拿到的东西用在了哪一步」。
//
// 只列「拿到了这个据点/这条凭据」是资产清单；写清它被哪条意图动用，才是作业叙述——这是交付物
// 里最接近「攻击链」的东西，也是 LootUsedBy 存在的理由。到手但没用过的要如实写「尚未动用」，
// 否则读报告的人会以为它已被推进。
func TestMarkdownLootUses(t *testing.T) {
	used := lootNode(1, db.KindFoothold, db.StateLootActive, map[string]any{
		"summary": "在 web01 上拿到 root 的反弹 shell", "access": "反弹 shell", "privilege": "root",
	})
	idle := lootNode(2, db.KindCredential, db.StateLootActive, map[string]any{
		"summary": "在 web01 读到数据库口令", "kind": "password", "username": "wp_user",
	})
	md := Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		Footholds: []*db.Node{used}, Credentials: []*db.Node{idle},
		LootUsedBy: map[int64][]LootUse{
			1: {{IntentID: 12, Summary: "以 web01 为跳板看内网还有什么"}},
		},
	})
	if !strings.Contains(md, "- 动用于：intent-12「以 web01 为跳板看内网还有什么」") {
		t.Errorf("据点该带出「动用于哪条意图」：\n%s", md)
	}
	// 没被用过的凭据要如实说，不能默不作声。
	if !strings.Contains(md, "- 尚未动用") {
		t.Errorf("到手但没用过的凭据该写「尚未动用」：\n%s", md)
	}
	// 「已动用于 intent-12」与「尚未动用」不能同时出现在同一条上。
	if n := strings.Count(md, "尚未动用"); n != 1 {
		t.Errorf("「尚未动用」应只出现 1 次（只有那条凭据），实际 %d 次", n)
	}

	// 长摘要按 rune 截断，不切出半个汉字。
	long := strings.Repeat("多步", 60)
	md = Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		Footholds:  []*db.Node{lootNode(3, db.KindFoothold, db.StateLootActive, map[string]any{"summary": "x"})},
		LootUsedBy: map[int64][]LootUse{3: {{IntentID: 7, Summary: long}}},
	})
	if !strings.Contains(md, "…") {
		t.Errorf("超长摘要该被截断：\n%s", md)
	}
	if !utf8.ValidString(md) {
		t.Error("截断切出了非法 UTF-8（半个汉字）")
	}
}

// 攻击路径段：把「拿到了什么」之后的「用它走到了哪一步」连成一条读得出来的路。
// 这是分析里那句「报告里的提权/横向只能靠文案」的兑现点——路径来自图里已记的 uses/yields 边，
// 不是另写一段叙述。
func TestMarkdownAttackChains(t *testing.T) {
	md := Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		Chains: []AttackChain{{
			Head: "据点「在 web01 上拿到 root 的反弹 shell」",
			Steps: []ChainStep{
				{IntentID: 13, Summary: "读 wp-config 找数据库口令", Produced: []string{"凭据「wp_user 口令」"}},
				{IntentID: 21, Summary: "用 wp_user 横向到 db01", Produced: []string{"据点「db01 的 root shell」"}},
			},
		}},
	})
	for _, want := range []string{
		"## 攻击路径（从已得的访问展开）",
		"**路径 1**：起点 据点「在 web01 上拿到 root 的反弹 shell」",
		"- intent-13「读 wp-config 找数据库口令」 → 得到：凭据「wp_user 口令」",
		"- intent-21「用 wp_user 横向到 db01」 → 得到：据点「db01 的 root shell」",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("攻击路径段缺少 %q\n---\n%s", want, md)
		}
	}
	// 顺序：战果（拿到了什么）→ 攻击路径（用它走到了哪）→ 发现。读者是按这条线读的。
	iLoot := strings.Index(md, "## 战果")
	iChain := strings.Index(md, "## 攻击路径")
	iFind := strings.Index(md, "## 发现")
	if !(iLoot < iChain && iChain < iFind) {
		t.Errorf("段落顺序不对：战果=%d 攻击路径=%d 发现=%d", iLoot, iChain, iFind)
	}
}

// 没有多步链时不出这一节——空壳标题会被读成「渲染坏了」。
func TestMarkdownAttackChainsAbsentWhenEmpty(t *testing.T) {
	md := Markdown(Input{Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0)})
	if strings.Contains(md, "攻击路径") {
		t.Errorf("没有链时不该出现攻击路径段：\n%s", md)
	}
}

// 技术覆盖（ATT&CK）：红队交付物是按技术组织的——客户要的是「你打了哪些 TTP」。
// 顺序必须稳定（按 ID 升序），否则同一个任务两次生成的报告会不一样。
func TestMarkdownTechniqueTally(t *testing.T) {
	md := Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		TechniqueTally: map[string]int{"T1552.001": 2, "T1190": 3, "T1021": 1},
	})
	for _, want := range []string{
		"## 技术覆盖（ATT&CK）",
		"- T1021 —— 1 条意图",
		"- T1190 —— 3 条意图",
		"- T1552.001 —— 2 条意图",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("技术覆盖缺少 %q\n---\n%s", want, md)
		}
	}
	if !(strings.Index(md, "T1021") < strings.Index(md, "T1190") &&
		strings.Index(md, "T1190") < strings.Index(md, "T1552.001")) {
		t.Errorf("技术表该按 ID 升序（否则两次生成的报告不一致）：\n%s", md)
	}
	// 没标注技术时不出这一节。
	empty := Markdown(Input{Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0)})
	if strings.Contains(empty, "技术覆盖") {
		t.Errorf("没有技术标注时不该出现技术覆盖段：\n%s", empty)
	}
}

// 攻击路径上的每一跳要带上它的 ATT&CK 标注——「用 T1552.001 读了凭据」才是一句完整的话。
func TestMarkdownChainStepTechniques(t *testing.T) {
	md := Markdown(Input{
		Title: "t", Goal: "g", GeneratedAt: time.Unix(0, 0),
		Chains: []AttackChain{{
			Head: "据点「web01 的 root shell」",
			Steps: []ChainStep{{
				IntentID: 13, Summary: "读 wp-config",
				Techniques: []string{"T1552.001", "T1083"},
				Produced:   []string{"凭据「wp_user 口令」"},
			}},
		}},
	})
	if !strings.Contains(md, "- intent-13「读 wp-config」 [T1552.001 T1083] → 得到：凭据「wp_user 口令」") {
		t.Errorf("跳上的技术标注没渲染出来：\n%s", md)
	}
}
