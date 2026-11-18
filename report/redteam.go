package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// RedTeamMarkdown 是红队叙事版报告：先讲攻击链与影响，再讲发现与证据。
//
// 复用默认报告已经加载好的 Input，不新增数据源、不二次读库；凭据只印身份与指纹，
// 任何章节都不输出 secret。它不是“另一份事实”，只是同一批事实的另一种编排。
func RedTeamMarkdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 红队作业报告 — %s\n\n", nz(in.Title, "未命名任务"))
	fmt.Fprintf(&b, "- **作业目标**：%s\n", nz(in.Goal, "（未指定）"))
	fmt.Fprintf(&b, "- **生成时间**：%s\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **模板**：redteam（攻击链与影响优先；不含凭据明文）\n\n")

	// 作业摘要
	fmt.Fprintf(&b, "## 作业摘要\n\n")
	fmt.Fprintf(&b, "- 确认发现：**%d** 个\n", len(in.Findings))
	fmt.Fprintf(&b, "- 据点：**%d** 个；凭据：**%d** 条\n", len(in.Footholds), len(in.Credentials))
	if len(in.Artifacts) > 0 {
		uncleaned := 0
		for _, n := range in.Artifacts {
			if n.State == db.StateArtifactActive {
				uncleaned++
			}
		}
		fmt.Fprintf(&b, "- 遗留物：**%d** 件（其中**未清理 %d 件**）\n", len(in.Artifacts), uncleaned)
	}
	if len(in.Footprints) > 0 {
		fmt.Fprintf(&b, "- 目标侧痕迹：**%d** 条\n", len(in.Footprints))
	}
	if len(in.DataGaps) > 0 {
		b.WriteString("\n**数据缺项**：\n")
		for _, gap := range in.DataGaps {
			fmt.Fprintf(&b, "- ⚠ %s\n", gap)
		}
	}
	b.WriteString("\n")

	// 攻击链
	if len(in.Chains) > 0 {
		fmt.Fprintf(&b, "## 攻击链\n\n")
		for i, ch := range in.Chains {
			fmt.Fprintf(&b, "### 链 %d：%s\n\n", i+1, ch.Head)
			for _, step := range ch.Steps {
				fmt.Fprintf(&b, "- %s「%s」", intentRef(in.IntentOrdinal, step.IntentID), clipRunes(step.Summary, 100))
				if len(step.Techniques) > 0 {
					fmt.Fprintf(&b, "（%s）", strings.Join(step.Techniques, ", "))
				}
				if len(step.Produced) > 0 {
					fmt.Fprintf(&b, " → 产出：%s", strings.Join(step.Produced, "、"))
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	// 战果动用
	fmt.Fprintf(&b, "## 战果与动用\n\n")
	if len(in.Footholds) == 0 && len(in.Credentials) == 0 {
		b.WriteString("本次作业没有登记据点或凭据。\n\n")
	}
	for _, n := range in.Footholds {
		fmt.Fprintf(&b, "- 据点「%s」", nz(nodeLabel(n), fmt.Sprintf("#%d", n.ID)))
		if n.State == db.StateLootLost {
			b.WriteString("（已失效）")
		}
		b.WriteString("\n")
		writeLootUses(&b, in.LootUsedBy[n.ID], n.State == db.StateLootActive, in.IntentOrdinal)
	}
	for _, n := range in.Credentials {
		label := nodeLabel(n)
		if label == "" {
			label = fmt.Sprintf("#%d", n.ID)
		}
		fmt.Fprintf(&b, "- 凭据「%s」", clipRunes(label, 80))
		if n.State == db.StateLootLost {
			b.WriteString("（已失效）")
		}
		b.WriteString("\n")
		writeLootUses(&b, in.LootUsedBy[n.ID], n.State == db.StateLootActive, in.IntentOrdinal)
	}
	b.WriteString("\n")

	// AD 路径
	if len(in.ADPaths) > 0 {
		fmt.Fprintf(&b, "## 域内攻击路径\n\n")
		for i, p := range in.ADPaths {
			fmt.Fprintf(&b, "### 路径 %d：%s → %s\n\n", i+1, p.Start, p.Target)
			if len(p.TargetTags) > 0 {
				fmt.Fprintf(&b, "目标价值：%s\n\n", strings.Join(p.TargetTags, "、"))
			}
			for _, hop := range p.Steps {
				fmt.Fprintf(&b, "- %s --%s--> %s\n", hop.From, ADRelLabel(hop.Rel), hop.To)
			}
			b.WriteString("\n")
		}
		b.WriteString("以上路径表示**存在**利用链，不代表已实施。\n\n")
	}

	// 检测验证
	fmt.Fprintf(&b, "## 检测验证\n\n")
	if len(in.Detections) == 0 {
		b.WriteString("本次没有检测验证记录。\n\n")
	} else {
		counts := map[string]int{}
		for _, n := range in.Detections {
			counts[n.State]++
		}
		fmt.Fprintf(&b, "- 已检出：**%d**；未检出：**%d**；无法判定：**%d**\n",
			counts[db.StateDetectionTriggered], counts[db.StateDetectionNotTriggered], counts[db.StateDetectionUnknown])
		b.WriteString("\n「无法判定」不是「未被检出」——三态分列，不合成检出率。\n\n")
	}

	// 凭据尝试
	if len(in.CredentialAttempts) > 0 {
		fmt.Fprintf(&b, "## 凭据尝试台账\n\n")
		writeCredentialAttempts(&b, in.CredentialAttempts)
	}

	// 确认发现
	fmt.Fprintf(&b, "## 确认发现\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("没有通过确认门的发现。\n\n")
	} else {
		sorted := append([]*db.Node(nil), in.Findings...)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sevOrder(parseFinding(sorted[i]).Severity) < sevOrder(parseFinding(sorted[j]).Severity)
		})
		for _, n := range sorted {
			f := parseFinding(n)
			fmt.Fprintf(&b, "### [%s] %s\n\n", strings.ToUpper(nz(f.Severity, "unknown")), nz(f.Name, f.Summary))
			if f.VulnClass != "" {
				fmt.Fprintf(&b, "- 类型：%s\n", f.VulnClass)
			}
			if c := in.ConfidenceByNode[n.ID]; c != "" {
				fmt.Fprintf(&b, "- 置信度：%s\n", confidenceLabel(c))
			}
			if f.Summary != "" {
				fmt.Fprintf(&b, "- 摘要：%s\n", f.Summary)
			}
			b.WriteString("\n")
		}
	}

	// 遗留物
	if len(in.Artifacts) > 0 {
		fmt.Fprintf(&b, "## 遗留物\n\n")
		for _, n := range in.Artifacts {
			state := "未清理"
			if n.State != db.StateArtifactActive {
				state = "已清理/已接受"
			}
			fmt.Fprintf(&b, "- %s（%s）\n", nz(nodeLabel(n), fmt.Sprintf("#%d", n.ID)), state)
		}
		b.WriteString("\n")
	}
	return b.String()
}
