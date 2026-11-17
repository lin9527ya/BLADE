package db

import (
	"database/sql"
)

// FindOpenFollowUpIntent 返回同一来源节点、同一描述、仍处于 open 的人工意图。
// 用于 UI 双击/重复提交时的幂等：不重复造意图，也不构成任何队列上限。
func (s *ExplorationStore) FindOpenFollowUpIntent(sourceNodeID int64, summary string) (int64, error) {
	if sourceNodeID <= 0 || summary == "" {
		return 0, nil
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state='open'
  AND payload->>'source_node_id'=$2 AND payload->>'summary'=$3
ORDER BY id LIMIT 1`, s.expID, itoa(sourceNodeID), summary).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}
