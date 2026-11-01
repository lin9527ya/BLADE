package db

import (
	"fmt"
	"sort"
	"time"
)

// 图完整性体检（只读）。
//
// 目的不是"修数据"，而是把一类**没有任何信号**的坏状态显性化：`depends_on` 指向一个
// 已软删除的意图时，下游会永远停在"等前置"，外部看到的现象只是"规划者不再派活"；
// 依赖成环同理。这些状态靠引擎自己发现不了——租约看门狗只处理 running 的意图。
//
// 纪律：**只读**。任何修复动作都是策略决定（重派前置 / unblock_intent / 删意图），
// 不能由体检替人做。启动恢复时只记日志、不阻断。
const graphAuditIssueLimit = 50

// GraphIssue 是一条体检结论。Code 稳定、可被 UI/告警按码聚合；NodeIDs 是涉及的节点。
type GraphIssue struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"` // error = 图语义已经坏了；warn = 可能是正常中间态
	NodeIDs  []int64 `json:"node_ids,omitempty"`
	From     int64   `json:"from,omitempty"`
	Rel      string  `json:"rel,omitempty"`
	To       int64   `json:"to,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}

// GraphAuditReport 是一次体检的完整结论。Counts 按 Code 汇总（含被 LIMIT 截断的部分
// 只统计到上限，报告里明说）。
type GraphAuditReport struct {
	ExplorationID int64           `json:"exploration_id"`
	CheckedAt     time.Time       `json:"checked_at"`
	Counts        map[string]int  `json:"counts"`
	Issues        []GraphIssue    `json:"issues"`
	Truncated     map[string]bool `json:"truncated,omitempty"`
}

// Clean 报告"没有 error 级问题"；warn 不影响它（warn 多为正常中间态）。
func (r *GraphAuditReport) Clean() bool {
	if r == nil {
		return true
	}
	for _, is := range r.Issues {
		if is.Severity == "error" {
			return false
		}
	}
	return true
}

// Summary 是一行式摘要，给启动日志用。
func (r *GraphAuditReport) Summary() string {
	if r == nil || len(r.Issues) == 0 {
		return "无问题"
	}
	parts := make([]string, 0, len(r.Counts))
	for code, n := range r.Counts {
		parts = append(parts, fmt.Sprintf("%s=%d", code, n))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%d 条问题（%v）", len(r.Issues), parts)
}

// allowedStatesByKind 镜像 db/schema.sql 的 ck_node_state。schema 是权威，这张表只用来
// 发现"代码写入了 schema 之外的状态"——两者漂移时体检会报非法状态，提醒改约束或改代码。
var allowedStatesByKind = map[string]map[string]bool{
	KindBegin:       {"open": true},
	KindIntent:      intentStates,
	KindGoal:        {"open": true, "met": true, "abandoned": true},
	KindFact:        {"confirmed": true, "dismissed": true, "origin": true},
	KindFinding:     {"confirmed": true, "dismissed": true, "ruled_out": true, "open_proof_gap": true},
	KindHint:        {"active": true, "consumed": true},
	KindDigest:      {"active": true, "superseded": true},
	KindFoothold:    {StateLootActive: true, "lost": true},
	KindCredential:  {StateLootActive: true, "lost": true},
	KindDetection:   {"triggered": true, "not_triggered": true, "unknown": true},
	KindArtifact:    {"active": true, "removed": true, "unknown": true},
	KindFootprint:   {"open": true, "unknown": true, "accepted": true, "cleaned": true, "not_found": true},
	KindADObject:    {StateADActive: true},
	KindCloudObject: {StateCloudActive: true},
}

var intentStates = map[string]bool{
	"open": true, "running": true, "paused": true, "done": true, "negative": true,
	"out-of-scope": true, "blocked": true, "exhausted": true, "stopped": true,
	"no_output": true, "deleted": true,
}

// AuditGraph 对当前 exploration 做一次只读体检。
func (s *ExplorationStore) AuditGraph() (*GraphAuditReport, error) {
	rep := &GraphAuditReport{
		ExplorationID: s.expID,
		CheckedAt:     time.Now(),
		Counts:        map[string]int{},
		Truncated:     map[string]bool{},
	}
	add := func(is GraphIssue) {
		rep.Counts[is.Code]++
		// 每类问题封顶，避免一个坏环把报告撑爆；总数在 Counts 里如实记。
		if rep.Counts[is.Code] <= graphAuditIssueLimit {
			rep.Issues = append(rep.Issues, is)
		} else {
			rep.Truncated[is.Code] = true
		}
	}

	// ⓪ 边的两端不属于本 exploration：FK 只约束节点存在，不约束两端的 exploration_id，
	//    跨任务写错一条边会让"别人的图"混进本任务的依赖判断。
	if err := s.auditCrossExplorationEdges(add); err != nil {
		return nil, err
	}

	// ① depends_on 指向已软删除的意图：边不会被 FK 级联删（软删只改 state），
	//    下游因此永远等不到「done/negative」。
	if err := s.eachEdge("depends_on", `d.state='deleted'`, func(src, dst int64) {
		add(GraphIssue{Code: "deleted_prerequisite", Severity: "error", From: src, Rel: "depends_on", To: dst,
			Detail: "前置意图已被软删除；下游永远不会 ready，需重派前置或用 unblock_intent 拆链"})
	}); err != nil {
		return nil, err
	}

	// ② depends_on 指向非意图节点。
	if err := s.eachEdge("depends_on", `d.kind<>'intent'`, func(src, dst int64) {
		add(GraphIssue{Code: "dependency_not_intent", Severity: "error", From: src, Rel: "depends_on", To: dst,
			Detail: "depends_on 的终点不是意图节点"})
	}); err != nil {
		return nil, err
	}

	// ③ 依赖成环（自环有 ck_edge_noself，多节点环没有约束）。
	if err := s.auditDependencyCycles(add); err != nil {
		return nil, err
	}

	// ④ digest 的 covers 指向已软删除的节点。
	if err := s.eachEdge("covers", `d.state='deleted'`, func(src, dst int64) {
		add(GraphIssue{Code: "digest_covers_deleted", Severity: "warn", From: src, Rel: "covers", To: dst,
			Detail: "摘要节点覆盖了一个已删除的成员；expand_digest 会取到缺失项"})
	}); err != nil {
		return nil, err
	}

	// ⑤ 前置已全终态、却长时间没被领取的 open 意图（饥饿）。
	if err := s.auditStarvedIntents(add); err != nil {
		return nil, err
	}

	// ⑥ running 意图租约过期/无 owner：协调器没在跑时的兜底信号（看门狗正常时不会出现）。
	if err := s.auditExpiredLeases(add); err != nil {
		return nil, err
	}

	// ⑦ kind/state 不在 schema 允许集内（代码与 ck_node_state 漂移）。
	if err := s.auditIllegalStates(add); err != nil {
		return nil, err
	}

	return rep, nil
}

// auditCrossExplorationEdges：边的任一端缺失或属于另一个 exploration。
func (s *ExplorationStore) auditCrossExplorationEdges(add func(GraphIssue)) error {
	const q = `SELECT e.src_id, e.dst_id FROM exploration_edges e
