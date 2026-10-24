// 报告导出、审计导出与手动 GC。
// 从 server.go 拆出：纯搬运，零行为变化。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/report"
)

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no active task")
		return
	}
	findings, _ := t.Store.ListByKind(db.KindFinding, 1000) // 纯漏洞（事实是独立的 KindFact，不进报告）
	// 撤回(retracted)的漏洞出正文、进「已撤回」段；blocked/exhausted 的意图单列「未测成」。
	tid, _ := strconv.ParseInt(t.ID, 10, 64)
	meta, _ := s.m.pg.FindingMetaByNodeID(tid)
	buckets := partitionReportFindings(findings, meta)
	live := buckets.live
	retracted := buckets.retracted
	ruledOut, proofGaps, closed := buckets.ruledOut, buckets.proofGaps, buckets.closed
	confidenceByNode := buckets.confidenceByNode
	intents, _ := t.Store.ListByKind(db.KindIntent, 1000)
	var notProbed []report.NotProbed
	for _, n := range intents {
		if n.State == "blocked" || n.State == "exhausted" || n.State == "no_output" {
			notProbed = append(notProbed, report.NotProbed{Node: n})
		}
	}
	var noEvidence []*db.Node
	for _, n := range live {
		if m, ok := meta[n.ID]; !ok || m.TrafficCount == 0 {
			noEvidence = append(noEvidence, n)
		}
	}
	coverage, _ := t.Store.CoverageMatrix(tid)
	// 复核结论：最新复测非"复现"的发现一并点名（存疑的会被 planner/人工再次审视）。
	// 复核结论用**全部**发现来找节点：`fixed` 的发现现在进了「已关闭」桶、不在 live 里，
	// 但它恰恰是复测结论最该点名的一条（「已修复（复测确认）」）。用 liveByID 会把它丢掉。
	verdicts, _ := s.m.pg.LatestRetestVerdicts(tid)
	allByID := map[int64]*db.Node{}
	for _, n := range findings {
		allByID[n.ID] = n
	}
	var retestNotes []report.RetestNote
	for nodeID, verdict := range verdicts {
		if verdict == "reproduced" {
			continue
		}
		if n, ok := allByID[nodeID]; ok {
			retestNotes = append(retestNotes, report.RetestNote{Node: n, Verdict: verdict})
		}
	}
	sort.Slice(retestNotes, func(i, j int) bool { return retestNotes[i].Node.ID < retestNotes[j].Node.ID })
	counts := map[string]int{}
	// 资产计数必须**只数本任务范围内**的。此前用全局 QueryByType：报告里的「资产」是整个库里
	// 所有作战的资产——既是跨任务泄漏，也让「范围内资产数」这个验收项失去意义。
	// 口径与覆盖卡片（tasks_api.go）、planner 概览（agent/tools.go）统一到 TaskCoverageWithSources，
	// 三处不再各报一个数。老任务没有 task_scope 时回退到 assets.task_ids 关联计数（同样不跨任务）。
	if cov, cerr := s.m.Assets().TaskCoverageWithSources(tid); cerr == nil && cov.Denominator > 0 {
		for _, bt := range cov.ByType {
			counts[bt.Type] = bt.Total
		}
	} else {
		if cerr != nil {
			log.Printf("[report] 任务 %s 覆盖口径资产计数失败，回退到 task_ids 关联计数：%v", t.ID, cerr)
		}
		if m, ferr := s.m.Assets().CountsByTypeForTask(tid); ferr == nil {
			counts = m
		} else {
			log.Printf("[report] 任务 %s 资产计数回退也失败，报告将不列资产数：%v", t.ID, ferr)
		}
	}
	// 战果：据点与凭据各取全量（同 findings 的取法）。它们不进 digest（coldgraph 只折
	// intent/fact），所以不会被折叠吞掉，按 kind 直取即可。
	footholds, _ := t.Store.ListByKind(db.KindFoothold, 1000)
	credentials, _ := t.Store.ListByKind(db.KindCredential, 1000)
	// 遗留物（P43）：我们留在目标上的东西。同样按 kind 直取全量。
	artifacts, _ := t.Store.ListByKind(db.KindArtifact, 1000)
	// 检测验证：同样按 kind 直取全量（也不进 digest）。
	detections, _ := t.Store.ListByKind(db.KindDetection, 1000)

	// 战果动用关系（intent --uses--> 战果）：交付物里最接近「攻击链」的东西——只写「拿到了
	// 什么」是资产清单，写清「拿它走到了哪一步」才是作业叙述。
	//
	// 「算不算真动用」复用 db.LootConsumingIntentState，与 agent 侧算 outstanding 是**同一条
	// 判定**：中途 blocked/exhausted 收场的意图不算动用，否则到手的能力会在报告里假装已被处理。
	intentState := make(map[int64]string, len(intents))
	intentSummary := make(map[int64]string, len(intents))
	intentTech := make(map[int64][]string, len(intents))
	for _, n := range intents {
		intentState[n.ID] = n.State
		var p struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		intentSummary[n.ID] = p.Summary
		intentTech[n.ID] = db.NodeTechniques(n)
	}
	// 技术覆盖与 agent 侧的概览统计共用 db.TechniqueTally——同一条规则（软删除不计）
	// 分两处写，报告和图迟早会报出两个不同的数字。
	techniqueTally := db.TechniqueTally(intents)
	lootUsedBy := map[int64][]report.LootUse{}
	var chains []report.AttackChain
	if edges, err := t.Store.Edges(5000); err == nil {
		for _, e := range edges {
			if e.Rel != db.RelUses || !db.LootConsumingIntentState(intentState[e.From]) {
				continue
			}
			lootUsedBy[e.To] = append(lootUsedBy[e.To], report.LootUse{
				IntentID: e.From,
				Summary:  intentSummary[e.From],
			})
		}
		// 攻击路径：把 uses/yields 两种边接起来读（见 server/attack_chains.go）。不引入新模型。
		chains = buildAttackChains(edges, lootLabels(footholds, credentials, artifacts), intentSummary, intentState, intentTech)
	}
	// 域内路径（P32）：与任务页的「AD 攻击路径」同一份实现——报告里写的和页面看到的不该是两个答案。
	adPaths := adPathsForReport(t.Store)

	// 凭据尝试台账（P43）：报告要说清"动过谁的账号"——那是客户复盘时最想核对的一件事。
	// 读不出来时记日志、**不静默略过**：报告里少一节，比让人以为"一次都没试过"安全得多。
	credAttempts, err := s.m.pg.ListCredentialAttempts(tid)
	if err != nil {
		log.Printf("[report] 任务 %s 凭据尝试台账读取失败：%v", t.ID, err)
	}

	md := report.Markdown(report.Input{
		Title: t.Description, Goal: t.Goal, GeneratedAt: time.Now(),
		AssetCounts: counts, Findings: live, Retracted: retracted, Closed: closed, NotProbed: notProbed, NoEvidence: noEvidence, Coverage: coverage, Retests: retestNotes,
		ConfidenceByNode: confidenceByNode, RuledOut: ruledOut, ProofGaps: proofGaps,
		Footholds: footholds, Credentials: credentials, Artifacts: artifacts, LootUsedBy: lootUsedBy, Chains: chains, ADPaths: adPaths,
		TechniqueTally: techniqueTally, Detections: detections,
		CredentialAttempts: credAttempts,
	})
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(md))
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, []any{})
		return
	}
	writeJSON(w, 200, map[string]any{"entries": t.Guard.Audit(), "attributions": t.Guard.Attributions()})
}

