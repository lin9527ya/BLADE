package db

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GlobalNode is one broadcast row joined with the task that owns it.
// Node is the same compact node shape used by the per-task 播报板.
type GlobalNode struct {
	Node      *Node
	TaskID    int64
	TaskName  string
	TaskState string
	// SessionKey points at the worker/planner conversation that produced this
	// node when it can be derived from the graph (or a safe origin fallback).
	// Empty means no exact session is known (for example a system-created fact).
	SessionKey string
}

// GlobalNodeFilter narrows the global broadcast feed. Unlike NodeFilter it also
// carries task-level filters and a time window; the per-task board keeps its
// smaller filter surface.
type GlobalNodeFilter struct {
	Kinds                 []string
	States                []string
	FindingStatuses       []string
	TaskStates            []string
	Query                 string
	Asc                   bool
	OnlyConfirmedFindings bool
	Since                 time.Time
	Until                 time.Time
	AfterID               int64 // SSE cursor: only nodes with id > AfterID
}

// globalTaskStateExpr projects the effective task state. tasks.status remains
// "running" while a task is manually paused (paused is a separate boolean), so
// reading status alone makes every paused task look alive on the board.
const globalTaskStateExpr = `CASE WHEN t.paused THEN 'paused' WHEN t.queued THEN 'queued' ELSE t.status END`

const globalNodeCols = `n.id, n.kind, n.payload, n.priority, n.state,
	COALESCE(n.origin,''), COALESCE(n.owner,''), COALESCE(n.blocked_reason,''),
	COALESCE(n.delete_reason,''), n.created_at, n.logical_session_id,
	n.execution_generation, n.content_version`

func scanGlobalNode(sc interface{ Scan(...any) error }) (*GlobalNode, error) {
	var g GlobalNode
	var n Node
	var payload []byte
	if err := sc.Scan(&n.ID, &n.Kind, &payload, &n.Priority, &n.State, &n.Origin, &n.Owner,
		&n.BlockedReason, &n.DeleteReason, &n.CreatedAt, &n.LogicalSessionID,
		&n.ExecutionGeneration, &n.ContentVersion, &g.TaskID, &g.TaskName, &g.TaskState); err != nil {
		return nil, err
	}
	n.Payload = json.RawMessage(payload)
	g.Node = &n
	return &g, nil
}

