package db

import (
	"strings"
	"testing"
)

// TestLootKindsReachPreExistingInstall 钉住「重启即迁移」这条路上的战果 kind。
//
// 新库走 schema.sql 里内联的 CREATE TABLE，自然带上 foothold/credential；**老库只能靠
// 那两个 DO $$ 块**把 ck_node_kind / ck_node_state 重建成含新 kind 的版本。这条路径只在
// 升级时走，日常测试碰不到——而它一旦失效，表现是老库上 report_foothold 直接写入被 CHECK
// 拦下，且只有真的从旧版本升上来的人才会遇到，本地开发永远复现不了。
//
// 做法：先把两个约束**降级回战果之前的定义**（等价于一个老库），再用 Open 重放一次迁移
// （与进程启动同一条路径），最后真的插入据点与凭据节点——约束没重建的话插入会被拦下。
func TestLootKindsReachPreExistingInstall(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	// 先清掉可能残留的战果/检测行（上一轮失败留下的）。不清的话，「降级回旧约束」这一步会以
	// check constraint is violated by some row 失败——那条信息离真正的原因很远，很容易
	// 被误读成"迁移块没生效"。本用例要能重复跑，这一步是前提。
	if _, err := d.Exec(`DELETE FROM exploration_nodes WHERE kind IN ($1, $2, $3)`, KindFoothold, KindCredential, KindDetection); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restored, err := Open(testDSN(t)); err == nil {
			_, _ = restored.Exec(`DELETE FROM exploration_nodes WHERE kind IN ($1, $2, $3)`, KindFoothold, KindCredential, KindDetection)
			restored.Close()
		}
	})

	// 战果之前那一版的约束定义（逐字抄自 schema.sql 的历史版本）。
	const oldKind = `CHECK (kind IN ('begin','goal','intent','fact','finding','hint','digest'))`
	const oldState = `CHECK (
            (kind='begin'   AND state IN ('open')) OR
            (kind='intent'  AND state IN ('open','running','paused','done','negative','out-of-scope','blocked','exhausted','stopped','deleted')) OR
            (kind='goal'    AND state IN ('open','met','abandoned')) OR
            (kind='fact'    AND state IN ('confirmed','dismissed','origin')) OR
            (kind='finding' AND state IN ('confirmed','dismissed','ruled_out','open_proof_gap')) OR
            (kind='hint'    AND state IN ('active','consumed')) OR
            (kind='digest'  AND state IN ('active','superseded'))
        )`
	// 战果（P27）引入 'uses' 时，边关系表也要一起放开——同一个坑在两张表上各有一份。
	const oldEdgeRel = `CHECK (rel IN ('spawns','derived_from','yields','proves','covers'))`
	for _, c := range []struct{ table, name, def string }{
		{"exploration_nodes", "ck_node_kind", oldKind},
		{"exploration_nodes", "ck_node_state", oldState},
		{"exploration_edges", "ck_edge_rel", oldEdgeRel},
	} {
		if _, err := d.Exec(`ALTER TABLE ` + c.table + ` DROP CONSTRAINT ` + c.name); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`ALTER TABLE ` + c.table + ` ADD CONSTRAINT ` + c.name + " " + c.def); err != nil {
			t.Fatal(err)
		}
	}
	// 重放启动迁移：新二进制起在一个老库上，走的正是这条路。
	d2, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("重放迁移失败: %v", err)
	}
	defer d2.Close()

	var def string
	if err := d2.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
WHERE conrelid='exploration_nodes'::regclass AND conname='ck_node_kind'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "foothold") || !strings.Contains(def, "credential") || !strings.Contains(def, "detection") {
		t.Fatalf("老库的 ck_node_kind 没被重建：%s", def)
	}
	var edgeDef string
	if err := d2.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
WHERE conrelid='exploration_edges'::regclass AND conname='ck_edge_rel'`).Scan(&edgeDef); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(edgeDef, "uses") {
		t.Fatalf("老库的 ck_edge_rel 没被重建：%s", edgeDef)
	}

	// 真插一条：kind 重建了而 state 分支漏掉的话，这里会被 ck_node_state 拦下。
	task, err := d2.CreateTaskWithOptions("loot migration", "老库升级", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d2.DeleteTask(task.ID) })
	ts := d2.Exploration(task.ExplorationID)
	lootIDs := make([]int64, 0, 2)
	for _, c := range []struct{ kind, state string }{
		{KindFoothold, StateLootActive},
		{KindCredential, StateLootActive},
		// 检测三态都插一遍：kind 重建了而 state 少了某一支的话，这里会被 ck_node_state 拦下。
		{KindDetection, StateDetectionTriggered},
		{KindDetection, StateDetectionNotTriggered},
		{KindDetection, StateDetectionUnknown},
	} {
		id, err := ts.AddNode(c.kind, map[string]any{"summary": "x"}, 8, c.state, "work#1", nil)
		if err != nil {
			t.Fatalf("升级后的库仍写不进 %s/%s 节点：%v", c.kind, c.state, err)
		}
		lootIDs = append(lootIDs, id)
	}

	// 真落一条 uses 边：边关系没重建的话，这里会被 ck_edge_rel 拦下。
	intent, err := ts.AddIntent(map[string]any{"summary": "动用战果"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(intent, RelUses, lootIDs[0]); err != nil {
		t.Fatalf("升级后的库仍写不进 uses 边：%v", err)
	}
}