LEFT JOIN exploration_nodes s ON s.id=e.src_id
LEFT JOIN exploration_nodes d ON d.id=e.dst_id
WHERE e.exploration_id=$1
  AND (s.id IS NULL OR d.id IS NULL OR s.exploration_id<>e.exploration_id OR d.exploration_id<>e.exploration_id)
ORDER BY e.src_id LIMIT $2`
	rows, err := s.db.Query(q, s.expID, graphAuditIssueLimit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var src, dst int64
		if err := rows.Scan(&src, &dst); err != nil {
			return err
		}
		add(GraphIssue{Code: "cross_exploration_edge", Severity: "error", From: src, Rel: "?", To: dst,
			Detail: "边的两端不在同一个 exploration 内（或节点已不存在）"})
	}
	return rows.Err()
}

// eachEdge 遍历某类关系里满足条件的边（每类最多 graphAuditIssueLimit 条）。
func (s *ExplorationStore) eachEdge(rel, cond string, fn func(src, dst int64)) error {
	q := `SELECT e.src_id, e.dst_id FROM exploration_edges e
JOIN exploration_nodes d ON d.id=e.dst_id AND d.exploration_id=e.exploration_id
WHERE e.exploration_id=$1 AND e.rel=$2 AND ` + cond + `
ORDER BY e.src_id LIMIT $3`
	rows, err := s.db.Query(q, s.expID, rel, graphAuditIssueLimit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var src, dst int64
		if err := rows.Scan(&src, &dst); err != nil {
			return err
		}
		fn(src, dst)
	}
	return rows.Err()
}

// auditDependencyCycles 用 DFS 找依赖环。只加载依赖边，图规模受单任务限制。
func (s *ExplorationStore) auditDependencyCycles(add func(GraphIssue)) error {
	rows, err := s.db.Query(`SELECT src_id, dst_id FROM exploration_edges WHERE exploration_id=$1 AND rel='depends_on'`, s.expID)
	if err != nil {
		return err
	}
	defer rows.Close()
	adj := map[int64][]int64{}
	for rows.Next() {
		var src, dst int64
		if err := rows.Scan(&src, &dst); err != nil {
			return err
		}
		adj[src] = append(adj[src], dst)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[int64]int{}
	var stack []int64
	var walk func(n int64) bool
	walk = func(n int64) bool {
		color[n] = gray
		stack = append(stack, n)
		for _, next := range adj[n] {
			switch color[next] {
			case gray:
				// 找到环：从 next 在栈里的位置到栈顶就是环上的节点。
				cycle := []int64{next}
				for i := len(stack) - 1; i >= 0 && stack[i] != next; i-- {
					cycle = append(cycle, stack[i])
				}
				sort.Slice(cycle, func(i, j int) bool { return cycle[i] < cycle[j] })
				add(GraphIssue{Code: "dependency_cycle", Severity: "error", NodeIDs: cycle,
					Detail: "depends_on 成环：环上的意图永远不会 ready"})
				return true
			case white:
				if walk(next) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	ids := make([]int64, 0, len(adj))
	for id := range adj {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if color[id] == white {
			if walk(id) {
				break // 报一个环就够；同一个 bug 往往会产生一串
			}
		}
	}
	return nil
}

// auditStarvedIntents：open、无 owner、前置已全终态，却超过 2×租约 TTL 仍没被领取。
func (s *ExplorationStore) auditStarvedIntents(add func(GraphIssue)) error {
	const q = `SELECT n.id FROM exploration_nodes n
