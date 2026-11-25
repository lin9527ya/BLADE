// 探索意图、图与节点查询。
// 从 server.go 拆出：纯搬运，零行为变化。
package server

import (
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/lin9527ya/blade/db"
)

func (s *Server) intents(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		// Back-compat: bare list shape when the task can't be resolved.
		writeJSON(w, 200, []any{})
		return
	}
	q := r.URL.Query()
	limit := min(atoiDefault(q.Get("limit"), 300), 500)
	// No paging params → preserve the legacy bare-array response so existing callers
	// (and the poll) keep working unchanged.
	if q.Get("before") == "" && q.Get("page") == "" {
		in, err := t.Store.ListByKind(db.KindIntent, limit)
		if err != nil {
			log.Printf("[intents] task=%s limit=%d: %v", t.ID, limit, err)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, taskNodeDTOs(in, s.credentialPlaintextEnabled()))
		return
	}
	// Paged form: ?before=<id> (or ?page as a marker) → {items, has_more} so the
	// worker session list can reach past the old fixed 300 boundary on scroll. It
	// also includes immutable intent results from directly related tasks; those
	// rows never participate in this task's frontier or claim path.
	before := int64(atoiDefault(q.Get("before"), 0))
	in, hasMore, err := taskIntentHistoryPage(t.Store, before, limit, false, 0)
	if err != nil {
		log.Printf("[intents] task=%s before=%d limit=%d: %v", t.ID, before, limit, err)
		writeErr(w, 500, err.Error())
		return
	}
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, source := range sources {
		items, sourceMore, sourceErr := taskIntentHistoryPage(source.Store, before, limit, true, source.Task.TaskID)
		if sourceErr != nil {
			writeErr(w, 500, sourceErr.Error())
			return
		}
		in = append(in, items...)
		hasMore = hasMore || sourceMore
	}
	sort.Slice(in, func(i, j int) bool { return in[i].ID > in[j].ID })
	if len(in) > limit {
		in = in[:limit]
		hasMore = true
	}
	writeJSON(w, 200, map[string]any{"items": taskNodeDTOs(in, s.credentialPlaintextEnabled()), "has_more": hasMore})
}

func inheritedIntentResult(state string) bool { return db.IntentStateTerminal(state) }

// inheritedGraphSnapshot exposes immutable source-task history without leaking
// work that is still open or running. Edges are filtered with the nodes so the
// response cannot contain dangling ids that reveal hidden live intent topology.
func inheritedGraphSnapshot(nodes []*db.Node, edges []db.Edge, sourceTaskID int64) ([]*db.Node, []db.Edge) {
	visible := make(map[int64]struct{}, len(nodes))
	filteredNodes := make([]*db.Node, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || (node.Kind == db.KindIntent && !inheritedIntentResult(node.State)) {
			continue
		}
		node.SourceTaskID = sourceTaskID
		node.Inherited = true
		visible[node.ID] = struct{}{}
		filteredNodes = append(filteredNodes, node)
	}
	filteredEdges := make([]db.Edge, 0, len(edges))
	for _, edge := range edges {
		if _, ok := visible[edge.From]; !ok {
			continue
		}
		if _, ok := visible[edge.To]; !ok {
			continue
		}
		filteredEdges = append(filteredEdges, edge)
	}
	return filteredNodes, filteredEdges
}

// taskIntentHistoryPage returns one newest-first page for an exploration.
// A source may have many open/running intents, so keep paging until enough
// historical results are collected instead of leaking its frontier into the UI.
func taskIntentHistoryPage(store *db.ExplorationStore, before int64, limit int, inherited bool, sourceTaskID int64) ([]*db.Node, bool, error) {
	if !inherited {
		return store.ListByKindPage(db.KindIntent, before, limit)
	}
	batch := max(limit, 300)
	cursor := before
	out := make([]*db.Node, 0, limit+1)
	for {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil {
			return nil, false, err
		}
		for _, node := range page {
			if !inheritedIntentResult(node.State) {
				continue
			}
			node.SourceTaskID = sourceTaskID
			node.Inherited = true
			out = append(out, node)
			if len(out) > limit {
				return out[:limit], true, nil
			}
		}
		if !more || len(page) == 0 {
			return out, false, nil
		}
		cursor = page[len(page)-1].ID
	}
}

