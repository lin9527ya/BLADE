package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func ExplorationLeadID(fact int64, index int) string { return fmt.Sprintf("L%d-%d", fact, index) }
func ParseExplorationLeadID(raw string) (int64, int, error) {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if len(raw) < 4 {
		return 0, 0, fmt.Errorf("invalid lead id %q", raw)
	}
	parts := strings.Split(raw[1:], "-")
	if !(strings.HasPrefix(raw, "L") || strings.HasPrefix(raw, "P")) || len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid lead id %q", raw)
	}
	fact, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || fact <= 0 {
		return 0, 0, fmt.Errorf("invalid lead fact id")
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil || index < 0 {
		return 0, 0, fmt.Errorf("invalid lead index")
	}
	return fact, index, nil
}

type leadReference struct {
	Text     string     `json:"text_snapshot"`
	LinkedAt *time.Time `json:"linked_at,omitempty"`
	Match    string     `json:"match"`
}

// Associations are stored on the intent in its creation transaction. Explicit
// references are optional; automatic matching is exact and unambiguous across
// declared fact parents, never inferred from a shared asset or mere child count.
func (s *ExplorationStore) linkIntentLeadsTx(tx *sql.Tx, intentID int64, explicit []string) error {
	var raw []byte
	if err := tx.QueryRow(`SELECT payload FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, intentID, s.expID).Scan(&raw); err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	parentRows, err := tx.Query(`SELECT src_id FROM exploration_edges WHERE exploration_id=$1 AND dst_id=$2 AND rel='derived_from'`, s.expID, intentID)
	if err != nil {
		return err
	}
	parents := map[int64]bool{}
	for parentRows.Next() {
		var id int64
		if err := parentRows.Scan(&id); err != nil {
			parentRows.Close()
			return err
		}
		parents[id] = true
	}
	err = parentRows.Err()
	parentRows.Close()
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	factIDs := []int64{}
	for id := range parents {
		factIDs = append(factIDs, id)
	}
	if len(explicit) > 32 {
		return fmt.Errorf("at most 32 lead references per intent")
	}
	for _, raw := range explicit {
		fact, index, err := ParseExplorationLeadID(raw)
		if err != nil {
			return err
		}
		selected[canonicalLeadID(raw, fact, index)] = true
		if !parents[fact] {
			factIDs = append(factIDs, fact)
			parents[fact] = true
		}
	}
	if len(factIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(`SELECT id,payload FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND id=ANY($2::bigint[]) ORDER BY id FOR SHARE`, s.expID, factIDs)
	if err != nil {
		return err
	}
	texts := map[string]string{}
	auto := []string{}
	normalize := func(text string) string { return strings.Join(strings.Fields(text), " ") }
	summary := normalize(payloadSummary(raw))
	hypothesis, _ := payload["hypothesis"].(string)
	for rows.Next() {
		var id int64
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return err
		}
		for key, text := range factTraceEntries(id, body) {
			texts[key] = text
			if normalize(text) == summary || (hypothesis != "" && normalize(text) == normalize(hypothesis)) {
				auto = append(auto, key)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for key := range selected {
		if texts[key] == "" {
			return fmt.Errorf("lead %s does not exist in this task", key)
		}
	}
	if len(auto) == 1 {
		selected[auto[0]] = true
	}
	if len(selected) == 0 {
		return nil
	}
	var now time.Time
	if err := tx.QueryRow(`SELECT now()`).Scan(&now); err != nil {
		return err
	}
	converted := map[string]bool{}
	if ids, ok := payload["converted_lead_ids"].([]any); ok {
		for _, id := range ids {
			if text, ok := id.(string); ok {
				converted[strings.ToUpper(strings.TrimSpace(text))] = true
			}
		}
	}
	refs, ok := payload["lead_links"].(map[string]any)
	if !ok {
		refs = map[string]any{}
	}
	for key := range selected {
		alreadyLinked := converted[key]
		converted[key] = true
		if refs[key] == nil {
			match := "exact"
			for _, raw := range explicit {
				fact, index, _ := ParseExplorationLeadID(raw)
				if canonicalLeadID(raw, fact, index) == key {
					match = "explicit"
				}
			}
			var linkedAt *time.Time
			if alreadyLinked {
				match = "legacy"
			} else {
				linkedAt = &now
			}
			refs[key] = leadReference{Text: texts[key], LinkedAt: linkedAt, Match: match}
		}
		fact, _, _ := ParseExplorationLeadID(key)
		if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'derived_from',$3) ON CONFLICT DO NOTHING`, s.expID, fact, intentID); err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(converted))
	for key := range converted {
		ids = append(ids, key)
	}
	sort.Strings(ids)
	payload["converted_lead_ids"] = ids
	payload["lead_links"] = refs
	delete(payload, "lead_ids")
	updated, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE exploration_nodes SET payload=$3,content_version=content_version+1 WHERE id=$1 AND exploration_id=$2`, intentID, s.expID, string(updated))
	if err != nil {
		return err
	}
	// Only remove the root fallback after a real source has been established.
	_, err = tx.Exec(`DELETE FROM exploration_edges e USING exploration_nodes f WHERE e.exploration_id=$1 AND e.dst_id=$2 AND e.rel='derived_from' AND f.id=e.src_id AND f.state='origin' AND f.kind='fact'`, s.expID, intentID)
	return err
}

func (s *ExplorationStore) LinkIntentLeads(intent int64, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.linkIntentLeadsTx(tx, intent, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func optionalLeadIDs(payload map[string]any) []string {
	switch value := payload["lead_ids"].(type) {
	case []string:
		return value
	case []any:
		out := []string{}
		for _, v := range value {
			if text, ok := v.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

type LeadIntentTrace struct {
	TextSnapshot    string                     `json:"text_snapshot,omitempty"`
	IntentID        int64                      `json:"intent_id"`
	Summary         string                     `json:"summary"`
	State           string                     `json:"state"`
	IntentCreatedAt time.Time                  `json:"intent_created_at"`
	LinkedAt        *time.Time                 `json:"linked_at,omitempty"`
	FirstClaimedAt  *time.Time                 `json:"first_claimed_at,omitempty"`
	FirstStartedAt  *time.Time                 `json:"first_started_at,omitempty"`
	CompletedAt     *time.Time                 `json:"completed_at,omitempty"`
	Match           string                     `json:"match"`
	SourceChanged   bool                       `json:"source_changed"`
	LastDispatch    *IntentScheduleExplanation `json:"last_dispatch,omitempty"`
}

type LeadTrace struct {
	Kind             string            `json:"kind"`
	AssetIDs         []int64           `json:"asset_ids,omitempty"`
	ID               string            `json:"id"`
	FactID           int64             `json:"fact_id"`
	Text             string            `json:"text"`
	FactCreatedAt    time.Time         `json:"fact_created_at"`
	State            string            `json:"state"`
	SourceRetired    bool              `json:"source_retired"`
	Intents          []LeadIntentTrace `json:"intents"`
	IntentTotal      int               `json:"intent_total"`
	HistoryTruncated bool              `json:"history_truncated"`
	AdoptedAt        *time.Time        `json:"adopted_at,omitempty"`
	FirstStartedAt   *time.Time        `json:"first_started_at,omitempty"`
}

type LeadTraceStats struct {
	Queued                  int      `json:"queued"`
	Paused                  int      `json:"paused"`
	Total                   int      `json:"total"`
	Pending                 int      `json:"pending"`
	Adopted                 int      `json:"adopted"`
	Running                 int      `json:"running"`
	Completed               int      `json:"completed"`
	NeedsFollowUp           int      `json:"needs_follow_up"`
	Dismissed               int      `json:"dismissed"`
	SourceChanged           int      `json:"source_changed"`
	AdoptionSamples         int      `json:"adoption_samples"`
	ExecutionSamples        int      `json:"execution_samples"`
	AverageAdoptionSeconds  *float64 `json:"average_adoption_seconds,omitempty"`
	AverageExecutionSeconds *float64 `json:"average_execution_seconds,omitempty"`
}

type LeadTraceReport struct {
	PendingPreview []LeadTrace `json:"-"`
	// OldestPendingPreview 是最老的一批未处理线索（受 20 条上限）。查询按最新优先
	// 扫描，所以这里在扫描中维护一个「保留最老 N 条」的小集合，避免二次全量查询。
	OldestPendingPreview []LeadTrace    `json:"-"`
	OpenByFact           map[int64]int  `json:"-"`
	Rows                 []LeadTrace    `json:"rows"`
	Stats                LeadTraceStats `json:"stats"`
	Total                int            `json:"total"`
	Limit                int            `json:"limit"`
	Offset               int            `json:"offset"`
	HasMore              bool           `json:"has_more"`
	Status               ReadStatus     `json:"read_status"`
}

// Full-task aggregate and paged display are separate. The join reads all intent
// associations (not the last 300 intents) and avoids a per-lead query loop.
func (s *ExplorationStore) LeadTraceReport(state, leadID string, limit, offset int) (*LeadTraceReport, error) {
	if !s.InReadSnapshot() {
		var out *LeadTraceReport
		err := s.ReadSnapshot(context.Background(), func(_ *DB, view *ExplorationStore) error {
			var err error
			out, err = view.LeadTraceReport(state, leadID, limit, offset)
			return err
		})
		return out, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(`WITH leads AS (
 SELECT f.id,f.created_at,f.state,format('L%s-%s',f.id,l.ord-1) AS lead_id,l.item->>'text' AS text,'lead'::text AS kind,l.item->'asset_ids' AS assets,
  COALESCE(f.payload->'dismissed_leads','[]'::jsonb) ? format('L%s-%s',f.id,l.ord-1) AS dismissed
 FROM exploration_nodes f CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(f.payload->'leads')='array' THEN f.payload->'leads' ELSE '[]'::jsonb END) WITH ORDINALITY l(item,ord)
 WHERE f.exploration_id=$1 AND f.kind='fact' AND f.state<>'origin' AND jsonb_typeof(l.item->'text')='string' AND length(btrim(l.item->>'text'))>0
 UNION ALL
 SELECT f.id,f.created_at,f.state,format('P%s-%s',f.id,l.ord-1),l.item->>'summary','proposal'::text,l.item->'asset_ids',
 COALESCE(f.payload->'dismissed_leads','[]'::jsonb) ? format('P%s-%s',f.id,l.ord-1)
 FROM exploration_nodes f CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(f.payload->'proposals')='array' THEN f.payload->'proposals' ELSE '[]'::jsonb END) WITH ORDINALITY l(item,ord)
 WHERE f.exploration_id=$1 AND f.kind='fact' AND jsonb_typeof(l.item->'summary')='string' AND length(btrim(l.item->>'summary'))>0
 ), refs AS (
 SELECT DISTINCT i.id,upper(btrim(ref.value)) AS lead_id,i.payload,i.state,i.created_at,i.completed_at
 FROM exploration_nodes i CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(i.payload->'converted_lead_ids')='array' THEN i.payload->'converted_lead_ids' ELSE '[]'::jsonb END) ref(value)
 WHERE i.exploration_id=$1 AND i.kind='intent'
 ), starts AS (SELECT node_id,MIN(created_at) AS ts FROM activity WHERE exploration_id=$1 AND kind='intent' GROUP BY node_id) SELECT l.id,l.created_at,l.state,l.lead_id,l.text,l.dismissed,l.kind,COALESCE(l.assets,'[]'::jsonb),
 COALESCE(jsonb_agg(jsonb_build_object('id',r.id,'summary',r.payload->>'summary','state',r.state,'created_at',r.created_at,'completed_at',r.completed_at,
 'linked_at',r.payload->'lead_links'->l.lead_id->>'linked_at','match',r.payload->'lead_links'->l.lead_id->>'match',
 'text_snapshot',r.payload->'lead_links'->l.lead_id->>'text_snapshot','first_claimed_at',r.payload->>'first_claimed_at',
 'first_started_at',COALESCE(r.payload->>'first_started_at',to_jsonb(started.ts)#>>'{}'), 'dispatch_snapshot',r.payload->'dispatch_snapshot')) FILTER(WHERE r.id IS NOT NULL),'[]'::jsonb)
 FROM leads l LEFT JOIN refs r ON r.lead_id=l.lead_id
 LEFT JOIN starts started ON started.node_id=r.id
 GROUP BY l.id,l.created_at,l.state,l.lead_id,l.text,l.dismissed,l.kind,l.assets ORDER BY l.id DESC,l.lead_id`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &LeadTraceReport{Rows: []LeadTrace{}, Limit: limit, Offset: offset, OpenByFact: map[int64]int{}}
	filtered := []LeadTrace{}
	var adoptionSum, executionSum float64
	for rows.Next() {
		var lead LeadTrace
		var factState string
		var dismissed bool
		var raw, assets []byte
		if err := rows.Scan(&lead.FactID, &lead.FactCreatedAt, &factState, &lead.ID, &lead.Text, &dismissed, &lead.Kind, &assets, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(assets, &lead.AssetIDs)
		lead.SourceRetired = factState == "dismissed"
		var refs []struct {
			ID        int64           `json:"id"`
			Summary   string          `json:"summary"`
			State     string          `json:"state"`
			Created   time.Time       `json:"created_at"`
			Completed *time.Time      `json:"completed_at"`
			Linked    *string         `json:"linked_at"`
			Match     *string         `json:"match"`
			Text      *string         `json:"text_snapshot"`
			Claimed   *string         `json:"first_claimed_at"`
			Started   *string         `json:"first_started_at"`
			Dispatch  json.RawMessage `json:"dispatch_snapshot"`
		}
		if err := json.Unmarshal(raw, &refs); err != nil {
			return nil, err
		}
		lead.Intents = []LeadIntentTrace{}
		unknownAdoption, unknownExecution := false, false
		for _, r := range refs {
			ref := LeadIntentTrace{IntentID: r.ID, Summary: r.Summary, State: r.State, IntentCreatedAt: r.Created, CompletedAt: r.Completed, Match: "legacy", LinkedAt: parseTraceTime(r.Linked), FirstClaimedAt: parseTraceTime(r.Claimed), FirstStartedAt: parseTraceTime(r.Started)}
			if r.Match != nil && (*r.Match == "explicit" || *r.Match == "exact") {
				ref.Match = *r.Match
			}
			ref.SourceChanged = r.Text != nil && *r.Text != lead.Text
			if r.Text != nil {
				ref.TextSnapshot = *r.Text
			}
			if len(r.Dispatch) > 0 && string(r.Dispatch) != "null" {
				var d IntentScheduleExplanation
				if json.Unmarshal(r.Dispatch, &d) == nil && d.IntentID == r.ID && !d.EvaluatedAt.IsZero() && d.Evaluated {
					ref.LastDispatch = &d
				}
			}
			lead.Intents = append(lead.Intents, ref)
			if !ref.SourceChanged {
				if ref.Match == "legacy" && ref.LinkedAt == nil {
					unknownAdoption = true
				}
				if ref.Match == "legacy" && ref.FirstStartedAt == nil {
					unknownExecution = true
				}
				lead.AdoptedAt = earliestTraceTime(lead.AdoptedAt, ref.LinkedAt)
				lead.FirstStartedAt = earliestTraceTime(lead.FirstStartedAt, ref.FirstStartedAt)
			}
		}
		if unknownAdoption {
			lead.AdoptedAt = nil
		}
		if unknownExecution {
			lead.FirstStartedAt = nil
		}
		sort.Slice(lead.Intents, func(i, j int) bool {
			a, b := lead.Intents[i], lead.Intents[j]
			at, bt := a.IntentCreatedAt, b.IntentCreatedAt
			if a.LinkedAt != nil {
				at = *a.LinkedAt
			}
			if b.LinkedAt != nil {
				bt = *b.LinkedAt
			}
			if !at.Equal(bt) {
				return at.After(bt)
			}
			return a.IntentID > b.IntentID
		})
		lead.State = leadTraceState(lead.Intents, dismissed || lead.SourceRetired)
		out.Stats.Total++
		if len(lead.Intents) > 0 {
			out.Stats.Adopted++
		}
		switch lead.State {
		case "pending":
			out.Stats.Pending++
		case "queued":
			out.Stats.Queued++
		case "paused":
			out.Stats.Paused++
		case "running":
			out.Stats.Running++
		case "completed":
			out.Stats.Completed++
		case "needs_follow_up":
			out.Stats.NeedsFollowUp++
		case "dismissed":
			out.Stats.Dismissed++
		case "source_changed":
			out.Stats.SourceChanged++
		}
		if lead.State != "source_changed" {
			if lead.AdoptedAt != nil && !lead.AdoptedAt.Before(lead.FactCreatedAt) {
				adoptionSum += lead.AdoptedAt.Sub(lead.FactCreatedAt).Seconds()
				out.Stats.AdoptionSamples++
			}
			if lead.FirstStartedAt != nil && !lead.FirstStartedAt.Before(lead.FactCreatedAt) {
				executionSum += lead.FirstStartedAt.Sub(lead.FactCreatedAt).Seconds()
				out.Stats.ExecutionSamples++
			}
		}
		openLead := openLeadState(lead.State)
		if openLead {
			out.OpenByFact[lead.FactID]++
			if len(out.PendingPreview) < 20 {
				out.PendingPreview = append(out.PendingPreview, lead)
			}
		}
		lead.IntentTotal = len(lead.Intents)
		if len(lead.Intents) > 20 {
			lead.Intents = lead.Intents[:20]
			lead.HistoryTruncated = true
		}
		if openLead {
			out.OldestPendingPreview = keepOldestLeads(out.OldestPendingPreview, lead, 20)
		}
		if (state == "" || lead.State == state) && (leadID == "" || lead.ID == leadID) {
			filtered = append(filtered, lead)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out.OldestPendingPreview, func(i, j int) bool {
		a, b := out.OldestPendingPreview[i], out.OldestPendingPreview[j]
		if !a.FactCreatedAt.Equal(b.FactCreatedAt) {
			return a.FactCreatedAt.Before(b.FactCreatedAt)
		}
		return a.ID < b.ID
	})
	if out.Stats.AdoptionSamples > 0 {
		v := adoptionSum / float64(out.Stats.AdoptionSamples)
		out.Stats.AverageAdoptionSeconds = &v
	}
	if out.Stats.ExecutionSamples > 0 {
		v := executionSum / float64(out.Stats.ExecutionSamples)
		out.Stats.AverageExecutionSeconds = &v
	}
	out.Total = len(filtered)
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	if offset < len(filtered) {
		out.Rows = filtered[offset:end]
	}
	out.HasMore = end < len(filtered)
	out.Status = ReadSetStatus(int64(out.Total), len(out.Rows), "leads?offset=<next>")
	return out, nil
}

func parseTraceTime(raw *string) *time.Time {
	if raw == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		return nil
	}
	return &t
}
func earliestTraceTime(a, b *time.Time) *time.Time {
	if b == nil {
		return a
	}
	if a == nil || b.Before(*a) {
		return b
	}
	return a
}

func openLeadState(state string) bool {
	switch state {
	case "pending", "needs_follow_up", "source_changed":
		return true
	}
	return false
}

// keepOldestLeads keeps the N oldest items seen so far. The report query scans
// newest-first, so a plain append would surface the freshest leads instead.
func keepOldestLeads(list []LeadTrace, lead LeadTrace, limit int) []LeadTrace {
	if limit <= 0 {
		return list
	}
	if len(list) < limit {
		return append(list, lead)
	}
	newest := 0
	for i := 1; i < len(list); i++ {
		if list[i].FactCreatedAt.After(list[newest].FactCreatedAt) {
			newest = i
		}
	}
	if lead.FactCreatedAt.Before(list[newest].FactCreatedAt) {
		list[newest] = lead
	}
	return list
}

func leadTraceState(refs []LeadIntentTrace, dismissed bool) string {
	if len(refs) == 0 {
		if dismissed {
			return "dismissed"
		}
		return "pending"
	}
	for _, r := range refs {
		if r.SourceChanged {
			return "source_changed"
		}
	}
	for _, state := range []string{"running", "open", "paused"} {
		for _, r := range refs {
			if r.State == state {
				if state == "open" {
					return "queued"
				}
				return state
			}
		}
	}
	if dismissed {
		return "dismissed"
	}
	if refs[0].State == "done" || refs[0].State == "negative" {
		return "completed"
	}
	return "needs_follow_up"
}

func canonicalLeadID(raw string, fact int64, index int) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	return fmt.Sprintf("%c%d-%d", raw[0], fact, index)
}

func factTraceEntries(id int64, raw []byte) map[string]string {
	var p struct {
		Leads []struct {
			Text string `json:"text"`
		} `json:"leads"`
		Proposals []struct {
			Summary string `json:"summary"`
		} `json:"proposals"`
		Detail string `json:"detail"`
		Origin string `json:"origin"`
	}
	out := map[string]string{}
	if json.Unmarshal(raw, &p) != nil {
		return out
	}
	for index, lead := range p.Leads {
		if strings.TrimSpace(lead.Text) != "" {
			out[ExplorationLeadID(id, index)] = lead.Text
		}
	}
	if len(p.Proposals) == 0 && p.Origin == "proposal" {
		var d struct {
			Proposals []struct {
				Summary string `json:"summary"`
			} `json:"proposals"`
		}
		if json.Unmarshal([]byte(p.Detail), &d) == nil {
			p.Proposals = d.Proposals
		}
	}
	for index, proposal := range p.Proposals {
		if strings.TrimSpace(proposal.Summary) != "" {
			out[fmt.Sprintf("P%d-%d", id, index)] = proposal.Summary
		}
	}
	return out
}

// A structural backfill only: historical proposals become addressable, but no
// adoption time or execution outcome is invented. Unknown/custom documents stay.
func (d *DB) BackfillProposalEntries() error {
	rows, err := d.Query(`SELECT id,exploration_id,payload FROM exploration_nodes WHERE kind='fact' AND payload->>'origin'='proposal' AND NOT(payload ? 'proposals')`)
	if err != nil {
		return err
	}
	type change struct {
		id, exploration   int64
		before, proposals string
	}
	updates := []change{}
	for rows.Next() {
		var id, exploration int64
		var raw []byte
		if err := rows.Scan(&id, &exploration, &raw); err != nil {
			rows.Close()
			return err
		}
		var p struct {
			Detail string `json:"detail"`
		}
		var doc struct {
			Proposals []json.RawMessage `json:"proposals"`
		}
		if json.Unmarshal(raw, &p) != nil || json.Unmarshal([]byte(p.Detail), &doc) != nil || len(doc.Proposals) == 0 {
			continue
		}
		data, err := json.Marshal(doc.Proposals)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, change{id, exploration, string(raw), string(data)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		var task int64
		err = tx.QueryRow(`SELECT id FROM tasks WHERE exploration_id=$1`, u.exploration).Scan(&task)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			tx.Rollback()
			return err
		}
		if task > 0 {
			if err := LockTaskEvidenceSharedTx(tx, task); err != nil {
				tx.Rollback()
				if errors.Is(err, ErrTaskArchiveState) {
					continue
				}
				return err
			}
		}
		_, err = tx.Exec(`UPDATE exploration_nodes SET payload=payload||jsonb_build_object('proposals',$2::jsonb),content_version=content_version+1 WHERE id=$1 AND payload=$3::jsonb`, u.id, u.proposals, u.before)
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
