package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ExploreModeExplore = "explore"
	ExploreModeDeepen  = "deepen"
	ParkReasonFocus    = "deferred_for_focus"
)

// ExplorationFocus 持久化当前探索任务的焦点策略（广度探索 vs 收敛深挖）。
// 存放在 explorations.exploration_focus 列，跨 Planner 热会话与进程重启保留。
type ExplorationFocus struct {
	Mode          string   `json:"mode"`                      // explore | deepen
	FocusRoutes   []string `json:"focus_routes,omitempty"`    // contract.route / risk_area / asset:<id>
	FocusAssetIDs []int64  `json:"focus_asset_ids,omitempty"` // 焦点资产 id
	FocusNote     string   `json:"focus_note,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
}

// NormalizeExplorationFocus 规整 mode、去重路由和资产 ID。
func NormalizeExplorationFocus(f ExplorationFocus) ExplorationFocus {
	mode := strings.ToLower(strings.TrimSpace(f.Mode))
	if mode != ExploreModeDeepen {
		mode = ExploreModeExplore
	}
	f.Mode = mode
	if len(f.FocusRoutes) > 0 {
		seen := map[string]bool{}
		clean := make([]string, 0, len(f.FocusRoutes))
		for _, r := range f.FocusRoutes {
			r = strings.TrimSpace(r)
			key := strings.ToLower(r)
			if r == "" || seen[key] {
				continue
			}
			seen[key] = true
			clean = append(clean, r)
		}
		f.FocusRoutes = clean
	}
	if len(f.FocusAssetIDs) > 0 {
		seen := map[int64]bool{}
		clean := make([]int64, 0, len(f.FocusAssetIDs))
		for _, id := range f.FocusAssetIDs {
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			clean = append(clean, id)
		}
		f.FocusAssetIDs = clean
	}
	f.FocusNote = strings.TrimSpace(f.FocusNote)
	f.Reason = strings.TrimSpace(f.Reason)
	f.UpdatedAt = strings.TrimSpace(f.UpdatedAt)
	return f
}

// HasTargets reports whether at least one focus route or asset anchor is configured.
func (f ExplorationFocus) HasTargets() bool {
	return len(f.FocusRoutes) > 0 || len(f.FocusAssetIDs) > 0
}

// Matches reports whether an intent's route or target assets belong to the active focus.
func (f ExplorationFocus) Matches(route string, assetIDs []int64) bool {
	if !f.HasTargets() {
		return false
	}
	route = strings.TrimSpace(route)
	if route != "" {
		for _, fr := range f.FocusRoutes {
			if strings.EqualFold(route, fr) {
				return true
			}
		}
	}
	if len(assetIDs) > 0 && len(f.FocusAssetIDs) > 0 {
		want := make(map[int64]bool, len(f.FocusAssetIDs))
		for _, id := range f.FocusAssetIDs {
			want[id] = true
		}
		for _, id := range assetIDs {
			if want[id] {
				return true
			}
		}
	}
	return false
}

// GetExplorationFocus reads the exploration's active explore/deepen focus configuration.
func (s *ExplorationStore) GetExplorationFocus() (ExplorationFocus, error) {
	if s == nil || s.db == nil {
		return ExplorationFocus{Mode: ExploreModeExplore}, nil
	}
	var raw []byte
	err := s.db.QueryRow(`SELECT COALESCE(exploration_focus, '{}'::jsonb) FROM explorations WHERE id=$1`, s.expID).Scan(&raw)
	if err == sql.ErrNoRows {
		return ExplorationFocus{Mode: ExploreModeExplore}, nil
	}
	if err != nil {
		return ExplorationFocus{Mode: ExploreModeExplore}, err
	}
	var f ExplorationFocus
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &f)
	}
	return NormalizeExplorationFocus(f), nil
}

// SetExplorationFocus atomically updates explorations.exploration_focus inside a blackboard operation.
func (s *ExplorationStore) SetExplorationFocus(f ExplorationFocus) error {
	f = NormalizeExplorationFocus(f)
	if f.UpdatedAt == "" {
		f.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := GraphOperationTx(tx, s.expID, "set_exploration_focus", f.Reason); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE explorations SET exploration_focus=$2 WHERE id=$1`, s.expID, string(raw))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("exploration %d not found", s.expID)
	}
	return tx.Commit()
}

