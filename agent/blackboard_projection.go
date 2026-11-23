package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// populateGoalsAndHints fills goals, active hints, and exact pending_hints
// count into out using compact projections shared across Planner and Worker.
func (t *ToolSet) populateGoalsAndHints(out map[string]any, goalsCap int) {
	if goalsCap <= 0 {
		goalsCap = 100
	}
	goals, _ := t.ts.ListByKind(db.KindGoal, goalsCap)
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum

	hints, _ := t.ts.ListByKindState(db.KindHint, db.StateHintActive, db.MaxActiveHints)
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if p["asset_ids"] != nil {
			hint["asset_ids"] = p["asset_ids"]
		}
		if findingTrafficBindingEnabled() && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	if n, err := t.ts.CountByKindState(db.KindHint, db.StateHintActive); err == nil {
		out["pending_hints"] = n
	}
}

func (t *ToolSet) populateTaskAndScope(out map[string]any) {
	if description, goal, err := t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	}
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		if cc, err := t.ts.CampaignContext(); err == nil && cc != nil {
			out["campaign"] = cc
		}
	}
	if t.as != nil && t.taskID > 0 {
		sc := map[string]any{"policy": t.as.TaskAssetScopePolicy(t.taskID)}
		if roots, err := t.as.ScopeRootDomains(t.taskID); err == nil && len(roots) > 0 {
			sc["roots"] = roots
		}
		if scopes, err := t.as.ListTaskScope(t.taskID); err == nil {
			var ids []int64
			var names []string
			for _, row := range scopes {
				if row.Kind == "company" && row.CompanyID != nil {
					ids = append(ids, *row.CompanyID)
					if row.CompanyName != "" {
						names = append(names, row.CompanyName)
					}
				}
			}
			if len(ids) > 0 {
				sc["company_ids"] = ids
			}
			if len(names) > 0 {
				sc["companies"] = names
			}
		}
		out["asset_scope"] = sc
	}
}

func (t *ToolSet) populatePriorKnowledge(out map[string]any) {
	if ks := t.companyKnowledgeRows(); len(ks) > 0 {
		lines := make([]string, 0, len(ks))
		for _, k := range ks {
			lines = append(lines, renderKnowledgeLine(k))
		}
		out["company_knowledge"] = lines
		out["company_knowledge_note"] = "以上来自同公司【其它任务】的沉淀（只读）：当先验用，需要时自己复核；" +
			"它们不代表本任务已测过——本任务的进度仍以本任务图为准。"
	}
	if t.as != nil && t.taskID > 0 {
		if matches, err := t.as.MatchPlaybooksForTask(t.taskID, 3); err == nil && len(matches) > 0 {
			lines := make([]string, 0, len(matches))
			for _, m := range matches {
				lines = append(lines, renderPlaybookLine(m))
			}
			out["matching_playbooks"] = lines
			out["matching_playbooks_note"] = "以上是历史【已打穿】目标沉淀的同类打法（全局指纹库，只读）。" +
				"先确认目标指纹与卡片一致，再用 lookup_playbooks 取完整步骤快速验证；" +
				"历史命中不代表目标一定有洞，仍要实际触发。用完用 record_playbook_feedback 回报 worked/failed。"
		}
		if matches, err := t.as.MatchCVEsForTask(t.taskID, 3); err == nil && len(matches) > 0 {
			lines := make([]string, 0, len(matches))
			for _, m := range matches {
				lines = append(lines, renderCVEMatchLine(m))
			}
			out["matching_cves"] = lines
			out["matching_cves_note"] = "以上是按本任务范围内资产指纹/产品/版本匹配到的 CVE 库条目（全局只读线索）。" +
				"**CVE/版本匹配不是证据**：具体到某台资产时用 lookup_cves(asset_id=...) 取详情与 probe_guide；" +
				"有脚本且目标明确时才用 cve_exploit 实际触发。cve_exploit 只返回输出/execution_id，不会自动产生 finding/foothold。"
		}
	}
}