WHERE n.exploration_id=$1 AND n.kind='intent' AND n.state='open' AND coalesce(n.owner,'')=''
  AND n.created_at < now() - make_interval(secs => $2)
  AND EXISTS (SELECT 1 FROM exploration_edges e WHERE e.exploration_id=n.exploration_id AND e.src_id=n.id AND e.rel='depends_on')
  AND NOT EXISTS (
      SELECT 1 FROM exploration_edges e JOIN exploration_nodes d ON d.id=e.dst_id AND d.exploration_id=e.exploration_id
      WHERE e.exploration_id=n.exploration_id AND e.src_id=n.id AND e.rel='depends_on'
        AND d.state NOT IN ('done','negative'))
ORDER BY n.id LIMIT $3`
	rows, err := s.db.Query(q, s.expID, int64((2 * IntentLeaseTTL).Seconds()), graphAuditIssueLimit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		add(GraphIssue{Code: "starved_open_intent", Severity: "warn", NodeIDs: []int64{id},
			Detail: "前置已全部收场，但这条意图长时间没被领取；检查 worker 槽位/队列是否卡住"})
	}
	return rows.Err()
}

// auditExpiredLeases：running 但租约已过期或没有 owner。
func (s *ExplorationStore) auditExpiredLeases(add func(GraphIssue)) error {
	const q = `SELECT id FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state='running'
  AND (coalesce(owner,'')='' OR lease_expires_at IS NULL OR lease_expires_at < now())
ORDER BY id LIMIT $2`
	rows, err := s.db.Query(q, s.expID, graphAuditIssueLimit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		add(GraphIssue{Code: "expired_running_lease", Severity: "warn", NodeIDs: []int64{id},
			Detail: "running 意图的租约已过期；看门狗在跑时应由它回收，持续出现说明协调器没在工作"})
	}
	return rows.Err()
}

// auditIllegalStates：kind/state 组合不在 schema 允许集内。
func (s *ExplorationStore) auditIllegalStates(add func(GraphIssue)) error {
	rows, err := s.db.Query(`SELECT DISTINCT kind, state FROM exploration_nodes WHERE exploration_id=$1`, s.expID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pair struct{ kind, state string }
	var bad []pair
	for rows.Next() {
		var k, st string
		if err := rows.Scan(&k, &st); err != nil {
			return err
		}
		allowed, known := allowedStatesByKind[k]
		if !known || !allowed[st] {
			bad = append(bad, pair{k, st})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(bad, func(i, j int) bool {
		if bad[i].kind != bad[j].kind {
			return bad[i].kind < bad[j].kind
		}
		return bad[i].state < bad[j].state
	})
	for _, p := range bad {
		add(GraphIssue{Code: "illegal_state", Severity: "error",
			Detail: fmt.Sprintf("%s 节点出现了 ck_node_state 不允许的状态 %q（代码与 schema 漂移）", p.kind, p.state)})
	}
	return nil
}