// ReprioritizeIntentUpdate describes one priority / park state adjustment for reprioritize_intents.
type ReprioritizeIntentUpdate struct {
	IntentID int64  `json:"intent_id"`
	Priority *int   `json:"priority,omitempty"` // 0..10
	Action   string `json:"action,omitempty"`   // keep | park | resume
}

// ReprioritizeIntentResult describes the transactional outcome of updating one intent.
type ReprioritizeIntentResult struct {
	IntentID    int64  `json:"intent_id"`
	OldState    string `json:"old_state"`
	NewState    string `json:"new_state"`
	OldPriority int    `json:"old_priority"`
	NewPriority int    `json:"new_priority"`
	Changed     bool   `json:"changed"`
	Skipped     string `json:"skipped,omitempty"`
}

func clampIntentPriority(p int) int {
	if p < 0 {
		return 0
	}
	if p > 10 {
		return 10
	}
	return p
}

// BatchUpdateIntents atomically adjusts priorities and park/resume states on open/paused intents.
// Updates are sorted by IntentID before FOR UPDATE locking to avoid deadlocks.
func (s *ExplorationStore) BatchUpdateIntents(reason string, updates []ReprioritizeIntentUpdate) ([]ReprioritizeIntentResult, error) {
	if len(updates) == 0 {
		return nil, fmt.Errorf("updates cannot be empty")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("reason is required")
	}
	// Preserve caller order in output, while locking rows in ascending ID order.
	type indexed struct {
		idx int
		upd ReprioritizeIntentUpdate
	}
	ordered := make([]indexed, len(updates))
	for i, u := range updates {
		ordered[i] = indexed{idx: i, upd: u}
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].upd.IntentID < ordered[j].upd.IntentID
	})

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := GraphOperationTx(tx, s.expID, "reprioritize_intents", reason); err != nil {
		return nil, err
	}

	results := make([]ReprioritizeIntentResult, len(updates))
	seen := map[int64]bool{}
	for _, entry := range ordered {
		u := entry.upd
		res := ReprioritizeIntentResult{IntentID: u.IntentID}
		if u.IntentID <= 0 {
			res.Skipped = "invalid intent_id"
			results[entry.idx] = res
			continue
		}
		if seen[u.IntentID] {
			res.Skipped = "duplicate intent_id in batch"
			results[entry.idx] = res
			continue
		}
		seen[u.IntentID] = true

		action := strings.ToLower(strings.TrimSpace(u.Action))
		if action == "" {
			action = "keep"
		}
		if action != "keep" && action != "park" && action != "resume" {
			res.Skipped = fmt.Sprintf("invalid action %q", u.Action)
			results[entry.idx] = res
			continue
		}

		var state string
		var oldPriority int
		var raw []byte
		err := tx.QueryRow(`SELECT state, priority, payload FROM exploration_nodes
WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, u.IntentID, s.expID).Scan(&state, &oldPriority, &raw)
		if err == sql.ErrNoRows {
			res.Skipped = "intent not found"
			results[entry.idx] = res
			continue
		}
		if err != nil {
			return nil, err
		}
		res.OldState, res.OldPriority = state, oldPriority
		res.NewState, res.NewPriority = state, oldPriority

		if state != "open" && state != "paused" {
			res.Skipped = fmt.Sprintf("state %q cannot be reprioritized", state)
			results[entry.idx] = res
			continue
		}
		var flags struct {
			Cleanup            bool   `json:"cleanup"`
			ResumeAfterRestart bool   `json:"resume_after_restart"`
			ParkReason         string `json:"park_reason"`
		}
		_ = json.Unmarshal(raw, &flags)
		if flags.Cleanup {
			res.Skipped = "cleanup intent cannot be modified"
			results[entry.idx] = res
			continue
		}
		if flags.ResumeAfterRestart && action == "park" {
			res.Skipped = "restart-resume intent cannot be parked"
			results[entry.idx] = res
			continue
		}

		newState := state
		switch action {
		case "park":
			if state == "open" {
				newState = "paused"
			}
		case "resume":
			if state == "paused" {
				newState = "open"
			}
		}
		newPriority := oldPriority
		if u.Priority != nil {
			newPriority = clampIntentPriority(*u.Priority)
		}
		res.NewState, res.NewPriority = newState, newPriority

		setPark := action == "park" && newState == "paused"
		clearPark := newState == "open" && flags.ParkReason != ""
		if newState == state && newPriority == oldPriority && !setPark && !clearPark {
			results[entry.idx] = res
			continue
		}

		if setPark {
			if _, err := tx.Exec(`UPDATE exploration_nodes
SET state=$3, priority=$4, content_version=content_version+1, updated_at=now(),
    payload = COALESCE(payload,'{}'::jsonb) || jsonb_build_object('park_reason', $5::text)
WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, u.IntentID, s.expID, newState, newPriority, ParkReasonFocus); err != nil {
				return nil, err
			}
		} else if clearPark {
			if _, err := tx.Exec(`UPDATE exploration_nodes
SET state=$3, priority=$4, content_version=content_version+1, updated_at=now(),
    payload = COALESCE(payload,'{}'::jsonb) - 'park_reason'
WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, u.IntentID, s.expID, newState, newPriority); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.Exec(`UPDATE exploration_nodes
SET state=$3, priority=$4, content_version=content_version+1, updated_at=now()
WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, u.IntentID, s.expID, newState, newPriority); err != nil {
				return nil, err
			}
		}
		res.Changed = true
		results[entry.idx] = res
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

