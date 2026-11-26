// Package llmpool implements LLM failover ("轮询"): a Provider decorator that
// walks an ordered chain of LLM profiles and moves to the next one when the
// current one can't serve the request — out of credit, revoked key, rate-limited
// past the SDK's own retries, or down.
//
// The chain is built by the server from llm_profiles (active profile first, then
// by priority), so the whole process shares ONE chain and ONE circuit-breaker
// registry: when a task discovers a profile is out of credit, every other task
// skips it immediately.
package llmpool

import (
	"sync"
	"time"
)

// backoff is the cooling-off ladder, indexed by trip count: the 1st trip cools
// for a minute, the 2nd for five, everything after that for half an hour. A
// profile that is merely rate-limited recovers quickly; one that keeps failing
// stops being probed every round.
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// State is one profile's circuit-breaker state.
type State struct {
	Fails      int       // consecutive failures; reset by a success
	Trips      int       // total trips, indexes the backoff ladder
	OpenUntil  time.Time // zero / past = closed (usable) or half-open awaiting probe
	LastError  string
	LastAt     time.Time
	HalfOpen   bool      // true when Trips > 0 and OpenUntil has expired (awaiting probe)
	probeUntil time.Time // internal single-flight lease while a half-open probe is running
}

// Open reports whether the breaker is currently open (profile should be skipped).
func (s State) Open() bool { return time.Now().Before(s.OpenUntil) }

func (s State) isHalfOpenAt(now time.Time) bool {
	return s.Trips > 0 && !s.OpenUntil.IsZero() && !now.Before(s.OpenUntil)
}

// softTripAfter is the DEFAULT number of consecutive TRANSIENT failures (429 /
// 5xx / network) that trip the breaker. Deterministic failures (no credit, bad
// key) trip on the first regardless. Overridable via Registry.SetPolicy.
const softTripAfter = 3

const probeLeaseTimeout = 3 * time.Minute

// Registry holds the circuit-breaker state of every profile, keyed by profile id.
// It is process-wide and outlives any single Pool instance, so rebuilding the pool
// (saving an unrelated profile, toggling a setting) never clears what we've learned
// about which backends are broken.
type Registry struct {
	mu sync.Mutex
	m  map[int64]*State

	// persist / forget mirror state to the DB so a cooling-off window survives a
	// restart. Both may be nil (no DB); both are called off the hot path.
	persist func(id int64, st State)
	forget  func(id int64)

	// softTrip / cooldown are the operator-set overrides (0 = use the built-in
	// default / ladder). They live here rather than being read per failure because
	// Trip runs on the failure path of every request.
	softTrip int
	cooldown time.Duration
}

// SetPolicy overrides the breaker's two knobs. softTrip: how many consecutive
// transient failures trip it (0 = default softTripAfter; negative = transient
// failures never trip it, leaving only the deterministic ones). cooldown: a fixed
// cooling-off window (0 = the 1min/5min/30min ladder). Safe to call at any time.
func (r *Registry) SetPolicy(softTrip int, cooldown time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.softTrip, r.cooldown = softTrip, cooldown
}

// tripAfter is the effective consecutive-transient-failure threshold. Callers
// hold r.mu. A negative override yields 0, which Trip reads as "never soft-trip".
func (r *Registry) tripAfter() int {
	if r.softTrip == 0 {
		return softTripAfter
	}
	return max(r.softTrip, 0)
}

// coolFor is the cooling-off window for the trips-th trip (1-based): the fixed
// override when set, else the ladder (last rung repeats). Callers hold r.mu.
func (r *Registry) coolFor(trips int) time.Duration {
	if r.cooldown > 0 {
		return r.cooldown
	}
	if trips-1 < len(backoff) {
		return backoff[trips-1]
	}
	return backoff[len(backoff)-1]
}

// NewRegistry builds an empty registry. persist/forget may be nil.
func NewRegistry(persist func(id int64, st State), forget func(id int64)) *Registry {
	return &Registry{m: map[int64]*State{}, persist: persist, forget: forget}
}

// Restore seeds state loaded from the DB at startup (bypasses persistence).
func (r *Registry) Restore(id int64, st State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := st
	cp.probeUntil = time.Time{}
	cp.HalfOpen = cp.isHalfOpenAt(time.Now())
	r.m[id] = &cp
}

