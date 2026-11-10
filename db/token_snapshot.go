package db

import (
	"context"
	"encoding/json"
)

// TokenCounts 是不含会话归属的四项计量；输入已包含缓存读与写，不再另加缓存。
type TokenCounts struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type TokenFrame struct {
	TokenCounts
	Seq int64 `json:"seq"`
}

type SessionTokenSnapshot struct {
	SessionTokenUsage
	LastResultSeq int64       `json:"last_result_seq"`
	Live          *TokenFrame `json:"live,omitempty"`
}

type TokenSnapshot struct {
	Workers        []TokenUsage           `json:"workers"`
	Sessions       []SessionTokenSnapshot `json:"sessions"`
	Total          TokenCounts            `json:"total"`
	SnapshotCursor int64                  `json:"snapshot_cursor"`
}

// TokenSnapshot 在一条 SQL 的 MVCC 快照里读取总计、会话计量和最新未结算 usage。
// usage 是单次 run 的累计值：只取最后一条 result 后的最新帧，绝不能 SUM 所有 usage。
// 冷会话也返回 live，刷新页面不依赖打开每个会话的历史来恢复正在运行的用量。
func (s *ExplorationStore) TokenSnapshot(ctx context.Context) (TokenSnapshot, error) {
	var out TokenSnapshot
	var raw []byte
	err := s.db.QueryRowContext(ctx, `WITH accounting AS (
 SELECT a.id, COALESCE(a.worker,'') worker, a.kind,
 CASE WHEN a.worker='mainagent' THEN 'main:'||COALESCE(a.main_seg,0)::text
      WHEN a.worker='planner' THEN 'plan'
      WHEN a.worker IN ('system') THEN a.worker
      WHEN a.slot_no IS NOT NULL THEN 'slot:'||a.slot_no::text
      WHEN n.id IS NOT NULL THEN 'intent:'||n.id::text
      ELSE 'worker:'||COALESCE(a.worker,'') END AS session,
 COALESCE(a.input_tokens,0) i, COALESCE(a.output_tokens,0) o,
 COALESCE(a.cache_read_tokens,0) cr, COALESCE(a.cache_write_tokens,0) cw
 FROM activity a LEFT JOIN exploration_nodes n
 ON n.id=a.node_id AND n.exploration_id=a.exploration_id AND n.kind='intent'
 WHERE a.exploration_id=$1 AND a.kind IN ('result','usage')
), grouped AS (
 SELECT session, COALESCE(MAX(id) FILTER (WHERE kind='result'),0) last_result_seq,
 COALESCE(SUM(i) FILTER (WHERE kind='result'),0) i,
 COALESCE(SUM(o) FILTER (WHERE kind='result'),0) o,
 COALESCE(SUM(cr) FILTER (WHERE kind='result'),0) cr,
 COALESCE(SUM(cw) FILTER (WHERE kind='result'),0) cw
 FROM accounting GROUP BY session
), latest_usage AS (
 SELECT DISTINCT ON (session) * FROM accounting WHERE kind='usage' ORDER BY session,id DESC
), workers AS (
 SELECT worker,SUM(i) i,SUM(o) o,SUM(cr) cr,SUM(cw) cw
 FROM accounting WHERE kind='result' GROUP BY worker
)
SELECT json_build_object(
 'snapshot_cursor',COALESCE((SELECT MAX(id) FROM activity WHERE exploration_id=$1),0),
 'total',json_build_object('input_tokens',COALESCE((SELECT SUM(i) FROM grouped),0),
   'output_tokens',COALESCE((SELECT SUM(o) FROM grouped),0),
   'cache_read_tokens',COALESCE((SELECT SUM(cr) FROM grouped),0),
   'cache_write_tokens',COALESCE((SELECT SUM(cw) FROM grouped),0)),
 'workers',COALESCE((SELECT json_agg(json_build_object('worker',worker,'input_tokens',i,
   'output_tokens',o,'cache_read_tokens',cr,'cache_write_tokens',cw) ORDER BY worker) FROM workers),'[]'::json),
 'sessions',COALESCE((SELECT json_agg(json_build_object('session',g.session,
   'input_tokens',g.i,'output_tokens',g.o,'cache_read_tokens',g.cr,'cache_write_tokens',g.cw,
   'last_result_seq',g.last_result_seq,
   'live',CASE WHEN u.id>g.last_result_seq THEN json_build_object('seq',u.id,
     'input_tokens',u.i,'output_tokens',u.o,'cache_read_tokens',u.cr,'cache_write_tokens',u.cw) END)
   ORDER BY g.session) FROM grouped g LEFT JOIN latest_usage u ON u.session=g.session),'[]'::json))`, s.expID).Scan(&raw)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}