// CountDeferredParkedIntents returns the count of intents parked via deferred_for_focus.
func (s *ExplorationStore) CountDeferredParkedIntents() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state='paused'
  AND COALESCE(payload->>'park_reason','')=$2`, s.expID, ParkReasonFocus).Scan(&n)
	return n, err
}

// AutoUnparkDeferred restores up to limit highest-priority deferred_for_focus intents back to open.
func (s *ExplorationStore) AutoUnparkDeferred(limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 4
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := GraphOperationTx(tx, s.expID, "auto_unpark_deferred", "焦点路线暂无可执行工作，恢复暂缓支线"); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`WITH pick AS (
  SELECT id FROM exploration_nodes
  WHERE exploration_id=$1 AND kind='intent' AND state='paused'
    AND COALESCE(payload->>'park_reason','')=$2
  ORDER BY priority DESC, id ASC
  LIMIT $3
  FOR UPDATE SKIP LOCKED
)
UPDATE exploration_nodes n
SET state='open', content_version=content_version+1, updated_at=now(),
    payload = COALESCE(n.payload,'{}'::jsonb) - 'park_reason'
FROM pick
WHERE n.id = pick.id
RETURNING n.id`, s.expID, ParkReasonFocus, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// RouteScoreStat aggregates volume and actual yield ROI per exploration route.
type RouteScoreStat struct {
	Route          string `json:"route"`
	Intents        int    `json:"intents"`
	Open           int    `json:"open"`
	Running        int    `json:"running"`
	PausedDeferred int    `json:"paused_deferred"`
	Done           int    `json:"done"`
	NoOutput       int    `json:"no_output"`
	Negative       int    `json:"negative"`
	Findings       int    `json:"findings"`
	Footholds      int    `json:"footholds"`
	Credentials    int    `json:"credentials"`
	Capabilities   int    `json:"capabilities"`
	ROIHint        string `json:"roi_hint,omitempty"`
}

// RouteScorecard computes per-route effectiveness (yields vs empty runs) over the full task graph.
func (s *ExplorationStore) RouteScorecard(limit int) ([]RouteScoreStat, error) {
	if limit <= 0 {
		limit = 15
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := s.db.Query(`WITH parents AS (
  SELECT dst_id, MIN(src_id) AS parent_id FROM exploration_edges
  WHERE exploration_id=$1 AND rel='derived_from' GROUP BY dst_id
),
routed AS (
  SELECT n.id, n.state, COALESCE(n.payload->>'park_reason','') AS park_reason,
         `+intentRouteSQL+` AS route
  FROM exploration_nodes n LEFT JOIN parents p ON p.dst_id=n.id
  WHERE n.exploration_id=$1 AND n.kind='intent' AND n.state<>'deleted'
),
yields AS (
  SELECT e.src_id AS intent_id,
    COUNT(*) FILTER (WHERE dst.kind='finding' AND dst.state IN ('confirmed','open_proof_gap')) AS findings,
    COUNT(*) FILTER (WHERE dst.kind='foothold' AND dst.state='active') AS footholds,
    COUNT(*) FILTER (WHERE dst.kind='credential' AND dst.state='active') AS credentials,
    COUNT(*) FILTER (WHERE dst.kind='fact' AND dst.state='confirmed'
      AND COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(dst.payload->'enables')='array' THEN dst.payload->'enables' ELSE '[]'::jsonb END), 0) > 0) AS capabilities
  FROM exploration_edges e
  JOIN exploration_nodes dst ON dst.id=e.dst_id AND dst.exploration_id=e.exploration_id
  WHERE e.exploration_id=$1 AND e.rel='yields'
  GROUP BY e.src_id
)
SELECT r.route,
  COUNT(*) AS intents,
  COUNT(*) FILTER (WHERE r.state='open') AS open,
  COUNT(*) FILTER (WHERE r.state='running') AS running,
  COUNT(*) FILTER (WHERE r.state='paused' AND r.park_reason=$3) AS paused_deferred,
  COUNT(*) FILTER (WHERE r.state NOT IN ('open','running','paused','deleted')) AS done,
  COUNT(*) FILTER (WHERE r.state='no_output') AS no_output,
  COUNT(*) FILTER (WHERE r.state='negative') AS negative,
  COALESCE(SUM(y.findings),0)::int AS findings,
  COALESCE(SUM(y.footholds),0)::int AS footholds,
  COALESCE(SUM(y.credentials),0)::int AS credentials,
  COALESCE(SUM(y.capabilities),0)::int AS capabilities
