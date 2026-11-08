package agent

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const cacheDiagnosticLimit = 256
const cacheDiagnosticTTL = 30 * time.Minute

var cacheDiagnosticUntil atomic.Int64

// CacheTraceStatus describes an opt-in, process-local diagnostic window.
type CacheTraceStatus struct {
	Enabled     bool       `json:"enabled"`
	Environment bool       `json:"environment"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Limit       int        `json:"limit"`
}

type CacheUsageFrame struct {
	Event  string         `json:"event"`
	Values map[string]any `json:"values"`
}

// CacheDiagnostic contains structure metadata only, never request/response bodies or headers.
type CacheDiagnostic struct {
	ID               uint64            `json:"id"`
	At               time.Time         `json:"at"`
	Session          string            `json:"session"`
	Model            string            `json:"model"`
	Previous         bool              `json:"previous"`
	SystemSame       bool              `json:"system_same"`
	ToolsSame        bool              `json:"tools_same"`
	SharedMessages   int               `json:"shared_messages"`
	Messages         int               `json:"messages"`
	PreviousMessages int               `json:"previous_messages"`
	System           cachePart         `json:"system"`
	Tools            cachePart         `json:"tools"`
	Breakpoints      []string          `json:"breakpoints"`
	Frames           []CacheUsageFrame `json:"frames"`
	ObservedFrames   int               `json:"observed_frames"`
	Status           int               `json:"status"`
	Complete         bool              `json:"complete"`
	Truncated        bool              `json:"truncated"`
}

var cacheDiagnostics = struct {
	sync.Mutex
	rows []CacheDiagnostic
}{}

func CacheTraceState() CacheTraceStatus {
	env := os.Getenv(CacheTraceEnv) == "1"
	until := cacheDiagnosticUntil.Load()
	state := CacheTraceStatus{Enabled: env || until > time.Now().UnixNano(), Environment: env, Limit: cacheDiagnosticLimit}
	if until > time.Now().UnixNano() {
		t := time.Unix(0, until).UTC()
		state.ExpiresAt = &t
	}
	return state
}

// SetCacheTraceWindow enables a short-lived capture without changing provider behavior or configuration.
func SetCacheTraceWindow(window time.Duration) {
	if window <= 0 {
		cacheDiagnosticUntil.Store(0)
		ClearCacheDiagnostics()
		return
	}
	if window > 30*time.Minute {
		window = 30 * time.Minute
	}
	cacheDiagnosticUntil.Store(time.Now().Add(window).UnixNano())
}

func ClearCacheDiagnostics() {
	cacheTraceHistory.Lock()
	cacheTraceHistory.last = map[string]cacheSignature{}
	cacheTraceHistory.Unlock()
	cacheDiagnostics.Lock()
	cacheDiagnostics.rows = nil
	cacheDiagnostics.Unlock()
}

func addCacheDiagnostic(row CacheDiagnostic) {
	if !CacheTraceState().Enabled {
		return
	}
	cacheDiagnostics.Lock()
	defer cacheDiagnostics.Unlock()
	if len(cacheDiagnostics.rows) >= cacheDiagnosticLimit {
		copy(cacheDiagnostics.rows, cacheDiagnostics.rows[1:])
		cacheDiagnostics.rows = cacheDiagnostics.rows[:cacheDiagnosticLimit-1]
	}
	cacheDiagnostics.rows = append(cacheDiagnostics.rows, row)
}

func updateCacheDiagnostic(id uint64, update func(*CacheDiagnostic)) {
	if id == 0 {
		return
	}
	cacheDiagnostics.Lock()
	defer cacheDiagnostics.Unlock()
	for i := len(cacheDiagnostics.rows) - 1; i >= 0; i-- {
		if cacheDiagnostics.rows[i].ID == id {
			update(&cacheDiagnostics.rows[i])
			return
		}
	}
}

func CacheDiagnosticSnapshot(task, session string, limit int) []CacheDiagnostic {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	cutoff := time.Now().Add(-cacheDiagnosticTTL)
	cacheDiagnostics.Lock()
	defer cacheDiagnostics.Unlock()
	out := make([]CacheDiagnostic, 0)
	for i := len(cacheDiagnostics.rows) - 1; i >= 0 && len(out) < limit; i-- {
		row := cacheDiagnostics.rows[i]
		if row.At.Before(cutoff) || (task != "" && !strings.HasPrefix(row.Session, "exp"+task+"-")) || (session != "" && row.Session != session) {
			continue
		}
		row.Breakpoints = append([]string{}, row.Breakpoints...)
		row.Frames = append([]CacheUsageFrame{}, row.Frames...)
		for j := range row.Frames {
			values := make(map[string]any, len(row.Frames[j].Values))
			for k, v := range row.Frames[j].Values {
				values[k] = v
			}
			row.Frames[j].Values = values
		}
		out = append(out, row)
	}
	return out
}
