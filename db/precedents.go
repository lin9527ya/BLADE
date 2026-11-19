package db

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Precedent is a compact cross-task memory hit: a fact/finding from another
// exploration that may be useful as a prior for the current task. It is
// deliberately read-only and carries provenance (task/node) so the planner can
// inspect the original before trusting it.
type Precedent struct {
	TaskID    int64     `json:"task_id"`
	TaskName  string    `json:"task_name,omitempty"`
	NodeID    int64     `json:"node_id"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	Summary   string    `json:"summary"`
	Evidence  string    `json:"evidence,omitempty"`
	Score     float64   `json:"score,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SearchPrecedents searches facts/findings across other tasks by keyword. When
// the current task has company scopes, the search is restricted to tasks with
// those companies; otherwise it is a global keyword search. Results are capped
// and newest-first; callers must inspect evidence before using them.
func (s *ExplorationStore) SearchPrecedents(query string, limit int) ([]Precedent, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Precedent{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	fetchLimit := limit * 5
	if fetchLimit < 100 {
		fetchLimit = 100
	}
	if fetchLimit > 500 {
		fetchLimit = 500
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	pattern := "%" + escaped + "%"

	rows, err := s.db.Query(`
SELECT DISTINCT ts.company_id FROM task_scope ts
JOIN tasks t ON t.id=ts.task_id
WHERE t.exploration_id=$1 AND ts.kind='company' AND ts.company_id IS NOT NULL`, s.expID)
	if err != nil {
		return nil, err
	}
	var companies []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		companies = append(companies, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	args := []any{pattern}
	companyFilter := ""
	if len(companies) > 0 {
		args = append(args, companies)
		companyFilter = ` AND EXISTS (
  SELECT 1 FROM task_scope ts
  WHERE ts.task_id=t.id AND ts.kind='company' AND ts.company_id=ANY($2::bigint[])
)`
	}
	args = append(args, fetchLimit)
	q := `SELECT t.id, COALESCE(NULLIF(t.name,''), t.description), n.id, n.kind, n.state, n.payload, n.created_at
FROM exploration_nodes n
JOIN tasks t ON t.exploration_id=n.exploration_id
WHERE t.deleted_at IS NULL AND n.kind IN ('fact','finding')
  AND (COALESCE(n.payload->>'summary','') ILIKE $1 ESCAPE '\'
    OR COALESCE(n.payload->>'detail','') ILIKE $1 ESCAPE '\'
    OR COALESCE(n.payload->>'hypothesis','') ILIKE $1 ESCAPE '\'
    OR COALESCE(n.payload->>'title','') ILIKE $1 ESCAPE '\')` + companyFilter + `
ORDER BY n.created_at DESC, n.id DESC LIMIT $` + fmt.Sprint(len(args))
	rows, err = s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Precedent, 0, limit)
	for rows.Next() {
		var p Precedent
		var payload []byte
		if err := rows.Scan(&p.TaskID, &p.TaskName, &p.NodeID, &p.Kind, &p.State, &payload, &p.CreatedAt); err != nil {
			return nil, err
		}
		var m map[string]any
		_ = json.Unmarshal(payload, &m)
		for _, k := range []string{"summary", "text", "title"} {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				p.Summary = v
				break
			}
		}
		if v, ok := m["evidence"].(string); ok {
			p.Evidence = v
		}
		detail, _ := m["detail"].(string)
		p.Score = precedentScore(query, p.Summary, detail, p.Evidence)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].NodeID > out[j].NodeID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// precedentScore is a deliberately local relevance score: exact phrase + term
// overlap over summary/detail/evidence, with summary weighted highest. It is not
// an embedding model; it gives deterministic ranking without requiring an
// external embedding provider or pgvector.
func precedentScore(query, summary, detail, evidence string) float64 {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	score := 0.0
	fields := []struct {
		text string
		w    float64
	}{
		{summary, 3}, {detail, 1.5}, {evidence, 1},
	}
	for _, f := range fields {
		text := strings.ToLower(f.text)
		if text == "" {
			continue
		}
		if strings.Contains(text, q) {
			score += 10 * f.w
		}
	}
	terms := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '/' || r == '-' || r == ':' || r == ',' || r == ';'
	})
	hay := strings.ToLower(summary + " " + detail + " " + evidence)
	for _, term := range terms {
		if strings.TrimSpace(term) != "" && strings.Contains(hay, term) {
			score += 2
		}
	}
	return score
}