// gc is a no-op stub (GC not yet implemented in the new asset store).
func (s *Server) gc(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"removed": 0})
}

// --- utils ---

// reportBuckets 是报告分区结果：正文（live）与四类不进正文但必须在案的桶。
type reportBuckets struct {
	live             []*db.Node
	retracted        []report.Retracted
	ruledOut         []report.CandidateNote
	proofGaps        []report.CandidateNote
	closed           []report.ClosedNote
	confidenceByNode map[int64]string
}

// partitionReportFindings 按 triage 状态把发现分桶。
//
// 正文只放**当前仍然成立**的发现。误报 / 重复 / 已忽略 / 已修复 / 已解决此前落进 default
// 分支，被当作「确认发现」印进正文与计数——一份把已推翻的结论算成确认漏洞的报告，
// 交付即失信。关闭的发现**不静默丢弃**：进「已关闭」段，交付物要能回答"这条后来怎么了"。
//
// 注意与 db.FindingPromotableForKnowledge 的取舍不同：那个问"能不能沉淀成知识"，
// 这个问"还进不进正文"，两件事不该共用一张状态表。
func partitionReportFindings(findings []*db.Node, meta map[int64]db.FindingMeta) reportBuckets {
	b := reportBuckets{live: make([]*db.Node, 0, len(findings)), confidenceByNode: map[int64]string{}}
	for _, n := range findings {
		m, ok := meta[n.ID]
		if ok && m.Confidence != "" {
			b.confidenceByNode[n.ID] = m.Confidence
		}
		switch {
		case ok && m.Status == db.FindingRetracted:
			b.retracted = append(b.retracted, report.Retracted{Node: n, Reason: m.RetractReason})
		case n.State == db.FindingRuledOut || (ok && m.Status == db.FindingRuledOut):
			b.ruledOut = append(b.ruledOut, report.CandidateNote{Node: n, Counterevidence: m.Counterevidence, ProofGap: m.ProofGap, Confidence: m.Confidence})
		case n.State == db.FindingOpenProofGap || (ok && m.Status == db.FindingOpenProofGap):
			b.proofGaps = append(b.proofGaps, report.CandidateNote{Node: n, Counterevidence: m.Counterevidence, ProofGap: m.ProofGap, Confidence: m.Confidence})
		case ok && findingClosedStatus(m.Status):
			b.closed = append(b.closed, report.ClosedNote{Node: n, Status: m.Status, Confidence: m.Confidence})
		default:
			b.live = append(b.live, n)
		}
	}
	return b
}

// findingClosedStatus 报告分区用：这些状态说明「这条结论已不再作为当前风险成立」。
func findingClosedStatus(status string) bool {
	switch status {
	case db.FindingFalsePositive, db.FindingDuplicate, db.FindingIgnored, db.FindingFixed, db.FindingResolved:
		return true
	}
	return false
}
