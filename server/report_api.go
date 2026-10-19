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
	live := make([]*db.Node, 0, len(findings))
	var retracted []report.Retracted
	var ruledOut, proofGaps []report.CandidateNote
	confidenceByNode := map[int64]string{}
	for _, n := range findings {
		m, ok := meta[n.ID]
		if ok && m.Confidence != "" {
			confidenceByNode[n.ID] = m.Confidence
		}
		switch {
		case ok && m.Status == db.FindingRetracted:
			retracted = append(retracted, report.Retracted{Node: n, Reason: m.RetractReason})
		case n.State == db.FindingRuledOut || (ok && m.Status == db.FindingRuledOut):
			ruledOut = append(ruledOut, report.CandidateNote{Node: n, Counterevidence: m.Counterevidence, ProofGap: m.ProofGap, Confidence: m.Confidence})
		case n.State == db.FindingOpenProofGap || (ok && m.Status == db.FindingOpenProofGap):
			proofGaps = append(proofGaps, report.CandidateNote{Node: n, Counterevidence: m.Counterevidence, ProofGap: m.ProofGap, Confidence: m.Confidence})
		default:
			live = append(live, n)
		}
	}
	intents, _ := t.Store.ListByKind(db.KindIntent, 1000)
	var notProbed []report.NotProbed
	for _, n := range intents {
		if n.State == "blocked" || n.State == "exhausted" {
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
	verdicts, _ := s.m.pg.LatestRetestVerdicts(tid)
	liveByID := map[int64]*db.Node{}
	for _, n := range live {
		liveByID[n.ID] = n
	}
	var retestNotes []report.RetestNote
	for nodeID, verdict := range verdicts {
		if verdict == "reproduced" {
			continue
		}
		if n, ok := liveByID[nodeID]; ok {
			retestNotes = append(retestNotes, report.RetestNote{Node: n, Verdict: verdict})
		}
	}
	sort.Slice(retestNotes, func(i, j int) bool { return retestNotes[i].Node.ID < retestNotes[j].Node.ID })
	counts := map[string]int{}
	for _, ty := range []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"} {
		ns, _ := s.m.Assets().QueryByType(ty, 100000, 0)
		if len(ns) > 0 {
			counts[ty] = len(ns)
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
		AssetCounts: counts, Findings: live, Retracted: retracted, NotProbed: notProbed, NoEvidence: noEvidence, Coverage: coverage, Retests: retestNotes,
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
