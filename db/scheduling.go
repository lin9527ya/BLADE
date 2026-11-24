package db

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

const DispatchBatchSize = 20

type ScheduleComponents struct {
	Priority   float64 `json:"priority"`
	Dependency float64 `json:"dependency"`
	Restart    float64 `json:"restart"`
	Route      float64 `json:"route"`
	Aging      float64 `json:"aging"`
	Retries    float64 `json:"retries"`
}

type IntentScheduleExplanation struct {
	WaitReasons       []string           `json:"wait_reasons,omitempty"`
	IntentID          int64              `json:"intent_id"`
	Summary           string             `json:"summary"`
	State             string             `json:"state"`
	EvaluatedAt       time.Time          `json:"evaluated_at"`
	Evaluated         bool               `json:"evaluated"`
	Claimable         bool               `json:"claimable"`
	Rank              int                `json:"rank,omitempty"`
	Score             float64            `json:"score"`
	Components        ScheduleComponents `json:"components"`
	Route             string             `json:"route"`
	RouteCount        int                `json:"route_count"`
	LargestRouteCount int                `json:"largest_route_count"`
	WaitingSeconds    int64              `json:"waiting_seconds"`
	FairWait          bool               `json:"fair_wait"`
	SelectionReason   string             `json:"selection_reason"`
	WaitReason        string             `json:"wait_reason,omitempty"`
	BlockedReason     string             `json:"blocked_reason,omitempty"`
	RequiresMissing   []map[string]any   `json:"requires_missing,omitempty"`
}

type SchedulingReport struct {
	EvaluatedAt    time.Time                   `json:"evaluated_at"`
	OpenTotal      int                         `json:"open_total"`
	ClaimableTotal int                         `json:"claimable_total"`
	CandidateCount int                         `json:"candidate_count"`
	Rows           []IntentScheduleExplanation `json:"rows"`
	LastDispatch   *IntentScheduleExplanation  `json:"last_dispatch,omitempty"`
	Status         ReadStatus                  `json:"read_status"`
}

func (s *ExplorationStore) rankedFrontier(limit int, now time.Time) ([]*Node, error) {
	candidateLimit := limit * 6
	if candidateLimit < 120 {
		candidateLimit = 120
	}
	if candidateLimit > 500 {
		candidateLimit = 500
	}
	candidates, err := s.Frontier(candidateLimit)
	if err != nil {
		return nil, err
	}
	if len(candidates) == candidateLimit {
		extra, err := s.frontierDivergentTail(candidates, limit)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, extra...)
	}
	ids := make([]int64, 0, len(candidates))
	for _, n := range candidates {
		ids = append(ids, n.ID)
	}
	routes, err := s.IntentRoutes(ids)
	if err != nil {
		return nil, err
	}
	active, err := s.DependencyActiveSources(ids)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	largest := 0
	for _, route := range routes {
		counts[route]++
		if counts[route] > largest {
			largest = counts[route]
		}
	}
	for _, n := range candidates {
		n.Schedule = scoreIntent(n, now, routes[n.ID], counts[routes[n.ID]], largest, active[n.ID])
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Schedule.FairWait != b.Schedule.FairWait {
			return a.Schedule.FairWait
		}
		if a.Schedule.FairWait && !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		if a.Schedule.Score != b.Schedule.Score {
			return a.Schedule.Score > b.Schedule.Score
		}
		return a.ID < b.ID
	})
	for i, n := range candidates {
		n.Schedule.Rank = i + 1
		if i > 0 {
			n.Schedule.WaitReasons = append(n.Schedule.WaitReasons, "earlier_candidates")
		}
		if n.Schedule.Components.Dependency < 0 {
			n.Schedule.WaitReasons = append(n.Schedule.WaitReasons, "advisory_dependency")
		}
	}
	return candidates, nil
}

func scoreIntent(n *Node, now time.Time, route string, count, largest int, activeDependency bool) *IntentScheduleExplanation {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	age := now.Sub(n.CreatedAt).Minutes()
	if n.CreatedAt.IsZero() || age < 0 {
		age = 0
	}
	aging := age
	if aging > 30 {
		aging = 30
	}
	c := ScheduleComponents{Priority: float64(n.Priority) * 100, Route: float64(largest-count) * 8, Aging: aging}
	if activeDependency {
		c.Dependency = -100
	}
	if resume, _ := p["resume_after_restart"].(bool); resume {
		c.Restart = 100000
	}
	if attempts, ok := p["attempts"].(float64); ok {
		c.Retries = -attempts * 12
	}
	reason := "score_then_id"
	fair := frontierOverdue(n, now)
	if fair {
		reason = "overdue_oldest_first"
	}
	return &IntentScheduleExplanation{IntentID: n.ID, Summary: payloadSummary(n.Payload), State: n.State, EvaluatedAt: now, Evaluated: true,
		Claimable: true, Score: c.Priority + c.Dependency + c.Restart + c.Route + c.Aging + c.Retries, Components: c, Route: route,
		RouteCount: count, LargestRouteCount: largest, WaitingSeconds: int64(age * 60), FairWait: fair, SelectionReason: reason}
}

