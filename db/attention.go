package db

import "time"

// AttentionFact is a small recall-window row for planner attention. It is not a
// full node: the planner already has node_detail for the content.
type AttentionFact struct {
	ID         int64
	State      string
	Summary    string
	Polarity   string
	Confidence string
	CreatedAt  time.Time
}

// AttentionUnresolved is an old intent/finding that still needs a decision.
type AttentionUnresolved struct {
	ID        int64
	Kind      string
	State     string
	Summary   string
	CreatedAt time.Time
}

// OldestUnconsumedFacts returns the oldest facts that no intent has derived from.
// The normal overview windows are newest-first, so a fact that fell behind the
// window can otherwise never be re-surfaced. Limit is clamped to a small number.
func (s *ExplorationStore) OldestUnconsumedFacts(limit int) ([]AttentionFact, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT n.id, n.state,
       COALESCE(n.payload->>'summary',''), COALESCE(n.payload->>'polarity',''), COALESCE(n.payload->>'confidence',''),
       n.created_at
FROM exploration_nodes n
WHERE n.exploration_id=$1 AND n.kind='fact' AND n.state NOT IN ('origin','dismissed')
  AND COALESCE(n.payload->>'polarity','') <> 'negative'
  AND NOT EXISTS (
    SELECT 1 FROM exploration_edges e
    JOIN exploration_nodes i ON i.id=e.dst_id AND i.exploration_id=e.exploration_id
    WHERE e.exploration_id=$1 AND e.src_id=n.id AND e.rel='derived_from' AND i.kind='intent'
  )
ORDER BY n.id ASC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttentionFact{}
	for rows.Next() {
		var f AttentionFact
		if err := rows.Scan(&f.ID, &f.State, &f.Summary, &f.Polarity, &f.Confidence, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// OldestUnresolved returns the oldest blocked/exhausted/no-output intents and
// open-proof-gap findings. These are decisions the planner may have missed when
// they fell out of the recent windows.
func (s *ExplorationStore) OldestUnresolved(limit int) ([]AttentionUnresolved, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, kind, state, summary, created_at FROM (
  (SELECT id, kind, state, COALESCE(payload->>'summary','') AS summary, created_at
   FROM exploration_nodes
   WHERE exploration_id=$1 AND kind='intent' AND state IN ('blocked','exhausted','no_output')
   ORDER BY id ASC LIMIT $2)
  UNION ALL
  (SELECT id, kind, state, COALESCE(payload->>'summary',''), created_at
   FROM exploration_nodes
   WHERE exploration_id=$1 AND kind='finding' AND state='open_proof_gap'
   ORDER BY id ASC LIMIT $2)
) t ORDER BY id ASC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttentionUnresolved{}
	for rows.Next() {
		var u AttentionUnresolved
		if err := rows.Scan(&u.ID, &u.Kind, &u.State, &u.Summary, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