func (t *ToolSet) populateCoverageSummary(out map[string]any) {
	if t.as == nil || t.ts == nil || t.taskID <= 0 {
		return
	}
	m := map[string]any{}
	if !t.coverageDisabled {
		if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
			m["denominator"] = cov.Denominator
			m["tested"] = cov.Tested
			m["by_type"] = cov.ByType
			m["note"] = "coverage 是事实锚点口径的粗略参考（含直接关联任务），不是终态验收；具体资产的覆盖情况以这里的 coverage 为准。list_untested_assets 只是 fact 级 backlog，按需参考。"
			if cov.Denominator == 0 {
				m["pct"] = nil
				m["status"] = "范围未锚定"
			} else {
				m["pct"] = cov.Pct
			}
		}
	}
	if hosts, err := t.as.HostsByTaskWithSources(t.taskID); err == nil {
		m["host_count"] = len(hosts)
	}
	if len(m) > 0 {
		out["coverage"] = m
	}
}

func (t *ToolSet) populateActiveTunnels(out map[string]any) {
	if t.execService == nil || t.taskID <= 0 {
		return
	}
	rows, err := t.execService.ActiveForTask(t.taskIDStr())
	if err != nil {
		out["execution_state_unknown"] = true
		return
	}
	if len(rows) == 0 {
		return
	}
	var tunnels []map[string]any
	for _, r := range rows {
		if r.Tool != "tunnel_open" && !strings.HasPrefix(r.Description, tunnelDescPrefix) {
			continue
		}
		tunnels = append(tunnels, map[string]any{
			"execution_id": r.ID, "description": r.Description, "status": r.Status,
		})
	}
	if len(tunnels) > 0 {
		out["tunnels"] = tunnels
	}
}

// workerSituationData builds a lean situational projection tailored for a single
// cold-started per-intent worker. Unlike graphOverviewDataInternalWithAttention,
// it omits planner-only scheduling/backlog/archive sections, related_tasks prose,
// cold graph loads, folded_facts N+1 queries, and unused AD scans on non-AD tasks.
func (t *ToolSet) workerSituationData() map[string]any {
	out := map[string]any{}
	t.populateGoalsAndHints(out, 50)
	t.populatePriorKnowledge(out)
	t.populateTaskAndScope(out)
	t.populateActiveTunnels(out)

	// Teammate live runs: slim summaries prevent duplicate probing across concurrent intents.
	if running, err := t.ts.ListByKindState(db.KindIntent, "running", 8); err == nil && len(running) > 0 {
		out["running_intents"] = slimIntentNodes(running)
	}

	// Findings window + authoritative total so worker avoids duplicate finding reports.
	const workerFindingCap = 12
	vulnNodes, _ := t.ts.ListActiveFindings(workerFindingCap)
	if total, err := t.ts.CountActiveFindings(); err == nil {
		out["findings_total"] = total
	} else {
		out["findings_total"] = len(vulnNodes)
	}
	fids := make([]int64, 0, len(vulnNodes))
	for _, n := range vulnNodes {
		fids = append(fids, n.ID)
	}
	findingProducers, _ := t.ts.FactProducersForNodes(fids)
	findingList := make([]map[string]any, 0, len(vulnNodes))
	for _, n := range vulnNodes {
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := findingProducers[n.ID]; from > 0 {
			m["from_intent"] = from
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList

	// Recent facts slice solely for capabilities & proven dead-ends (ruled_out).
	factNodes, _ := t.ts.ListByKind(db.KindFact, 100)
	if total, err := t.ts.CountByKind(db.KindFact); err == nil {
		out["facts"] = total
	} else {
		out["facts"] = len(factNodes)
	}
	capCounts := map[string]int{}
	ruled := make([]map[string]any, 0, 8)
	for _, n := range factNodes {
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) != nil {
			continue
		}
		if en, ok := fp["enables"].([]any); ok {
			for _, v := range en {
				if sv, ok := v.(string); ok {
					capCounts[sv]++
				}
			}
		}
		if len(ruled) < 6 && asText(fp["polarity"]) == factPolarityNeg {
			ruled = append(ruled, map[string]any{"id": n.ID, "kind": "fact", "summary": clipLine(asText(fp["summary"]), 100)})
		}
	}
	if fs, err := t.ts.ListByKindState(db.KindFinding, "ruled_out", 4); err == nil {
		for _, n := range fs {
			if len(ruled) >= 8 {
				break
			}
			var fp map[string]any
			_ = json.Unmarshal(n.Payload, &fp)
			ruled = append(ruled, map[string]any{"id": n.ID, "kind": "finding", "summary": clipLine(asText(fp["summary"]), 100)})
		}
	}
	if len(capCounts) > 0 {
		out["capabilities"] = capCounts
	}
	if len(ruled) > 0 {
		out["ruled_out"] = ruled
	}

	// Active loot (footholds & credentials) so workers reuse existing access instead of re-exploiting.
	const workerLootCap = 8
	fhWindow, _ := t.ts.ListByKindState(db.KindFoothold, db.StateLootActive, workerLootCap)
	crWindow, _ := t.ts.ListByKindState(db.KindCredential, db.StateLootActive, workerLootCap)
	if total, err := t.ts.CountByKind(db.KindFoothold); err == nil {
		out["footholds_total"] = total
	}
	if total, err := t.ts.CountByKind(db.KindCredential); err == nil {
		out["credentials_total"] = total
	}
	lootIDs := make([]int64, 0, len(fhWindow)+len(crWindow))
	for _, n := range fhWindow {
		lootIDs = append(lootIDs, n.ID)
	}
	for _, n := range crWindow {
		lootIDs = append(lootIDs, n.ID)
	}
	lootProducers, _ := t.ts.FactProducersForNodes(lootIDs)
	usedLoot, _ := t.ts.LootUsedMap(lootIDs)
	out["footholds"] = compactLoot(fhWindow, lootProducers, usedLoot, "access", "privilege", "protocol", "host", "port", "url")
	out["credentials"] = compactCredentials(crWindow, lootProducers, usedLoot)

	// Skip AD/Cloud 20k-edge path traversal completely when no topology nodes exist.
	adCount, _ := t.ts.CountByKind(db.KindADObject)
	cloudCount, _ := t.ts.CountByKind(db.KindCloudObject)
	if adCount+cloudCount > 0 {
		if adNodes, err := loadTopologyNodes(t.ts); err == nil && len(adNodes) > 0 {
			if adEdges, err := t.ts.Edges(20000); err == nil {
				res := db.ADPaths(adNodes, adEdges, 0)
				ad := map[string]any{
					"objects": res.Objects, "highvalue_targets": res.HighValue,
					"paths_total": len(res.Paths),
				}
				const adPathPreview = 3
				if len(res.Paths) > 0 {
					preview := res.Paths
					if len(preview) > adPathPreview {
						preview = preview[:adPathPreview]
					}
					ad["shortest_paths"] = preview
				}
				out["ad_graph"] = ad
			}
		}
	}
	return out
}

func slimIntentNodes(nodes []*db.Node) []map[string]any {
	slim := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		entry := map[string]any{"id": n.ID, "state": n.State}
		if s, ok := p["summary"].(string); ok {
			entry["summary"] = clipLine(s, 100)
		}
		slim = append(slim, entry)
	}
	return slim
}