func payloadSummary(raw json.RawMessage) string {
	var p struct {
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Summary
}

// A report uses a read snapshot; counts and score components never mix revisions.
// Preview always uses the production batch size even if the UI asks for more rows.
func (s *ExplorationStore) ScheduleReport(limit int, focusID int64) (*SchedulingReport, error) {
	if !s.InReadSnapshot() {
		var out *SchedulingReport
		err := s.ReadSnapshot(context.Background(), func(_ *DB, view *ExplorationStore) error {
			var err error
			out, err = view.ScheduleReport(limit, focusID)
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
	now := time.Now()
	ranked, err := s.rankedFrontier(DispatchBatchSize, now)
	if err != nil {
		return nil, err
	}
	snap, err := s.IntentDispatchSnapshot()
	if err != nil {
		return nil, err
	}
	out := &SchedulingReport{EvaluatedAt: now, OpenTotal: snap.Open, ClaimableTotal: snap.Claimable, CandidateCount: len(ranked), Rows: []IntentScheduleExplanation{}}
	if focusID > 0 {
		n, err := s.GetNode(focusID)
		if err != nil {
			return nil, err
		}
		if n == nil || n.Kind != KindIntent {
			return nil, fmt.Errorf("intent not found")
		}
		var saved struct {
			Snapshot *IntentScheduleExplanation `json:"dispatch_snapshot"`
		}
		_ = json.Unmarshal(n.Payload, &saved)
		if saved.Snapshot != nil && saved.Snapshot.IntentID == n.ID && !saved.Snapshot.EvaluatedAt.IsZero() && saved.Snapshot.Evaluated {
			out.LastDispatch = saved.Snapshot
		}
		for _, r := range ranked {
			if r.ID == focusID {
				out.Rows = append(out.Rows, *r.Schedule)
				break
			}
		}
		if len(out.Rows) == 0 {
			_ = s.PopulateEvidenceReadState([]*Node{n})
			out.Rows = append(out.Rows, s.unevaluatedSchedule(n, now))
		}
		out.Status = ReadSetStatus(1, 1, "")
		return out, nil
	}
	for _, n := range ranked {
		if len(out.Rows) >= limit {
			break
		}
		out.Rows = append(out.Rows, *n.Schedule)
	}
	if len(out.Rows) < limit {
		blocked, err := s.ListBlockedOpenIntents(limit - len(out.Rows))
		if err != nil {
			return nil, err
		}
		if len(blocked) > 0 {
			blockedIDs := make([]int64, 0, len(blocked))
			for _, b := range blocked {
				blockedIDs = append(blockedIDs, b.IntentID)
			}
			nodes, err := s.NodesByIDs(blockedIDs)
			if err != nil {
				return nil, err
			}
			_ = s.PopulateEvidenceReadState(nodes)
			byID := make(map[int64]*Node, len(nodes))
			for _, n := range nodes {
				if n != nil {
					byID[n.ID] = n
				}
			}
			for _, b := range blocked {
				if n := byID[b.IntentID]; n != nil {
					out.Rows = append(out.Rows, s.unevaluatedSchedule(n, now))
				}
			}
		}
	}
	out.Status = ReadSetStatus(int64(snap.Open), len(out.Rows), "scheduling?intent_id=<id>")
	return out, nil
}

func (s *ExplorationStore) unevaluatedSchedule(n *Node, now time.Time) IntentScheduleExplanation {
	age := now.Sub(n.CreatedAt).Seconds()
	if age < 0 {
		age = 0
	}
	reason := "not_open"
	claimable := false
	if n.State == "open" {
		// Classification only; no invented score outside the actual candidate window.
		var ready bool
		err := s.db.QueryRow(`SELECT `+intentClaimableSQL+` FROM exploration_nodes n WHERE n.id=$1 AND n.exploration_id=$2`, n.ID, s.expID).Scan(&ready)
		if err != nil {
			reason = "read_unknown"
		} else if ready {
			claimable = true
			reason = "outside_candidate_window"
		} else {
			reason = "strict_dependency"
		}
	}
	return IntentScheduleExplanation{IntentID: n.ID, Summary: payloadSummary(n.Payload), State: n.State, EvaluatedAt: now,
		Claimable: claimable, WaitingSeconds: int64(age), SelectionReason: reason, WaitReason: reason, WaitReasons: []string{reason},
		BlockedReason: n.BlockedReason, RequiresMissing: n.RequiresMissing}
}
