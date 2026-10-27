package db

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
)

// task_worker_slots 固化“任务首次启动时的 worker 槽位数”。后续 intent 都在这 N 个
// 槽位内复用会话；改全局 workers 只影响之后首次启动的任务，不会让长任务中途换槽。
//
// 用独立小表而不是给 tasks 加列：tasks 的读取列很宽、分散在多处扫描，加列要同步改
// 很多查询；槽位只需要一个 (task_id, slots) 映射。

// EnsureTaskWorkerSlots 返回任务已固化的槽位数；没有则写入 fallback 并返回它。
// 并发首次启动用 ON CONFLICT DO UPDATE ... RETURNING 取同一个值。
func (d *DB) EnsureTaskWorkerSlots(taskID string, fallback int) (int, error) {
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("bad task id %q", taskID)
	}
	if fallback <= 0 {
		fallback = 1
	}
	var slots int
	if err := d.QueryRow(`
INSERT INTO task_worker_slots(task_id, slots) VALUES ($1,$2)
ON CONFLICT(task_id) DO UPDATE SET slots=task_worker_slots.slots
RETURNING slots`, id, fallback).Scan(&slots); err != nil {
		return 0, err
	}
	return slots, nil
}

// TaskWorkerSlots 读取任务固化的槽位数；没启动过返回 (0, nil)。
func (d *DB) TaskWorkerSlots(taskID string) (int, error) {
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || id <= 0 {
		return 0, nil
	}
	var slots int
	err = d.QueryRow(`SELECT slots FROM task_worker_slots WHERE task_id=$1`, id).Scan(&slots)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return slots, err
}

// backfillActivitySlots 给升级前的 activity 行补槽位归属（一次性，settings 旗标守卫）。
// 优先从 worker=work#N 解析；user/chat 行再按 node_id 找到 intent 的 owner。仍无
// work#N 归属的保持 NULL（旧历史仍可按 intent/节点详情查看）。
func (d *DB) backfillActivitySlots() error {
	const flag = "activity_slot_backfill_v1"
	if v, _, _ := d.GetSetting(flag); v == "done" {
		return nil
	}
	if _, err := d.Exec(`
UPDATE activity
SET slot_no = NULLIF(substring(worker from '^work#([0-9]+)$'), '')::int
WHERE slot_no IS NULL AND worker ~ '^work#[0-9]+$'`); err != nil {
		return fmt.Errorf("backfill slot from worker: %w", err)
	}
	if _, err := d.Exec(`
UPDATE activity a
SET slot_no = NULLIF(substring(n.owner from '^work#([0-9]+)$'), '')::int
FROM exploration_nodes n
WHERE a.node_id=n.id AND a.exploration_id=n.exploration_id AND a.slot_no IS NULL
  AND n.owner ~ '^work#[0-9]+$'`); err != nil {
		return fmt.Errorf("backfill slot from intent owner: %w", err)
	}
	if err := d.SetSetting(flag, "done"); err != nil {
		log.Printf("[migrate] activity slot backfill flag: %v", err)
	}
	return nil
}
