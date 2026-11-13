package db

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// GlobalNode is one broadcast row joined with the task that owns it.
// Node is the same compact node shape used by the per-task 播报板.
type GlobalNode struct {
	Node      *Node
	TaskID    int64
	TaskName  string
	TaskState string
	// SessionKey points at the worker/planner conversation that produced this
	// node when it can be derived from a yields edge. Empty means no exact
	// session is known (origin/goal or hand-written node).
	SessionKey string
}

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
func (d *DB) GlobalNodesPage(f NodeFilter, taskID int64, page, size int) ([]*GlobalNode, int, error) {
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
	if taskID > 0 {
		args = append(args, taskID)
		conds = append(conds, "t.id=$"+fmt.Sprint(len(args)))
	}
	if f.OnlyConfirmedFindings {
		conds = append(conds, `(n.kind <> 'finding' OR COALESCE((SELECT f.status FROM findings f WHERE f.node_id=n.id ORDER BY f.id DESC LIMIT 1), 'pending') IN (`+FindingConfirmedForDisplaySQL+`))`)
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
	if err := d.QueryRow(`SELECT COUNT(*)
FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "DESC"
	if f.Asc {
		order = "ASC"
	}
	args = append(args, size, (page-1)*size)
	rows, err := d.Query(`SELECT `+globalNodeCols+`,
		t.id, COALESCE(NULLIF(t.name,''), NULLIF(t.goal,''), t.description),
		CASE WHEN t.paused THEN 'paused' WHEN t.queued THEN 'queued' ELSE t.status END
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
	if len(ids) == 0 {
		return nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "$" + fmt.Sprint(i+1)
		args[i] = id
	}
	rs, err := d.Query(`SELECT e.dst_id, e.src_id
FROM exploration_edges e JOIN exploration_nodes i ON i.id=e.src_id AND i.kind='intent'
WHERE e.rel='yields' AND e.dst_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		var dst, src int64
		if err := rs.Scan(&dst, &src); err != nil {
			return err
		}
		if g := byID[dst]; g != nil && g.SessionKey == "" {
			g.SessionKey = "intent:" + strconv.FormatInt(src, 10)
		}
	}
	return rs.Err()
}