// blackboardDeltaCore builds a genuinely lightweight incremental projection for
// warm Planner hot sessions without running full-graph coldGraph/AD/related_tasks/
// 5000-edge queries first and throwing them away afterwards.
func (t *ToolSet) blackboardDeltaCore(after int64, limit int, changes db.BlackboardChanges) map[string]any {
	out := map[string]any{}
	t.populateGoalsAndHints(out, 100)
	t.populateTaskAndScope(out)
	t.populateCoverageSummary(out)
	if health := t.graphHealth(); len(health) > 0 {
		out["graph_health"] = health
	}

	openNodes, _ := t.ts.ListByKindState(db.KindIntent, "open", 16)
	runNodes, _ := t.ts.ListByKindState(db.KindIntent, "running", 10)
	out["open_intents"] = slimIntentNodes(openNodes)
	out["running_intents"] = slimIntentNodes(runNodes)

	totals := map[string]any{}
	if states, err := t.ts.CountIntentStates(); err == nil {
		delete(states, db.StateIntentDeleted)
		totals["intents"] = states
	}
	if snap, err := t.ts.IntentDispatchSnapshot(); err == nil {
		out["claimable_intents"] = snap.Claimable
		out["blocked_open_intents"] = snap.Blocked
		out["strict_blocked_intents"] = snap.Blocked
		out["frontier_open"] = snap.Open
		totals["open_intents"] = snap.Open
		totals["claimable"] = snap.Claimable
	}
	if dt, err := t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
		totals["done_intents"] = dt
	}
	if total, err := t.ts.CountByKindExcludingState(db.KindIntent, db.StateIntentDeleted); err == nil {
		out["intents_total"] = total
	}
	if total, err := t.ts.CountActiveFindings(); err == nil {
		out["findings_total"] = total
		totals["findings"] = total
	}
	if total, err := t.ts.CountByKind(db.KindFact); err == nil {
		out["facts"] = total
		totals["facts"] = total
	}
	if total, err := t.ts.CountByKind(db.KindFoothold); err == nil {
		out["footholds_total"] = total
		totals["footholds"] = total
	}
	if total, err := t.ts.CountByKind(db.KindCredential); err == nil {
		out["credentials_total"] = total
		totals["credentials"] = total
	}
	if total, err := t.ts.CountEdges(); err == nil {
		out["edges_total"] = total
		totals["edges"] = total
	}

	// Route distribution directly from SQL (with lightweight local recent slice).
	if stats, rerr := t.ts.RouteDistribution(20); rerr == nil && len(stats) > 0 {
		routes := make([]map[string]any, 0, len(stats))
		for _, st := range stats {
			routes = append(routes, map[string]any{
				"route": st.Route, "intents": st.Intents, "open": st.Open,
				"running": st.Running, "done": st.Done,
			})
		}
		out["routes"] = routes
	}

	// Unconsumed facts, branch review, capabilities & ruled_out over bounded recent window.
	factNodes, _ := t.ts.ListByKind(db.KindFact, 120)
	factIDs := make([]int64, 0, len(factNodes))
	for _, n := range factNodes {
		factIDs = append(factIDs, n.ID)
	}
	childIntents, _ := t.ts.DerivedChildrenForNodes(factIDs)
	covered, _ := t.ts.CoveredMembers()
	tracking, traceErr := t.ts.LeadTraceReport("", "", 20, 0)
	if traceErr != nil {
		out["lead_tracking_status"] = map[string]any{"state": "unknown", "error": traceErr.Error()}
	}
	if tracking != nil {
		factProducers, _ := t.ts.FactProducersForNodes(factIDs)
		recentLeads := make([]map[string]any, 0, 20)
		for _, lead := range tracking.PendingPreview {
			row := map[string]any{
				"id": lead.ID, "text": lead.Text, "from_fact": lead.FactID, "state": lead.State,
				"kind": lead.Kind, "asset_ids": lead.AssetIDs, "age_min": int(time.Since(lead.FactCreatedAt).Minutes()),
			}
			if source := factProducers[lead.FactID]; source > 0 {
				row["from_intent"] = source
			}
			recentLeads = append(recentLeads, row)
		}
		if len(recentLeads) > 0 {
			out["recent_leads"] = recentLeads
		}
		out["lead_tracking"] = tracking.Stats
		out["leads_total"] = tracking.Stats.Total
		out["leads_open"] = tracking.Stats.Pending
		out["leads_converted"] = tracking.Stats.Adopted
		out["leads_dismissed"] = tracking.Stats.Dismissed
		out["lead_tracking_status"] = map[string]any{"state": "complete", "note": "计数覆盖本任务全部结构化线索与提案；列表是窗口，完成表示意图执行结束，不是已证明漏洞。"}
	}

	var branchReview []map[string]any
	unconsumedHigh := make([]map[string]any, 0, 12)
	unconsumedOther := make([]map[string]any, 0, 12)
	capCounts := map[string]int{}
	ruled := make([]map[string]any, 0, 8)
	for _, n := range factNodes {
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		var factEnables []string
		if en, ok := fp["enables"].([]any); ok {
			for _, v := range en {
				if sv, ok := v.(string); ok {
					capCounts[sv]++
					factEnables = append(factEnables, sv)
				}
			}
		}
		pol, _ := fp["polarity"].(string)
		if pol == factPolarityNeg && len(ruled) < 6 {
			ruled = append(ruled, map[string]any{"id": n.ID, "kind": "fact", "summary": clipLine(asText(fp["summary"]), 100)})
		}
		if n.State == "origin" || n.State == "dismissed" {
			continue
		}
		_, isCovered := covered[n.ID]
		factOpenLeads := 0
		if tracking != nil {
			factOpenLeads = tracking.OpenByFact[n.ID]
		}
		if len(childIntents[n.ID]) > 0 && pol != factPolarityNeg && !isCovered && len(branchReview) < 8 {
			if detail := strings.TrimSpace(asText(fp["detail"])); detail != "" {
				branchReview = append(branchReview, map[string]any{
					"id": n.ID, "summary": asText(fp["summary"]),
					"child_intent_ids": childIntents[n.ID], "open_leads": factOpenLeads,
					"detail_excerpt": FenceUntrusted("已部分接手的事实", clipLine(detail, 240)),
				})
			}
		}
		if pol != factPolarityNeg && len(childIntents[n.ID]) == 0 && !isCovered {
			highValue := hasActionableEnable(factEnables) || factOpenLeads > 0 || strings.EqualFold(asText(fp["origin"]), "proposal")
			if (highValue && len(unconsumedHigh) < 12) || (!highValue && len(unconsumedOther) < 12) {
				item := map[string]any{
					"id": n.ID, "summary": asText(fp["summary"]), "polarity": pol,
					"confidence": asText(fp["confidence"]), "age_min": int(time.Since(n.CreatedAt).Minutes()),
				}
				if len(factEnables) > 0 {
					item["enables"] = factEnables
				}
				if factOpenLeads > 0 {
					item["open_leads"] = factOpenLeads
				}
				if d, ok := fp["detail"].(string); ok && strings.TrimSpace(d) != "" {
					item["detail_excerpt"] = FenceUntrusted("事实详情", clipLine(d, 200))
				}
				if highValue {
					unconsumedHigh = append(unconsumedHigh, item)
				} else {
					unconsumedOther = append(unconsumedOther, item)
				}
			}
		}
	}
	if fs, err := t.ts.ListByKindState(db.KindFinding, "ruled_out", 4); err == nil {
		for _, n := range fs {
			if len(ruled) >= 8 {
				break
			}
			var fp map[string]any
			_ = json.Unmarshal(n.Payload, &fp)
			ruled = append(ruled, map[string]any{"id": n.ID, "kind": "finding", "summary": clipLine(asText(fp["summary"]), 100)})
		}
	}
	unconsumed := unconsumedHigh
	if len(unconsumed) < 12 {
		rem := 12 - len(unconsumed)
		if rem > len(unconsumedOther) {
			rem = len(unconsumedOther)
		}
		unconsumed = append(unconsumed, unconsumedOther[:rem]...)
	}
	if len(branchReview) > 0 {
		out["branch_review_facts"] = branchReview
	}
	if len(unconsumed) > 0 {
		out["unconsumed_facts"] = unconsumed
	}
	if len(capCounts) > 0 {
		out["capabilities"] = capCounts
	}
	if len(ruled) > 0 {
		out["ruled_out"] = ruled
	}

	// Incremental events + cursor metadata.
	events := make([]map[string]any, 0, limit)
	hasMore, hasEarlier := false, false
	probe := limit + 1
	nextCursor := after
	eventsDeduped := false
	if nodes, err := t.ts.NodesChangedSince(after, probe); err == nil {
		if len(nodes) > limit {
			if after == 0 {
				nodes = nodes[1:]
				hasEarlier = true
			} else {
				nodes = nodes[:limit]
				hasMore = true
			}
		}
		if len(nodes) > 0 {
			nextCursor = nodes[len(nodes)-1].ID
		}
		if after == 0 && limit >= 10 && len(changes.Changes) > 0 {
			eventsDeduped = true
		} else {
			for _, n := range nodes {
				m := compactNode(n)
				m["kind"] = n.Kind
				if s, ok := m["summary"].(string); ok {
					m["summary"] = clipLine(s, 80)
				}
				events = append(events, m)
			}
		}
	}
	revision, _ := t.ts.MaxNodeID()
	out["totals"] = totals
	out["events"] = events
	bbMeta := map[string]any{
		"node_revision":      revision,
		"legacy_node_cursor": true,
		"cursor":             after,
		"has_more":           hasMore,
		"has_earlier":        hasEarlier,
		"next_cursor":        nextCursor,
		"incremental_delta":  true,
		"window_note":        "轻量增量黑板视图：汇总自变化游标以来的变更、最新统计与未接手/线索队列；每 5 轮自动执行完整有界对账。",
	}
	if eventsDeduped {
		bbMeta["events_omitted_in_favor_of_changes"] = true
	}
	out["blackboard"] = bbMeta
	out["changes"] = changes.Changes
	out["changes_status"] = changes.Status
	out["resync_required"] = false
	t.projectionContract(out)
	bbMeta["revision"] = out["revision"]
	out["change_cursor"] = changes.Cursor
	return out
}
