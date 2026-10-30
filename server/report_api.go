// 报告导出、审计导出与手动 GC。
// 从 server.go 拆出：纯搬运，零行为变化。
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
	const reportListLimit = 1000
	var truncated []string
	var dataGaps []string
	findings, ferr := t.Store.ListByKind(db.KindFinding, reportListLimit) // 纯漏洞（事实是独立的 KindFact，不进报告）
	if ferr != nil {
		// 发现列表是报告正文的主体：读失败时输出一份「没有发现」的报告，比报错危险得多。
		log.Printf("[report] 任务 %s 发现列表读取失败，拒绝生成貌似完整的报告：%v", t.ID, ferr)
		writeErr(w, 503, "发现列表读取失败，无法生成完整报告，请稍后重试")
		return
	}
	if len(findings) >= reportListLimit {
		truncated = append(truncated, fmt.Sprintf("发现列表达到 %d 条上限：正文可能不完整，更新的一批未列出", reportListLimit))
	}
	// 撤回(retracted)的漏洞出正文、进「已撤回」段；blocked/exhausted 的意图单列「未测成」。
	tid, _ := strconv.ParseInt(t.ID, 10, 64)
	meta, merr := s.m.pg.FindingMetaByNodeID(tid)
	if merr != nil {
		// 处置状态/置信度是确认门的输入；降级会把已确认漏洞误打成待确认候选。
		log.Printf("[report] 任务 %s 发现处置状态读取失败，无法判定确认门：%v", t.ID, merr)
		writeErr(w, 503, "发现处置状态读取失败，无法判定确认门，请稍后重试")
		return
	}
	buckets := partitionReportFindings(findings, meta)
	live := buckets.live
	retracted := buckets.retracted
	ruledOut, proofGaps, closed := buckets.ruledOut, buckets.proofGaps, buckets.closed
	confidenceByNode := buckets.confidenceByNode
	// 复核结论先读：正文的「确认」判定需要最近一次复测结论（reproduced/fixed 可以越过
	// confidence 门槛），不能再等到写完 noEvidence 才加载。
	verdicts, verr := s.m.pg.LatestRetestVerdicts(tid)
	if verr != nil {
		log.Printf("[report] 任务 %s 复测结论读取失败，正文按 confidence/status 判定：%v", t.ID, verr)
		verdicts = map[int64]string{}
		dataGaps = append(dataGaps, "复测结论读取失败：复测 reproduced|fixed 的发现可能被归入待确认候选")
	}
	// 正文只放通过确认门的发现：high confidence / 人工确认 / 复测 reproduced|fixed。
	// 其余（medium/low 的 pending、进行中、存疑等）进「待确认候选」——它们不是漏洞证据，
	// 但也绝不能静默消失：交付物必须区分「没发现」「没测成」「待补证」「已确认」。
	var unconfirmed []*db.Node
	confirmedLive := make([]*db.Node, 0, len(live))
	for _, n := range live {
		m := meta[n.ID]
		if findingConfirmed(m.Confidence, m.Status, verdicts[n.ID]) {
			confirmedLive = append(confirmedLive, n)
		} else {
			unconfirmed = append(unconfirmed, n)
		}
	}
	live = confirmedLive
	intents, ierr := t.Store.ListByKind(db.KindIntent, reportListLimit)
	if ierr != nil {
		// 意图列表驱动「未测成」、攻击链与技术统计：缺了它报告会假装所有方向都测完了。
		log.Printf("[report] 任务 %s 意图列表读取失败，拒绝生成貌似完整的报告：%v", t.ID, ierr)
		writeErr(w, 503, "意图列表读取失败，无法生成完整报告，请稍后重试")
		return
	}
	if len(intents) >= reportListLimit {
		truncated = append(truncated, fmt.Sprintf("意图列表达到 %d 条上限：未测成/攻击链可能不完整", reportListLimit))
	}
	var notProbed []report.NotProbed
	for _, n := range intents {
		if n.State == "blocked" || n.State == "exhausted" || n.State == "no_output" {
			notProbed = append(notProbed, report.NotProbed{Node: n})
		}
	}
	// 同指纹的重复登记只在正文出现一次：同一件事数两遍会让客户以为洞很多，
	// 也让严重度合计虚高。重复项挂在代表项下面点名。
	live, duplicates := s.mergeDuplicateFindings(tid, live, meta)
	var noEvidence []*db.Node
	for _, n := range live {
		if m, ok := meta[n.ID]; !ok || m.TrafficCount == 0 {
			noEvidence = append(noEvidence, n)
		}
	}
	coverage, covErr := t.Store.CoverageMatrix(tid)
	if covErr != nil {
		log.Printf("[report] 任务 %s 覆盖矩阵读取失败，报告将不含覆盖明细：%v", t.ID, covErr)
		dataGaps = append(dataGaps, "覆盖矩阵读取失败：报告不含资产×风险类覆盖明细")
	}
	// 复核结论：最新复测非"复现"的发现一并点名（存疑的会被 planner/人工再次审视）。
	// 复核结论用**全部**发现来找节点：`fixed` 的发现现在进了「已关闭」桶、不在 live 里，
	// 但它恰恰是复测结论最该点名的一条（「已修复（复测确认）」）。用 liveByID 会把它丢掉。
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
	var scope *report.ScopeSummary
	if cov, cerr := s.m.Assets().TaskCoverageWithSources(tid); cerr == nil && cov.Denominator > 0 {
		for _, bt := range cov.ByType {
			counts[bt.Type] = bt.Total
		}
		scope = &report.ScopeSummary{
			ScopeRows: cov.ScopeRows, Denominator: cov.Denominator, Tested: cov.Tested, Pct: cov.Pct,
		}
		if t.CreatedAt > 0 {
			scope.WindowFrom = time.Unix(t.CreatedAt, 0).Format("2006-01-02 15:04")
		}
		if t.CompletedAt > 0 {
			scope.WindowTo = time.Unix(t.CompletedAt, 0).Format("2006-01-02 15:04")
		}
		// 未测资产清单：交付物必须能回答"什么没测"，否则"没发现漏洞"和"没测"被读成一回事。
		if untested, total, uerr := s.m.Assets().ListUntestedAssetsWithSources(tid, "", 20, 0); uerr == nil {
			scope.UntestedTotal = total
			for _, a := range untested {
				scope.Untested = append(scope.Untested, fmt.Sprintf("[%s] %s", a.Type, a.Label))
			}
		} else {
			log.Printf("[report] 任务 %s 未测资产清单读取失败，报告将只给总数：%v", t.ID, uerr)
			dataGaps = append(dataGaps, "未测资产清单读取失败：报告只给未测总数，不含清单")
		}
	} else {
		if cerr != nil {
			log.Printf("[report] 任务 %s 覆盖口径资产计数失败，回退到 task_ids 关联计数：%v", t.ID, cerr)
			dataGaps = append(dataGaps, "范围口径资产计数失败，资产数退回按任务关联统计")
		}
		if m, ferr := s.m.Assets().CountsByTypeForTask(tid); ferr == nil {
			counts = m
		} else {
			log.Printf("[report] 任务 %s 资产计数回退也失败，报告将不列资产数：%v", t.ID, ferr)
			dataGaps = append(dataGaps, "资产计数读取失败：报告不含资产统计")
		}
	}
	// 报告 Agent 为每条发现写的详细分析：链路里最值钱的一段，此前完全没进总报告。
	// 同时核对证据版本：写完之后又补了证据的分析要显式标「可能已过期」，不能拿旧结论当现状。
	reportDetails, rerr := s.m.pg.FindingReportsByNode(tid)
	findingReports := make(map[int64]string, len(reportDetails))
	findingReportStale := map[int64]string{}
	if rerr != nil {
		log.Printf("[report] 任务 %s 详细报告读取失败，正文将不含「详细分析」段：%v", t.ID, rerr)
		dataGaps = append(dataGaps, "详细分析读取失败：报告正文不含报告 Agent 的逐条分析")
	} else {
		for nodeID, detail := range reportDetails {
			if strings.TrimSpace(detail.Report) == "" {
				continue
			}
			findingReports[nodeID] = detail.Report
			switch {
			case detail.ReportEvidenceVersion < 0:
				findingReportStale[nodeID] = fmt.Sprintf("该详细分析撰写时未声明所读证据版本，当前证据版本为 v%d，可能已过期，请重新核对后再引用。", detail.EvidenceVersion)
			case detail.ReportEvidenceVersion != detail.EvidenceVersion:
				findingReportStale[nodeID] = fmt.Sprintf("该详细分析基于证据 v%d 撰写，当前证据为 v%d，可能已过期，请重新核对后再引用。", detail.ReportEvidenceVersion, detail.EvidenceVersion)
			}
		}
	}
	// 战果：据点与凭据各取全量（同 findings 的取法）。它们不进 digest（coldgraph 只折
	// intent/fact），所以不会被折叠吞掉，按 kind 直取即可。
	footholds, fhErr := t.Store.ListByKind(db.KindFoothold, 1000)
	credentials, crErr := t.Store.ListByKind(db.KindCredential, 1000)
	// 遗留物（P43）：我们留在目标上的东西。同样按 kind 直取全量。
	artifacts, arErr := t.Store.ListByKind(db.KindArtifact, 1000)
	// 检测验证：同样按 kind 直取全量（也不进 digest）。
	detections, dtErr := t.Store.ListByKind(db.KindDetection, 1000)
	// 目标侧痕迹：自动归纳与 Agent 补录共用同一节点 kind。来源（leaves 边）一并读，
	// 报告要能回答"哪一步动作留下了这条痕迹"。
	footprints, fpErr := t.Store.ListByKind(db.KindFootprint, 1000)
	footprintSources, _ := t.Store.FootprintSourceNodes()
	if fhErr != nil || crErr != nil || arErr != nil || dtErr != nil || fpErr != nil {
		log.Printf("[report] 任务 %s 战果/遗留物/检测/痕迹读取失败：据点=%v 凭据=%v 遗留物=%v 检测=%v 痕迹=%v", t.ID, fhErr, crErr, arErr, dtErr, fpErr)
		dataGaps = append(dataGaps, "战果/遗留物/检测/痕迹读取失败：对应段落可能不完整")
	}
	if len(footprints) >= 1000 {
		truncated = append(truncated, "测试痕迹达到 1000 条上限：报告可能不完整")
	}

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
	if edges, edgeErr := t.Store.Edges(5000); edgeErr == nil {
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
	} else {
		log.Printf("[report] 任务 %s 图边读取失败，报告不含攻击链与战果动用关系：%v", t.ID, edgeErr)
		dataGaps = append(dataGaps, "图边读取失败：攻击链与战果动用关系可能缺失")
	}
	// 域内路径（P32）：与任务页的「AD 攻击路径」同一份实现——报告里写的和页面看到的不该是两个答案。
	adPaths := adPathsForReport(t.Store)

	// 凭据尝试台账（P43）：报告要说清"动过谁的账号"——那是客户复盘时最想核对的一件事。
	// 读不出来时记日志、**不静默略过**：报告里少一节，比让人以为"一次都没试过"安全得多。
	credAttempts, err := s.m.pg.ListCredentialAttempts(tid)
	if err != nil {
		log.Printf("[report] 任务 %s 凭据尝试台账读取失败：%v", t.ID, err)
		dataGaps = append(dataGaps, "凭据尝试台账读取失败：报告不含对账号的尝试记录")
	}

	// 展示编号：任务内序号 + 全局 id。正文/附录写成「#3（id 659）」，
	// 工具与 API 寻址仍用全局 id（同一数字在两种编号下含义不同）。
	nodeOrd, _ := t.Store.NodeOrdinals(db.KindFinding, 0)
	intentOrd, _ := t.Store.IntentOrdinals(0)
	md := report.Markdown(report.Input{
		Title: t.Description, Goal: t.Goal, GeneratedAt: time.Now(),
		AssetCounts: counts, Findings: live, Unconfirmed: unconfirmed, Retracted: retracted, Closed: closed, NotProbed: notProbed, NoEvidence: noEvidence, Coverage: coverage, Retests: retestNotes,
		FindingReports: findingReports, FindingReportStale: findingReportStale, Scope: scope, Duplicates: duplicates, Truncated: truncated,
		DataGaps:         dataGaps,
		ConfidenceByNode: confidenceByNode, RuledOut: ruledOut, ProofGaps: proofGaps,
		NodeOrdinal: nodeOrd, IntentOrdinal: intentOrd,
		Footholds: footholds, Credentials: credentials, Artifacts: artifacts, Footprints: footprints,
		FootprintSources: footprintSources,
		LootUsedBy:       lootUsedBy, Chains: chains, ADPaths: adPaths,
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

// mergeDuplicateFindings 按「漏洞类型 + 端点集合」的指纹把重复登记合并到代表项。
// 没有绑定流量的发现没有指纹（无法判断是不是同一个洞），原样保留、不参与合并。
func (s *Server) mergeDuplicateFindings(tid int64, live []*db.Node, meta map[int64]db.FindingMeta) ([]*db.Node, map[int64][]int64) {
	if len(live) < 2 {
		return live, nil
	}
	ids := make([]int64, 0, len(live))
	for _, n := range live {
		if m, ok := meta[n.ID]; ok && m.ID > 0 {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return live, nil
	}
	endpoints, err := s.m.pg.FindingEndpoints(ids)
	if err != nil {
		// 读不出来就不合并：宁可多列一条，也不要因为一次 DB 抖动把两条不同的洞并掉。
		log.Printf("[report] 任务 %d 端点读取失败，本次跳过重复合并：%v", tid, err)
		return live, nil
	}
	byFP := map[string]int64{}
	dups := map[int64][]int64{}
	kept := make([]*db.Node, 0, len(live))
	for _, n := range live {
		m := meta[n.ID]
		refs := make([]report.EndpointRef, 0, len(endpoints[m.ID]))
		for _, e := range endpoints[m.ID] {
			refs = append(refs, report.EndpointRef{Method: e.Method, URL: e.URL})
		}
		fp := report.Fingerprint(nodeVulnClass(n), refs)
		if fp != "" {
			if rep, ok := byFP[fp]; ok {
				dups[rep] = append(dups[rep], n.ID)
				continue
			}
			byFP[fp] = n.ID
		}
		kept = append(kept, n)
	}
	if len(dups) == 0 {
		return live, nil
	}
	return kept, dups
}

func nodeVulnClass(n *db.Node) string {
	var p struct {
		VulnClass string `json:"vulnclass"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.VulnClass
}
