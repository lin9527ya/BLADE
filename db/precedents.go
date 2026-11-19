package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// PrecedentScope controls which tasks a precedent search may read.
//
//   - auto (default): company scope if the current task has one, else campaign,
//     else redacted global.
//   - company: only tasks sharing a company scope; no fallback when absent.
//   - campaign: only tasks in the same campaign; no fallback when absent.
//   - global: explicitly cross the whole database. Evidence remains omitted
//     unless the caller also sets IncludeEvidence.
const (
	PrecedentScopeAuto     = "auto"
	PrecedentScopeCompany  = "company"
	PrecedentScopeCampaign = "campaign"
	PrecedentScopeGlobal   = "global"
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

// PrecedentSearchOptions carries the explicitly chosen scope and whether the
// caller wants raw evidence. Evidence is omitted by default because it may hold
// credentials, payloads or client data; Summary is always secret-redacted.
type PrecedentSearchOptions struct {
	Scope           string
	IncludeEvidence bool
}

// PrecedentSearchResult includes the resolved scope so the planner can tell
// whether it got company/campaign data or a redacted global fallback.
type PrecedentSearchResult struct {
	Hits            []Precedent `json:"hits"`
	Scope           string      `json:"scope"`
	Reason          string      `json:"reason,omitempty"`
	EvidenceOmitted bool        `json:"evidence_omitted,omitempty"`
}

func normalizePrecedentScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case PrecedentScopeCompany:
		return PrecedentScopeCompany
	case PrecedentScopeCampaign:
		return PrecedentScopeCampaign
	case PrecedentScopeGlobal:
		return PrecedentScopeGlobal
	default:
		return PrecedentScopeAuto
	}
}

// SearchPrecedents searches facts/findings in other tasks by keyword. The
// current exploration is always excluded. auto resolves to company when the
// task has company scopes, then campaign, then redacted global; explicit
// company/campaign never silently fall back to global.
func (s *ExplorationStore) SearchPrecedents(query string, limit int, opts PrecedentSearchOptions) (PrecedentSearchResult, error) {
	result := PrecedentSearchResult{Hits: []Precedent{}}
	query = strings.TrimSpace(query)
	if query == "" {
		return result, nil
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

	var campaignID sql.NullInt64
	if err := s.db.QueryRow(`SELECT campaign_id FROM tasks
WHERE exploration_id=$1 AND deleted_at IS NULL LIMIT 1`, s.expID).Scan(&campaignID); err != nil && err != sql.ErrNoRows {
		return result, err
	}
	companies, err := s.companyScopeIDs()
	if err != nil {
		return result, err
	}

	requested := normalizePrecedentScope(opts.Scope)
	resolved := requested
	switch requested {
	case PrecedentScopeAuto:
		switch {
		case len(companies) > 0:
			resolved = PrecedentScopeCompany
		case campaignID.Valid && campaignID.Int64 > 0:
			resolved = PrecedentScopeCampaign
		default:
			resolved = PrecedentScopeGlobal
			result.Reason = "auto_fallback_global"
		}
	case PrecedentScopeCompany:
		if len(companies) == 0 {
			result.Scope = PrecedentScopeCompany
			result.Reason = "no_company_scope"
			return result, nil
		}
	case PrecedentScopeCampaign:
		if !campaignID.Valid || campaignID.Int64 == 0 {
			result.Scope = PrecedentScopeCampaign
			result.Reason = "no_campaign"
			return result, nil
		}
	}
	result.Scope = resolved
	explicitGlobal := requested == PrecedentScopeGlobal
	includeEvidence := opts.IncludeEvidence && (resolved != PrecedentScopeGlobal || explicitGlobal)
	result.EvidenceOmitted = !includeEvidence

	args := []any{s.expID, pattern}
	scopeFilter := ""
	switch resolved {
	case PrecedentScopeCompany:
		args = append(args, companies)
		scopeFilter = fmt.Sprintf(` AND EXISTS (
  SELECT 1 FROM task_scope ts
  WHERE ts.task_id=t.id AND ts.kind='company' AND ts.company_id=ANY($%d::bigint[])
)`, len(args))
	case PrecedentScopeCampaign:
		args = append(args, campaignID.Int64)
		scopeFilter = fmt.Sprintf(` AND t.campaign_id=$%d`, len(args))
	}
	args = append(args, fetchLimit)
	q := `SELECT t.id, COALESCE(NULLIF(t.name,''), t.description), n.id, n.kind, n.state, n.payload, n.created_at
FROM exploration_nodes n
JOIN tasks t ON t.exploration_id=n.exploration_id
WHERE t.deleted_at IS NULL AND n.exploration_id <> $1 AND n.kind IN ('fact','finding')
  AND (COALESCE(n.payload->>'summary','') ILIKE $2 ESCAPE '\'
    OR COALESCE(n.payload->>'detail','') ILIKE $2 ESCAPE '\'
    OR COALESCE(n.payload->>'hypothesis','') ILIKE $2 ESCAPE '\'
    OR COALESCE(n.payload->>'title','') ILIKE $2 ESCAPE '\')` + scopeFilter + `
ORDER BY n.created_at DESC, n.id DESC LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	out := make([]Precedent, 0, limit)
	for rows.Next() {
		var p Precedent
		var payload []byte
		if err := rows.Scan(&p.TaskID, &p.TaskName, &p.NodeID, &p.Kind, &p.State, &payload, &p.CreatedAt); err != nil {
			return result, err
		}
		var m map[string]any
		_ = json.Unmarshal(payload, &m)
		summary := ""
		for _, k := range []string{"summary", "text", "title"} {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				summary = v
				break
			}
		}
		evidence, _ := m["evidence"].(string)
		detail, _ := m["detail"].(string)
		// Score against the original text, then redact what leaves the DB layer.
		p.Score = precedentScore(query, summary, detail, evidence)
		p.Summary = redactPrecedentSecrets(summary)
		if includeEvidence {
			p.Evidence = redactPrecedentSecrets(evidence)
		}
		if resolved == PrecedentScopeGlobal {
			p.TaskName = fmt.Sprintf("task#%d", p.TaskID)
		} else {
			p.TaskName = redactPrecedentSecrets(p.TaskName)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return result, err
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
	result.Hits = out
	return result, nil
}

func (s *ExplorationStore) companyScopeIDs() ([]int64, error) {
	rows, err := s.db.Query(`
SELECT DISTINCT ts.company_id FROM task_scope ts
JOIN tasks t ON t.id=ts.task_id
WHERE t.exploration_id=$1 AND ts.kind='company' AND ts.company_id IS NOT NULL`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

var (
	precedentPrivateKeyRE = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	precedentBearerRE     = regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]+`)
	precedentSecretRE     = regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|secret|api[_-]?key|access[_-]?key|private[_-]?key|authorization)\b\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
)

// redactPrecedentSecrets is a conservative best-effort scrubber for values that
// commonly appear in evidence text. It is not a substitute for scope choice.
func redactPrecedentSecrets(s string) string {
	if s == "" {
		return s
	}
	s = precedentPrivateKeyRE.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = precedentBearerRE.ReplaceAllString(s, "${1}[REDACTED]")
	s = precedentSecretRE.ReplaceAllString(s, "${1}=[REDACTED]")
	return s
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
