package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/lin9527ya/blade/intercept"
)

const maxBatchDecide = 100

type batchDecideResult struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// interceptBatchDecide resolves several pending requests with the same decision.
// It reuses Decide per id, so target pins, decided_by audit and waiter wake-up are
// identical to the single-item endpoint. Partial failure is a 200 with per-id
// results; a failed id is never reported as decided.
func (s *Server) interceptBatchDecide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs      []int64 `json:"ids"`
		Decision string  `json:"decision"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Decision != "allowed" && req.Decision != "denied" {
		writeErr(w, 400, "decision 必须是 allowed 或 denied")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > maxBatchDecide {
		writeErr(w, 400, fmt.Sprintf("ids 数量必须为 1-%d", maxBatchDecide))
		return
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 {
			writeErr(w, 400, "ids 必须是正整数")
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	allowed := req.Decision == "allowed"
	actor := actorFrom(r)
	if actor == "" {
		actor = "admin"
	}
	results := make([]batchDecideResult, 0, len(ids))
	decided, failed := 0, 0
	for _, id := range ids {
		item := batchDecideResult{ID: id}
		if err := s.m.interceptor.DecideWithActor(id, allowed, actor, "batch"); err != nil {
			item.Error = err.Error()
			failed++
		} else {
			item.OK = true
			decided++
		}
		results = append(results, item)
	}
	writeJSON(w, 200, map[string]any{"decided": decided, "failed": failed, "results": results})
}

// interceptAlwaysAllow creates (or returns) an argv allow rule derived from a
// pending Bash request. The pending request is not decided; the caller still has
// to approve it (or it remains pending).
func (s *Server) interceptAlwaysAllow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad pending id")
		return
	}
	p, err := s.m.pg.GetInterceptPending(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "not found")
		return
	}
	if p.Status != "pending" {
		writeErr(w, 409, "审批已处理")
		return
	}
	if p.ToolName != "Bash" {
		writeErr(w, 400, "always-allow 只支持 Bash 审批")
		return
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(p.ToolInput, &in); err != nil || strings.TrimSpace(in.Command) == "" {
		writeErr(w, 400, "审批记录里没有可解析的 Bash 命令")
		return
	}
	prefix, err := intercept.AlwaysAllowPrefix(in.Command)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tokens, err := intercept.ArgvPrefix(in.Command)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	patternBytes, _ := json.Marshal(tokens)
	pattern := string(patternBytes)
	rules, err := s.m.pg.ListInterceptRules()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, rule := range rules {
		if rule.Enabled && rule.MatchTarget == "tool_input" && rule.MatchType == "argv" &&
			rule.Action == "allow" && rule.Pattern == pattern {
			writeJSON(w, 200, map[string]any{"rule": rule, "created": false, "prefix": prefix})
			return
		}
	}
	rule, err := s.m.pg.CreateInterceptRule(
		"always-allow: "+prefix, "tool_input", "argv", pattern, "allow", "",
		intercept.AlwaysAllowPriority, true, true, 60, "deny",
	)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.m.interceptor.Invalidate()
	writeJSON(w, 200, map[string]any{"rule": rule, "created": true, "prefix": prefix})
}
