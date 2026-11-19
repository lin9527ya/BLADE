package server

import (
	"testing"

	"github.com/lin9527ya/blade/db"
)

// blocked-only（open>0, running=0, claimable=0）必须 kick 首轮规划，不能因为
// HasActiveIntent 看到 open 就静默等心跳。
func TestShouldKickPlanner(t *testing.T) {
	cases := []struct {
		name string
		in   db.IntentDispatch
		want bool
	}{
		{"running", db.IntentDispatch{Running: 1}, false},
		{"claimable", db.IntentDispatch{Open: 3, Claimable: 2}, false},
		{"blocked-only", db.IntentDispatch{Open: 3, Claimable: 0, Blocked: 3}, true},
		{"empty", db.IntentDispatch{}, true},
	}
	for _, c := range cases {
		if got := shouldKickPlanner(c.in); got != c.want {
			t.Fatalf("%s: shouldKickPlanner(%+v)=%v, want %v", c.name, c.in, got, c.want)
		}
	}
}
