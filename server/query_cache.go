package server

import (
	"context"
	"sync"
	"time"

	"github.com/lin9527ya/blade/db"
)

// These two aggregates are whole-database scans but change slowly relative to
// list/dashboard polling. A short shared TTL avoids every duration sort and
// every five-second dashboard poll repeating the same query.
const (
	lastActivityCacheTTL = 5 * time.Second
	dashboardTokenTTL    = 15 * time.Second
)

type queryCacheState struct {
	mu sync.Mutex

	lastActivity   map[int64]int64
	lastActivityAt time.Time

	dashboardTokens   db.DashboardTaskTokens
	dashboardTokensAt time.Time
}

func (s *Server) cachedLastActivity(ctx context.Context) map[int64]int64 {
	s.queryCache.mu.Lock()
	if s.queryCache.lastActivity != nil && time.Since(s.queryCache.lastActivityAt) < lastActivityCacheTTL {
		value := s.queryCache.lastActivity
		s.queryCache.mu.Unlock()
		return value
	}
	stale := s.queryCache.lastActivity
	s.queryCache.mu.Unlock()

	if s.m == nil {
		return stale
	}
	pg := s.m.PG()
	if pg == nil {
		return stale
	}
	value, err := pg.LastActivityAllContext(ctx)
	if err != nil {
		return stale
	}
	s.queryCache.mu.Lock()
	s.queryCache.lastActivity = value
	s.queryCache.lastActivityAt = time.Now()
	s.queryCache.mu.Unlock()
	return value
}

func (s *Server) cachedDashboardTokens(ctx context.Context) (db.DashboardTaskTokens, error) {
	s.queryCache.mu.Lock()
	if !s.queryCache.dashboardTokensAt.IsZero() && time.Since(s.queryCache.dashboardTokensAt) < dashboardTokenTTL {
		value := s.queryCache.dashboardTokens
		s.queryCache.mu.Unlock()
		return value, nil
	}
	stale := s.queryCache.dashboardTokens
	hadValue := !s.queryCache.dashboardTokensAt.IsZero()
	s.queryCache.mu.Unlock()

	if s.m == nil {
		return stale, nil
	}
	pg := s.m.PG()
	if pg == nil {
		return stale, nil
	}
	value, err := pg.DashboardTaskTokensContext(ctx)
	if err != nil {
		if hadValue {
			return stale, nil
		}
		return db.DashboardTaskTokens{
			ByProfile: []db.DashboardTaskTokenProfile{},
			Daily:     []db.DashboardTaskTokenDay{},
		}, err
	}
	s.queryCache.mu.Lock()
	s.queryCache.dashboardTokens = value
	s.queryCache.dashboardTokensAt = time.Now()
	s.queryCache.mu.Unlock()
	return value, nil
}
