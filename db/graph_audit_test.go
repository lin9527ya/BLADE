package db

import (
	"fmt"
	"testing"
	"time"
)

// auditFixture 建一个独立 exploration，并在用例结束时连根删掉（节点/边随之级联）。
func auditFixture(t *testing.T) (*DB, *ExplorationStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	desc := fmt.Sprintf("graph-audit-%d", time.Now().UnixNano())
	exp, err := d.CreateExploration(desc, "体检")
	if err != nil {
		t.Fatalf("create exploration: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, exp) })
	return d, d.Exploration(exp)
}

func auditCodes(rep *GraphAuditReport) map[string]int {
	out := map[string]int{}
	for _, is := range rep.Issues {
		out[is.Code]++
	}
	return out
}

// 干净的图不能报任何问题——体检最常见的失败方式是误报，让操作者学会忽略它。
func TestGraphAuditCleanGraphHasNoIssues(t *testing.T) {
	_, st := auditFixture(t)
	a, err := st.AddIntent(map[string]any{"summary": "方向 A"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.AddIntent(map[string]any{"summary": "方向 B"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(a, RelDependsOn, b); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AuditGraph()
	if err != nil {
		t.Fatalf("AuditGraph: %v", err)
	}
	if len(rep.Issues) != 0 {
		t.Fatalf("干净图报了问题：%v", rep.Issues)
	}
	if !rep.Clean() {
		t.Fatal("Clean() 应为 true")
	}
}

// 软删除前置后，下游永远等不到 done/negative——这正是体检存在的理由。
func TestGraphAuditDetectsDeletedPrerequisite(t *testing.T) {
	d, st := auditFixture(t)
	a, err := st.AddIntent(map[string]any{"summary": "下游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.AddIntent(map[string]any{"summary": "上游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(a, RelDependsOn, b); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='deleted' WHERE id=$1`, b); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AuditGraph()
	if err != nil {
		t.Fatal(err)
	}
	if got := auditCodes(rep)["deleted_prerequisite"]; got != 1 {
		t.Fatalf("应报 1 条 deleted_prerequisite，实际 %d（issues=%v）", got, rep.Issues)
	}
	if rep.Clean() {
		t.Fatal("error 级问题应让 Clean() 为 false")
	}
}

func TestGraphAuditDetectsDependencyCycle(t *testing.T) {
	_, st := auditFixture(t)
	a, err := st.AddIntent(map[string]any{"summary": "A"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.AddIntent(map[string]any{"summary": "B"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(a, RelDependsOn, b); err != nil {
		t.Fatal(err)
	}
	if err := st.Link(b, RelDependsOn, a); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AuditGraph()
	if err != nil {
		t.Fatal(err)
	}
	if got := auditCodes(rep)["dependency_cycle"]; got != 1 {
		t.Fatalf("应报 1 条 dependency_cycle，实际 %d（issues=%v）", got, rep.Issues)
	}
}

// digest 的 covers 指向已软删除节点：expand_digest 会取不到成员。
func TestGraphAuditDetectsDigestCoversDeleted(t *testing.T) {
	d, st := auditFixture(t)
	intent, err := st.AddIntent(map[string]any{"summary": "被折叠的方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	var digest int64
	if err := d.QueryRow(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,'digest','active','{"body":"摘要"}'::jsonb) RETURNING id`, st.expID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if err := st.Link(digest, "covers", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='deleted' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AuditGraph()
	if err != nil {
		t.Fatal(err)
	}
	if got := auditCodes(rep)["digest_covers_deleted"]; got != 1 {
		t.Fatalf("应报 1 条 digest_covers_deleted，实际 %d（issues=%v）", got, rep.Issues)
	}
}

// running 却已经过期/没有 owner：协调器没在跑时的兜底信号。
func TestGraphAuditDetectsExpiredLease(t *testing.T) {
	d, st := auditFixture(t)
	id, err := st.AddIntent(map[string]any{"summary": "僵尸方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes
SET state='running', owner='', lease_expires_at=now()-interval '1 hour' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AuditGraph()
	if err != nil {
		t.Fatal(err)
	}
	if got := auditCodes(rep)["expired_running_lease"]; got != 1 {
		t.Fatalf("应报 1 条 expired_running_lease，实际 %d（issues=%v）", got, rep.Issues)
	}
}

// 边的 exploration_id 与两端节点不一致：FK 不管这个，只有体检能发现。
func TestGraphAuditDetectsCrossExplorationEdge(t *testing.T) {
	d, st := auditFixture(t)
	mine, err := st.AddIntent(map[string]any{"summary": "本任务"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	otherExp, err := d.CreateExploration(fmt.Sprintf("graph-audit-other-%d", time.Now().UnixNano()), "别的任务")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, otherExp) })
	theirs, err := d.Exploration(otherExp).AddIntent(map[string]any{"summary": "别的任务的方向"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(mine, RelDependsOn, theirs); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AuditGraph()
	if err != nil {
		t.Fatal(err)
	}
	if got := auditCodes(rep)["cross_exploration_edge"]; got != 1 {
		t.Fatalf("应报 1 条 cross_exploration_edge，实际 %d（issues=%v）", got, rep.Issues)
	}
}

// 体检只读：跑一次不能改图。
func TestGraphAuditIsReadOnly(t *testing.T) {
	d, st := auditFixture(t)
	a, err := st.AddIntent(map[string]any{"summary": "A"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.AddIntent(map[string]any{"summary": "B"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(a, RelDependsOn, b); err != nil {
		t.Fatal(err)
	}
	before := graphFingerprint(t, d, st.expID)
	if _, err := st.AuditGraph(); err != nil {
		t.Fatal(err)
	}
	after := graphFingerprint(t, d, st.expID)
	if before != after {
		t.Fatalf("体检改动了图：before=%s after=%s", before, after)
	}
}

func graphFingerprint(t *testing.T, d *DB, expID int64) string {
	t.Helper()
	var nodes, edges int
	var maxUpdated string
	if err := d.QueryRow(`SELECT count(*), coalesce(max(updated_at)::text,'') FROM exploration_nodes WHERE exploration_id=$1`, expID).
		Scan(&nodes, &maxUpdated); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1`, expID).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("n=%d,e=%d,u=%s", nodes, edges, maxUpdated)
}
