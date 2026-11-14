package server

import (
	"fmt"
	"strconv"
	"strings"
)

// compactorLLMProfileID returns the optional independent LLM profile used for
// context/cold-node summaries. 0 = follow the task chain / role binding.
func (s *Server) compactorLLMProfileID() int64 {
	if s == nil || s.m == nil || s.m.pg == nil {
		return 0
	}
	raw, ok, err := s.m.pg.GetSetting(settingCompactorLLMProfile)
	if err != nil || !ok {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// attackChainContinuationEnabled controls the non-blocking planner hint emitted
// after a new high/critical finding (default on). It never creates intents or
// gates completion; the planner still decides the next hop.
func (s *Server) attackChainContinuationEnabled() bool {
	if s == nil || s.m == nil || s.m.pg == nil {
		return false
	}
	return s.m.pg.GetBool(settingAttackChainContinuation, true)
}

// emptyReportPatrolEnabled controls the optional reporter patrol for confirmed
// findings whose detailed report is still empty (default off: it spends LLM tokens).
func (s *Server) emptyReportPatrolEnabled() bool {
	if s == nil || s.m == nil || s.m.pg == nil {
		return false
	}
	return s.m.pg.GetBool(settingEmptyReportPatrol, false)
}

// validateCompactorProfile rejects a stale/invalid profile id before it is
// persisted; clearing the setting is always allowed.
func (s *Server) validateCompactorProfile(id int64) error {
	if id <= 0 || s == nil || s.m == nil || s.m.pg == nil {
		return nil
	}
	p, err := s.m.pg.ProfileByID(id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("压缩器 LLM 配置不存在")
	}
	return nil
}
