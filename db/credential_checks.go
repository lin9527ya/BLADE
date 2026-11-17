package db

import (
	"fmt"
	"strings"
	"time"
)

// 凭据验证/复核流水：一条凭据在一条目标上的多次检查是**事件**，不是可覆盖的字段。
// 节点 payload 的 targets 只保留合并后的最新视图；这里的记录用于时间线与复核。
// result='verified' 必须有 evidence；failed/locked/expired/unknown 各自独立，
// 失败不会清除历史 Verified，只更新 ValidationStatus。

// CredentialCheckResults 是允许的检查结果闭集（agent 工具与 API 共用）。
var CredentialCheckResults = []string{"verified", "failed", "locked", "expired", "unknown"}

// ValidCredentialCheckResult 校验结果值。
func ValidCredentialCheckResult(result string) bool {
	result = strings.ToLower(strings.TrimSpace(result))
	for _, v := range CredentialCheckResults {
		if result == v {
			return true
		}
	}
	return false
}

// CredentialCheckItem 是一条验证流水。
type CredentialCheckItem struct {
	ID         int64      `json:"id"`
	NodeID     int64      `json:"node_id"`
	Host       string     `json:"host"`
	Port       int        `json:"port"`
	Service    string     `json:"service"`
	Result     string     `json:"result"`
	Evidence   string     `json:"evidence,omitempty"`
	Note       string     `json:"note,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	Origin     string     `json:"origin"`
	Actor      string     `json:"actor,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// InsertCredentialCheck 写一条验证流水。observedAt/validUntil 为空串表示未知。
func (d *DB) InsertCredentialCheck(explorationID, nodeID int64, host string, port int, service, result, evidence, note, observedAt, validUntil, origin, actor string) (int64, error) {
	result = strings.ToLower(strings.TrimSpace(result))
	if !ValidCredentialCheckResult(result) {
		return 0, fmt.Errorf("result 必须是 %s 之一", strings.Join(CredentialCheckResults, "/"))
	}
	if strings.TrimSpace(host) == "" {
		return 0, fmt.Errorf("host is required")
	}
	if result != "unknown" && strings.TrimSpace(evidence) == "" {
		return 0, fmt.Errorf("result=%s 时必须给 evidence（实测命令与关键输出）", result)
	}
	var id int64
	err := d.QueryRow(`INSERT INTO credential_checks(
  exploration_id, node_id, host, port, service, result, evidence, note,
  observed_at, valid_until, origin, actor)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::timestamptz,NULLIF($10,'')::timestamptz,$11,$12)
RETURNING id`,
		explorationID, nodeID, strings.ToLower(strings.TrimSpace(host)), port, service,
		result, evidence, note, strings.TrimSpace(observedAt), strings.TrimSpace(validUntil), origin, actor).Scan(&id)
	return id, err
}

// ListCredentialChecks 返回一条凭据的检查时间线（最新在前）。
func (d *DB) ListCredentialChecks(nodeID int64, limit int) ([]CredentialCheckItem, error) {
	if nodeID <= 0 {
		return nil, fmt.Errorf("node id is required")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.Query(`SELECT id, node_id, host, port, service, result, evidence, note,
       observed_at, valid_until, origin, actor, created_at
FROM credential_checks WHERE node_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredentialCheckItem{}
	for rows.Next() {
		var it CredentialCheckItem
		var observed, valid sqlNullTime
		if err := rows.Scan(&it.ID, &it.NodeID, &it.Host, &it.Port, &it.Service, &it.Result,
			&it.Evidence, &it.Note, &observed, &valid, &it.Origin, &it.Actor, &it.CreatedAt); err != nil {
			return nil, err
		}
		it.ObservedAt = observed.ptr()
		it.ValidUntil = valid.ptr()
		out = append(out, it)
	}
	return out, rows.Err()
}

// sqlNullTime 是本文件内部的小包装，避免把 database/sql 的 NullTime 泄漏到 JSON 层。
type sqlNullTime struct {
	Time  time.Time
	Valid bool
}

func (n *sqlNullTime) Scan(v any) error {
	if v == nil {
		n.Valid = false
		return nil
	}
	switch t := v.(type) {
	case time.Time:
		n.Time, n.Valid = t, true
		return nil
	}
	return fmt.Errorf("unsupported time value %T", v)
}

func (n sqlNullTime) ptr() *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time
	return &t
}

// RecordCredentialCheck 是 ExplorationStore 的便捷包装（自动带本探索 id）。
func (s *ExplorationStore) RecordCredentialCheck(nodeID int64, host string, port int, service, result, evidence, note, observedAt, validUntil, origin, actor string) (int64, error) {
	return s.db.InsertCredentialCheck(s.expID, nodeID, host, port, service, result, evidence, note, observedAt, validUntil, origin, actor)
}

// CredentialChecks 读取本探索内一条凭据的检查时间线。
func (s *ExplorationStore) CredentialChecks(nodeID int64, limit int) ([]CredentialCheckItem, error) {
	return s.db.ListCredentialChecks(nodeID, limit)
}