// explorationGraph returns up to 2000 newest nodes (with deterministic edges +
// real total/truncated metadata) so large graphs do not silently drop recent discoveries.
func (s *Server) explorationGraph(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, map[string]any{"nodes": []any{}, "edges": []any{}, "total": 0, "truncated": false})
		return
	}
	nodes, total, err := t.Store.NodesNewest(2000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	truncated := int64(len(nodes)) < total
	if err := t.Store.PopulateEvidenceReadState(nodes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := t.Store.PopulateFindingStatuses(nodes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	nodeIDs := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			nodeIDs = append(nodeIDs, n.ID)
		}
	}
	edges, err := t.Store.EdgesForNodes(nodeIDs, 5000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sources, err := t.Store.DirectSourceStores()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, source := range sources {
		sourceNodes, srcTotal, nodeErr := source.Store.NodesNewest(2000)
		if nodeErr != nil {
			writeErr(w, 500, nodeErr.Error())
			return
		}
		total += srcTotal
		if int64(len(sourceNodes)) < srcTotal {
			truncated = true
		}
		srcIDs := make([]int64, 0, len(sourceNodes))
		for _, sn := range sourceNodes {
			if sn != nil {
				srcIDs = append(srcIDs, sn.ID)
			}
		}
		sourceEdges, edgeErr := source.Store.EdgesForNodes(srcIDs, 5000)
		if edgeErr != nil {
			writeErr(w, 500, edgeErr.Error())
			return
		}
		if pErr := source.Store.PopulateEvidenceReadState(sourceNodes); pErr != nil {
			writeErr(w, 500, pErr.Error())
			return
		}
		if pErr := source.Store.PopulateFindingStatuses(sourceNodes); pErr != nil {
			writeErr(w, 500, pErr.Error())
			return
		}
		sourceNodes, sourceEdges = inheritedGraphSnapshot(sourceNodes, sourceEdges, source.Task.TaskID)
		nodes = append(nodes, sourceNodes...)
		edges = append(edges, sourceEdges...)
	}
	// 这里保留完整图：探索链路的显示过滤在 GraphTab 做（只画 confirmed 漏洞），
	// 而时间线还要用这个接口按 id 打开候选/已排除节点，不能在 API 层删掉它们。
	writeJSON(w, 200, map[string]any{
		"nodes":     taskNodeDTOs(nodes, s.credentialPlaintextEnabled()),
		"edges":     edgeDTOs(edges),
		"total":     total,
		"truncated": truncated,
	})
}

// explorationNodes serves the 播报板: this task's own exploration nodes as a
// paged time series (newest first unless ?order=asc), filterable by kind/state
// and a payload substring. Inherited nodes are deliberately out of scope — the
// board reports what this task is doing right now, and paging across the source
// tasks' stores would make the cursor meaningless.
// Response also carries the edges touching the page plus the neighbour nodes
// they point at, so each row can state where it came from and what it produced.
func (s *Server) explorationNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	size := atoiDefault(q.Get("size"), 20)
	t := s.m.ResolveTask(q.Get("task"))
	if t == nil {
		writeJSON(w, 200, map[string]any{
			"items": []any{}, "total": 0, "page": page, "size": size,
			"edges": []any{}, "refs": map[string]any{},
		})
		return
	}
	states := csvValues(q.Get("state"))
	filter := db.NodeFilter{
		Kinds:  csvValues(q.Get("kind")),
		States: states,
		Query:  q.Get("q"),
		Asc:    q.Get("order") == "asc",
	}
	nodes, total, err := t.Store.NodesPage(filter, page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ids := make([]int64, 0, len(nodes))
	onPage := make(map[int64]bool, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
		onPage[n.ID] = true
	}
	edges, err := t.Store.EdgesTouching(ids)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	neighbourSet := map[int64]bool{}
	for _, e := range edges {
		if !onPage[e.From] {
			neighbourSet[e.From] = true
		}
		if !onPage[e.To] {
			neighbourSet[e.To] = true
		}
	}
	neighbourIDs := make([]int64, 0, len(neighbourSet))
	for id := range neighbourSet {
		neighbourIDs = append(neighbourIDs, id)
	}
	neighbours, err := t.Store.NodesByIDs(neighbourIDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := t.Store.PopulateEvidenceReadState(nodes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := t.Store.PopulateFindingStatuses(nodes); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := t.Store.PopulateEvidenceReadState(neighbours); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := t.Store.PopulateFindingStatuses(neighbours); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 播报板是时间线：pending / 候选 / 已排除都以 finding_status 徽标展示，
	// 默认不静默丢掉；点进发现页再做 triage。隐藏节点的边仍要丢掉。
	visible := make(map[int64]bool, len(nodes)+len(neighbours))
	for _, n := range nodes {
		visible[n.ID] = true
	}
	refs := make(map[string]TaskNodeDTO, len(neighbours))
	visibleIDs := append([]int64(nil), ids...)
	for _, n := range neighbours {
		refs[i64s(n.ID)] = taskNodeDTO(n, s.credentialPlaintextEnabled())
		visible[n.ID] = true
		visibleIDs = append(visibleIDs, n.ID)
	}
	visibleEdges := edges[:0]
	for _, e := range edges {
		if visible[e.From] && visible[e.To] {
			visibleEdges = append(visibleEdges, e)
		}
	}
	writeJSON(w, 200, map[string]any{
		"items":  taskNodeDTOs(nodes, s.credentialPlaintextEnabled()),
		"total":  total,
		"page":   page,
		"size":   size,
		"edges":  edgeDTOs(visibleEdges),
		"refs":   refs,
		"assets": s.nodeAnchoredAssets(t, visibleIDs),
	})
}

// nodeAnchoredAssets resolves the exploration_anchors of the given nodes into
// display-ready asset labels, keyed by node id. Anchors are provenance decoration
// for the 播报板 — a failure here must not cost the caller its page, so errors are
// logged and degrade to "no assets".
func (s *Server) nodeAnchoredAssets(t *Task, nodeIDs []int64) map[string][]FindingAssetDTO {
	out := map[string][]FindingAssetDTO{}
	anchors, err := t.Store.NodeAssets(nodeIDs)
	if err != nil {
		log.Printf("[broadcast] node assets: %v", err)
		return out
	}
	var flat []int64
	for _, ids := range anchors {
		flat = append(flat, ids...)
	}
	assets := s.resolveAssetIDs(flat)
	for nodeID, ids := range anchors {
		if dtos := findingAssetDTOs(ids, assets); len(dtos) > 0 {
			out[i64s(nodeID)] = dtos
		}
	}
	return out
}

// csvValues splits a comma-separated query parameter, dropping empty entries.
func csvValues(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// explorationNodeDetail returns a single exploration node (including inherited read-only sources)
// with its evidence_state and finding_status/finding_id populated for on-demand detail dialogs.
func (s *Server) explorationNodeDetail(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	nid := int64(atoiDefault(r.PathValue("nid"), 0))
	if nid <= 0 {
		writeErr(w, 400, "bad node id")
		return
	}
	n, err := t.Store.GetNodeWithSources(nid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if n == nil {
		writeErr(w, 404, "node not found")
		return
	}
	if n.Inherited && n.SourceTaskID > 0 {
		if src := s.m.ResolveTask(i64s(n.SourceTaskID)); src != nil {
			_ = src.Store.PopulateEvidenceReadState([]*db.Node{n})
			_ = src.Store.PopulateFindingStatuses([]*db.Node{n})
		}
	} else {
		_ = t.Store.PopulateEvidenceReadState([]*db.Node{n})
		_ = t.Store.PopulateFindingStatuses([]*db.Node{n})
	}
	writeJSON(w, 200, taskNodeDTO(n, s.credentialPlaintextEnabled()))
}