// PolicySnapshot returns the effective soft-trip threshold (0 = soft-tripping
// disabled), the cooldown ladder in seconds, and whether a fixed override is active.
func (r *Registry) PolicySnapshot() (softTrip int, ladderSecs []int, fixedCooldown bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	softTrip = r.tripAfter()
	if r.cooldown > 0 {
		s := max(int(r.cooldown.Round(time.Second)/time.Second), 1)
		return softTrip, []int{s}, true
	}
	secs := make([]int, len(backoff))
	for i, d := range backoff {
		secs[i] = int(d / time.Second)
	}
	return softTrip, secs, false
}

// Get returns a copy of one profile's state.
func (r *Registry) Get(id int64) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.m[id]; st != nil {
		cp := *st
		cp.HalfOpen = cp.isHalfOpenAt(time.Now())
		return cp
	}
	return State{}
}

// IsOpen reports whether a profile is in its cooling-off window.
func (r *Registry) IsOpen(id int64) bool { return r.Get(id).Open() }

// IsProbing reports whether a half-open profile currently has an active
// single-flight probe in progress.
func (r *Registry) IsProbing(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	st := r.m[id]
	return st != nil && st.isHalfOpenAt(now) && now.Before(st.probeUntil)
}

// TryBeginProbe attempts to claim the single-flight probe lease if id is
// currently Half-Open. For healthy/closed profiles it returns (false, true); for
// actively cooling profiles (false, false); for Half-Open profiles (true, true)
// for the winning caller and (true, false) for concurrent waiters.
func (r *Registry) TryBeginProbe(id int64) (halfOpen, acquired bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	st := r.m[id]
	if st == nil || st.Trips == 0 || st.OpenUntil.IsZero() {
		return false, true
	}
	if now.Before(st.OpenUntil) {
		return false, false
	}
	if now.Before(st.probeUntil) {
		return true, false
	}
	st.probeUntil = now.Add(probeLeaseTimeout)
	return true, true
}

// ReleaseProbe clears an active single-flight probe lease when a probe attempt
// stops without failing or succeeding the backend (e.g. 413 payload skip or cancel).
func (r *Registry) ReleaseProbe(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.m[id]; st != nil {
		st.probeUntil = time.Time{}
	}
}

// Pass records a successful call: clears the failure counters so a profile that
// recovers is fully trusted again (and drops the persisted row).
func (r *Registry) Pass(id int64) {
	r.mu.Lock()
	st := r.m[id]
	if st == nil || (st.Fails == 0 && st.Trips == 0 && st.OpenUntil.IsZero() && st.probeUntil.IsZero()) {
		r.mu.Unlock()
		return // already clean — nothing to write
	}
	delete(r.m, id)
	forget := r.forget
	r.mu.Unlock()
	if forget != nil {
		forget(id)
	}
}

// Trip records a failed call. hard=true marks a deterministic failure (no credit,
// invalid key, missing model) which opens the breaker immediately; hard=false is a
// transient one (429 / 5xx / network) that needs softTripAfter in a row (except
// during Half-Open probe, where 1 failure immediately escalates cooldown).
// Returns true when this call is what opened the breaker, so the caller can log it once.
func (r *Registry) Trip(id int64, errMsg string, hard bool) (tripped bool) {
	r.mu.Lock()
	now := time.Now()
	st := r.m[id]
	if st == nil {
		st = &State{}
		r.m[id] = st
	}
	wasHalfOpen := st.isHalfOpenAt(now)
	st.probeUntil = time.Time{}
	st.Fails++
	st.LastError = errMsg
	st.LastAt = now
	softTrip := r.tripAfter()
	if hard || (softTrip > 0 && (st.Fails >= softTrip || wasHalfOpen)) {
		st.Trips++
		st.OpenUntil = now.Add(r.coolFor(st.Trips))
		st.Fails = 0 // counted into this trip; next probe starts clean
		tripped = true
	}
	snap := *st
	snap.HalfOpen = snap.isHalfOpenAt(now)
	persist := r.persist
	r.mu.Unlock()
	if persist != nil {
		persist(id, snap)
	}
	return tripped
}

// Reset clears one profile's state — the UI's "立即恢复" action.
func (r *Registry) Reset(id int64) {
	r.mu.Lock()
	delete(r.m, id)
	forget := r.forget
	r.mu.Unlock()
	if forget != nil {
		forget(id)
	}
}

// Snapshot returns a copy of all tracked state, for the status API.
func (r *Registry) Snapshot() map[int64]State {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := make(map[int64]State, len(r.m))
	for id, st := range r.m {
		cp := *st
		cp.HalfOpen = cp.isHalfOpenAt(now)
		out[id] = cp
	}
	return out
}
