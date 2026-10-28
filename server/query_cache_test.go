package server

import (
	"context"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestQueryCacheServesFreshCopiesWithoutDB(t *testing.T) {
	s := &Server{}
	s.queryCache.lastActivity = map[int64]int64{1: 42}
	s.queryCache.lastActivityAt = time.Now()
	if got := s.cachedLastActivity(context.Background())[1]; got != 42 {
		t.Fatalf("last activity cache = %d", got)
	}
	s.queryCache.dashboardTokens = db.DashboardTaskTokens{
		ByProfile: []db.DashboardTaskTokenProfile{{InputTokens: 7}},
	}
	s.queryCache.dashboardTokensAt = time.Now()
	tokens, err := s.cachedDashboardTokens(context.Background())
	if err != nil || len(tokens.ByProfile) != 1 || tokens.ByProfile[0].InputTokens != 7 {
		t.Fatalf("dashboard token cache = %+v err=%v", tokens, err)
	}
}