FROM routed r
LEFT JOIN yields y ON y.intent_id=r.id
GROUP BY r.route
ORDER BY (COALESCE(SUM(y.findings),0)*3 + COALESCE(SUM(y.footholds),0)*3 + COALESCE(SUM(y.credentials),0)*2 + COALESCE(SUM(y.capabilities),0)) DESC,
         (COUNT(*) FILTER (WHERE r.state IN ('open','running'))) DESC,
         intents DESC, r.route
LIMIT $2`, s.expID, limit, ParkReasonFocus)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RouteScoreStat, 0, limit)
	for rows.Next() {
		var r RouteScoreStat
		if err := rows.Scan(&r.Route, &r.Intents, &r.Open, &r.Running, &r.PausedDeferred,
			&r.Done, &r.NoOutput, &r.Negative, &r.Findings, &r.Footholds, &r.Credentials, &r.Capabilities); err != nil {
			return nil, err
		}
		switch {
		case r.Findings+r.Footholds+r.Credentials > 0:
			r.ROIHint = "high_yield"
		case r.Capabilities > 0:
			r.ROIHint = "promising"
		case r.Done >= 3 && r.NoOutput+r.Negative == r.Done:
			r.ROIHint = "stalled"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// IntentDispatchSignals holds bounded runtime scoring signals for candidate ranking.
type IntentDispatchSignals struct {
	AncestryBonus  map[int64]float64
	RouteMomentum  map[string]float64
	RunningByRoute map[string]int
}

// IntentDispatchSignals computes lineage momentum and active concurrency signals for ids.
func (s *ExplorationStore) IntentDispatchSignals(ids []int64, routes map[int64]string) (IntentDispatchSignals, error) {
	sig := IntentDispatchSignals{
		AncestryBonus:  map[int64]float64{},
		RouteMomentum:  map[string]float64{},
		RunningByRoute: map[string]int{},
	}
	if len(ids) == 0 {
		return sig, nil
	}
	// 1) Derived parent signals for each candidate intent.
	rows, err := s.db.Query(`SELECT e.dst_id, src.kind, src.state, src.payload