// GlobalNodesPage is the cross-task version of ExplorationStore.NodesPage.
// It intentionally returns only the node + task metadata (no edges/refs/assets):
// the global board is a wide feed, and per-row graph expansion belongs on the
// task detail page.
func (d *DB) GlobalNodesPage(f GlobalNodeFilter, taskID int64, page, size int) ([]*GlobalNode, int, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	conds := []string{"t.deleted_at IS NULL"}
	args := []any{}
	addIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		marks := make([]string, 0, len(values))
		for _, v := range values {
			args = append(args, v)
			marks = append(marks, "$"+fmt.Sprint(len(args)))
		}
		conds = append(conds, column+" IN ("+strings.Join(marks, ",")+")")
	}
	addIn("n.kind", f.Kinds)
	addIn("n.state", f.States)
	addIn(globalTaskStateExpr, f.TaskStates)
	if taskID > 0 {
		args = append(args, taskID)
		conds = append(conds, "t.id=$"+fmt.Sprint(len(args)))
	}
	if f.OnlyConfirmedFindings {
		conds = append(conds, `(n.kind <> 'finding' OR COALESCE((SELECT f.status FROM findings f WHERE f.node_id=n.id ORDER BY f.id DESC LIMIT 1), 'pending') IN (`+FindingConfirmedForDisplaySQL+`))`)
	}
	if len(f.FindingStatuses) > 0 {
		marks := make([]string, 0, len(f.FindingStatuses))
		for _, status := range f.FindingStatuses {
			args = append(args, status)
			marks = append(marks, "$"+fmt.Sprint(len(args)))
		}
		conds = append(conds, `n.kind='finding' AND COALESCE((SELECT f.status FROM findings f WHERE f.node_id=n.id ORDER BY f.id DESC LIMIT 1), 'pending') IN (`+strings.Join(marks, ",")+`)`)
	}
	if !f.Since.IsZero() {
		args = append(args, f.Since)
		conds = append(conds, "n.created_at >= $"+fmt.Sprint(len(args)))
	}
	if !f.Until.IsZero() {
		args = append(args, f.Until)
		conds = append(conds, "n.created_at < $"+fmt.Sprint(len(args)))
	}
	if f.AfterID > 0 {
		args = append(args, f.AfterID)
		conds = append(conds, "n.id > $"+fmt.Sprint(len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+q+"%")
		mark := "$" + fmt.Sprint(len(args))
		ors := []string{"n.payload::text ILIKE " + mark, "COALESCE(n.origin,'') ILIKE " + mark}
		if idStr := strings.TrimPrefix(q, "#"); idStr != "" {
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				args = append(args, id)
				ors = append(ors, "n.id = $"+fmt.Sprint(len(args)))
			}
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int
	// SSE tails with AfterID and only consumes rows; skip the count there so the
	// 2s stream query does not scan the whole feed just to discard a total.
	if f.AfterID == 0 {
		if err := d.QueryRow(`SELECT COUNT(*)
FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id`+where, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	order := "DESC"
	if f.Asc {
		order = "ASC"
	}
	args = append(args, size, (page-1)*size)
	rows, err := d.Query(`SELECT `+globalNodeCols+`,
		t.id, COALESCE(NULLIF(t.name,''), NULLIF(t.goal,''), t.description),
		`+globalTaskStateExpr+`
FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id`+where+
		` ORDER BY n.id `+order+` LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]*GlobalNode, 0, size)
	for rows.Next() {
		g, err := scanGlobalNode(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := d.populateGlobalFindingStatuses(out); err != nil {
		return nil, 0, err
	}
	if err := d.populateGlobalSessionKeys(out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GlobalLatestNodeID is the cursor baseline for a fresh global SSE connection.
// Deleted tasks are excluded so the stream and the paged board share a scope.
func (d *DB) GlobalLatestNodeID() (int64, error) {
	var id int64
	err := d.QueryRow(`SELECT COALESCE(MAX(n.id), 0)
FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id
WHERE t.deleted_at IS NULL`).Scan(&id)
	return id, err
}

func (d *DB) populateGlobalFindingStatuses(rows []*GlobalNode) error {
	var ids []int64
	nodes := map[int64]*Node{}
	for _, g := range rows {
		if g.Node != nil && g.Node.Kind == KindFinding {
			ids = append(ids, g.Node.ID)
			nodes[g.Node.ID] = g.Node
		}
	}
	if len(ids) == 0 {
		return nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "$" + fmt.Sprint(i+1)
		args[i] = id
	}
	rs, err := d.Query(`SELECT node_id, COALESCE(NULLIF(status,''),'pending') FROM findings
WHERE node_id IN (`+strings.Join(marks, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		var id int64
		var status string
		if err := rs.Scan(&id, &status); err != nil {
			return err
		}
		if n := nodes[id]; n != nil {
			n.FindingStatus = status
		}
	}
	return rs.Err()
}

// populateGlobalSessionKeys maps each row to the worker conversation that can
// show its origin. It follows incoming graph edges recursively (yields is the
// common direct case; proves/derived_from chains are covered too), then falls
// back to origin-based sessions for nodes the graph does not attribute
// (planner goals, human hints, compactor digests, the task origin fact).
func (d *DB) populateGlobalSessionKeys(rows []*GlobalNode) error {
	var ids []int64
	byID := map[int64]*GlobalNode{}
	for _, g := range rows {
		if g.Node == nil {
			continue
		}
		if g.Node.Kind == KindIntent {
			g.SessionKey = "intent:" + strconv.FormatInt(g.Node.ID, 10)
			continue
		}
		ids = append(ids, g.Node.ID)
		byID[g.Node.ID] = g
	}
	if len(ids) > 0 {
		marks := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			marks[i] = "$" + fmt.Sprint(i+1)
			args[i] = id
		}
		// Walk upstream (dst -> src) until an intent is reached. Depth 8 covers
		// fact -> fact -> intent and finding -> goal -> intent chains without
		// letting a pathological graph walk forever. The path array prevents
		// cycles; ORDER BY depth/node_id makes the nearest intent win.
		rs, err := d.Query(`
WITH RECURSIVE up(start_id, node_id, depth, path) AS (
  SELECT n.id, n.id, 0, ARRAY[n.id]
  FROM exploration_nodes n
  WHERE n.id IN (`+strings.Join(marks, ",")+`)
  UNION ALL
  SELECT up.start_id, e.src_id, up.depth + 1, up.path || e.src_id
  FROM up
  JOIN exploration_edges e ON e.dst_id = up.node_id
  WHERE up.depth < 8 AND NOT (e.src_id = ANY(up.path))
)
SELECT up.start_id, up.node_id
FROM up
JOIN exploration_nodes i ON i.id = up.node_id AND i.kind='intent'
WHERE up.depth > 0
ORDER BY up.start_id, up.depth, up.node_id`, args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var startID, intentID int64
			if err := rs.Scan(&startID, &intentID); err != nil {
				return err
			}
			if g := byID[startID]; g != nil && g.SessionKey == "" {
				g.SessionKey = "intent:" + strconv.FormatInt(intentID, 10)
			}
		}
		if err := rs.Err(); err != nil {
			return err
		}
	}
	for _, g := range rows {
		if g.Node == nil || g.SessionKey != "" {
			continue
		}
		g.SessionKey = globalOriginSessionKey(g.Node)
	}
	return nil
}

func globalOriginSessionKey(n *Node) string {
	if n.Kind == KindFact && n.State == "origin" {
		return "main:0"
	}
	switch strings.ToLower(strings.TrimSpace(n.Origin)) {
	case "planner", "goals":
		return "plan"
	case "human", "user", "chat", "mainagent", "compactor":
		return "main:0"
	}
	return ""
}