FROM exploration_edges e
JOIN exploration_nodes src ON src.id=e.src_id AND src.exploration_id=e.exploration_id
WHERE e.exploration_id=$1 AND e.rel='derived_from' AND e.dst_id=ANY($2::bigint[])`, s.expID, ids)
	if err != nil {
		return sig, err
	}
	for rows.Next() {
		var dstID int64
		var kind, state string
		var raw []byte
		if err := rows.Scan(&dstID, &kind, &state, &raw); err != nil {
			rows.Close()
			return sig, err
		}
		bonus := 0.0
		switch kind {
		case KindFinding:
			if state == "confirmed" || state == "open_proof_gap" {
				bonus = 140
			}
		case KindFoothold, KindCredential:
			if state == StateLootActive {
				bonus = 140
			}
		case KindHint:
			if state == StateHintActive {
				bonus = 120
			}
		case KindFact:
			if state == "confirmed" {
				var p struct {
					Origin   string   `json:"origin"`
					Polarity string   `json:"polarity"`
					Enables  []string `json:"enables"`
				}
				_ = json.Unmarshal(raw, &p)
				if p.Polarity != "negative" && (len(p.Enables) > 0 || strings.EqualFold(p.Origin, "proposal")) {
					bonus = 80
				}
			}
		}
		if bonus > sig.AncestryBonus[dstID] {
			sig.AncestryBonus[dstID] = bonus
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return sig, err
	}
	rows.Close()

	// 2) Running intent count & yield momentum per route.
	rrows, err := s.db.Query(`WITH parents AS (
  SELECT dst_id, MIN(src_id) AS parent_id FROM exploration_edges
  WHERE exploration_id=$1 AND rel='derived_from' GROUP BY dst_id
),
routed AS (
  SELECT n.id, n.state, `+intentRouteSQL+` AS route
  FROM exploration_nodes n LEFT JOIN parents p ON p.dst_id=n.id
  WHERE n.exploration_id=$1 AND n.kind='intent' AND n.state<>'deleted'
),
yields AS (
  SELECT e.src_id AS intent_id,
    COUNT(*) FILTER (WHERE dst.kind IN ('finding','foothold','credential') AND dst.state IN ('confirmed','open_proof_gap','active')) AS breakthroughs,
    COUNT(*) FILTER (WHERE dst.kind='fact' AND dst.state='confirmed'
      AND COALESCE(jsonb_array_length(CASE WHEN jsonb_typeof(dst.payload->'enables')='array' THEN dst.payload->'enables' ELSE '[]'::jsonb END), 0) > 0) AS capabilities
  FROM exploration_edges e
  JOIN exploration_nodes dst ON dst.id=e.dst_id AND dst.exploration_id=e.exploration_id
  WHERE e.exploration_id=$1 AND e.rel='yields'
  GROUP BY e.src_id
),
recent_settled AS (
  SELECT route, state,
    ROW_NUMBER() OVER (PARTITION BY route ORDER BY id DESC) AS rn
  FROM routed
  WHERE state NOT IN ('open','running','paused','deleted')
),
streaks AS (
  SELECT route,
    COUNT(*) FILTER (WHERE rn <= 3) AS recent_total,
    COUNT(*) FILTER (WHERE rn <= 3 AND state IN ('no_output','negative')) AS recent_empty
  FROM recent_settled
  GROUP BY route
)
SELECT r.route,
  COUNT(*) FILTER (WHERE r.state='running')::int AS running_cnt,
  COALESCE(SUM(y.breakthroughs),0)::int AS breakthroughs,
  COALESCE(SUM(y.capabilities),0)::int AS capabilities,
  COALESCE(MAX(s.recent_total),0)::int AS recent_total,
  COALESCE(MAX(s.recent_empty),0)::int AS recent_empty
FROM routed r
LEFT JOIN yields y ON y.intent_id=r.id
LEFT JOIN streaks s ON s.route=r.route
GROUP BY r.route`, s.expID)
	if err != nil {
		return sig, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var route string
		var running, breakthroughs, caps, recentTotal, recentEmpty int
		if err := rrows.Scan(&route, &running, &breakthroughs, &caps, &recentTotal, &recentEmpty); err != nil {
			return sig, err
		}
		if running > 0 {
			sig.RunningByRoute[route] = running
		}
		switch {
		case breakthroughs >= 2:
			sig.RouteMomentum[route] = 100
		case breakthroughs == 1:
			sig.RouteMomentum[route] = 80
		case caps > 0:
			sig.RouteMomentum[route] = 40
		case recentTotal >= 3 && recentEmpty == recentTotal:
			sig.RouteMomentum[route] = -40
		}
	}
	return sig, rrows.Err()
}
