package llmpool

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"iter"
	"log"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/transcript"
)

// Member is one LLM profile in the chain, already built into a provider (wrapped
// with the recorder by the caller, so a failed attempt is still recorded under
// its own profile name).
type Member struct {
	ID       int64  // llm_profiles.id
	Name     string // profile name, for logs / UI
	Model    string
	Format   string // "anthropic" | "openai"
	Priority int    // the profile's configured priority (display only)
	Active   bool   // is_default (display only)
	// Rank is the ordering key the caller assigned: higher goes first, and members
	// sharing a Rank take turns leading (load-spreading across duplicate keys).
	// The caller encodes "active profile heads the chain" as a Rank above every
	// user-settable priority, so this type needs no policy of its own.
	Rank int
	// WindowTokens is the profile's context window in tokens. A member whose
	// window can't hold the request is skipped rather than made to fail on it.
	WindowTokens int
	// ProfileConfigured indicates runtime option fields below came from a real
	// profile config (zero-valued test Members leave request options untouched).
	ProfileConfigured    bool
	NonStreaming         bool
	MaxTokens            int
	CacheTTL             string
	PromptCacheRetention string
	PreserveThinking     bool
	Retry                StreamPolicy
	Prov                 llm.Provider
}

// RankActive is the Rank the caller gives the chain head (the active profile, or
// an explicitly bound one) so it always outranks any configured priority.
const RankActive = int(^uint(0)>>1) - 1

// ErrExhausted is returned when every member of the chain failed.
var ErrExhausted = errors.New("LLM 轮询：所有配置均不可用")

type stickyEntry struct {
	memberID int64
	seq      uint64
}

// Pool is an llm.Provider that fails over across an ordered chain of members.
// It is safe for concurrent use: members are immutable after construction and
// all mutable state lives in the shared Registry.
type Pool struct {
	members   []*Member // in chain order (active first, then priority DESC)
	health    *Registry
	rr        atomic.Uint64 // rotates the starting point within an equal-priority group when no session id exists
	stickySeq atomic.Uint64
	stickyMu  sync.Mutex
	sticky    map[string]stickyEntry // sessionID -> last succeeded Member.ID for prefix KV cache affinity
}

const maxStickySessions = 4096

// HandlesStreamRetry reports that Pool applies per-member StreamAttempts retries
// internally before failing over, avoiding outer whole-chain multiplication.
func (p *Pool) HandlesStreamRetry() bool { return true }

// StickyCounts returns the number of active sticky session pins per member ID.
func (p *Pool) StickyCounts() map[int64]int {
	out := make(map[int64]int)
	if p == nil {
		return out
	}
	p.stickyMu.Lock()
	defer p.stickyMu.Unlock()
	for _, e := range p.sticky {
		out[e.memberID]++
	}
	return out
}

// New builds a Pool over members (already in chain order). Returns nil when the
// chain is empty. A single-member chain is still a valid Pool — it just behaves
// exactly like the bare provider.
func New(members []*Member, health *Registry) *Pool {
	if len(members) == 0 {
		return nil
	}
	if health == nil {
		health = NewRegistry(nil, nil)
	}
	return &Pool{members: members, health: health}
}

// Members returns the chain in order (read-only).
func (p *Pool) Members() []*Member { return p.members }

// Head returns the first member of the chain.
func (p *Pool) Head() *Member { return p.members[0] }

// CurrentMember returns the member preferred right now for sid (honoring sticky
// affinity when healthy, otherwise the first non-tripped member, falling back
// to Head). Used by outer resolvers (e.g. ChatAgent) so runtime options follow
// failover rather than staying pinned to the tripped head profile.
func (p *Pool) CurrentMember(sid string) *Member {
	if p == nil || len(p.members) == 0 {
		return nil
	}
	if sid != "" {
		if pinID := p.pinnedMember(sid); pinID > 0 {
			for _, m := range p.members {
				if m.ID == pinID && !p.health.IsOpen(m.ID) {
					return m
				}
			}
		}
	}
	for _, m := range p.members {
		if !p.health.IsOpen(m.ID) {
			return m
		}
	}
	return p.members[0]
}

// PreservesThinking delegates thinking-block preservation to the currently
// preferred member so upstream agents stay aligned after failover.
func (p *Pool) PreservesThinking() bool {
	if m := p.CurrentMember(""); m != nil {
		if m.ProfileConfigured {
			return m.PreserveThinking
		}
		if pt, ok := m.Prov.(interface{ PreservesThinking() bool }); ok {
			return pt.PreservesThinking()
		}
	}
	return false
}

func (m *Member) adaptedRequest(req llm.CompletionRequest) llm.CompletionRequest {
	if !m.ProfileConfigured {
		return req
	}
	out := req
	out.CacheTTL = m.CacheTTL
	out.PromptCacheRetention = m.PromptCacheRetention
	if out.MaxTokens == 0 && m.MaxTokens > 0 {
		out.MaxTokens = m.MaxTokens
	}
	return out
}

func emitCompleteAsStream(yield func(llm.StreamEvent, error) bool, msg llm.Message, stopReason string, usage llm.Usage) bool {
	if !yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil) {
		return false
	}
	for _, b := range msg.Content {
		switch b.Type {
		case llm.BlockThinking:
			if b.Thinking != "" && !yield(llm.StreamEvent{Type: llm.SEThinkingDelta, Text: b.Thinking}, nil) {
				return false
			}
			if b.Signature != "" && !yield(llm.StreamEvent{Type: llm.SEThinkingSignature, Text: b.Signature}, nil) {
				return false
			}
		case llm.BlockText:
			if b.Text != "" && !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: b.Text}, nil) {
				return false
			}
		case llm.BlockToolUse:
			if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: b.ID, ToolName: b.Name}, nil) {
				return false
			}
			if len(b.Input) > 0 && !yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(b.Input)}, nil) {
				return false
			}
		}
	}
	if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stopReason, Usage: usage}, nil) {
		return false
	}
	return yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
}

func (p *Pool) tryEnterMember(order []*Member, idx, minWindow int) (skip, acquiredProbe bool) {
	m := order[idx]
	halfOpen, acquired := p.health.TryBeginProbe(m.ID)
	if !halfOpen {
		return false, false
	}
	if acquired {
		return false, true
	}
	// Another goroutine is actively half-open probing this backend; skip it
	// whenever any healthy/eligible alternative remains in the ordered chain.
	for _, cand := range order[idx+1:] {
		if minWindow > 0 && cand.WindowTokens > 0 && cand.WindowTokens <= minWindow {
			continue
		}
		if !p.health.IsOpen(cand.ID) && !p.health.IsProbing(cand.ID) {
			return true, false
		}
	}
	return false, false
}

func (m *Member) hasStreamRetryConfig() bool {
	return m.ProfileConfigured || m.Retry != (StreamPolicy{})
}

func hasOtherFormat(rest []*Member, minWindow int, curFmt string) bool {
	cf := normFormat(curFmt)
	for _, m := range rest {
		if minWindow > 0 && m.WindowTokens > 0 && m.WindowTokens <= minWindow {
			continue
		}
		if normFormat(m.Format) != cf {
			return true
		}
	}
	return false
}

// Stream implements llm.Provider with failover.
//
// The one hard rule: a member may only be abandoned BEFORE it has yielded any
// event. Once text or a tool_use has reached the caller, re-sending the same
// request to another model would duplicate output and corrupt the conversation
// history — so a mid-stream failure is surfaced as-is and left to the agent
// harness's resume logic. Fortunately the failures this exists for (402 no
// credit, 401 bad key, 429, 5xx) all surface during request establishment,
// before the body is read, so they always land in the safe window.
func (p *Pool) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	ctx, _ = WithRetryHint(ctx)
	sid := sessionKey(ctx, req)
	order, est := p.orderFor(sid, req)
	return func(yield func(llm.StreamEvent, error) bool) {
		if len(order) == 0 {
			yield(llm.StreamEvent{}, fmt.Errorf("%w：请求估算约 %d tokens，超过所有配置的上下文窗口上限", ErrExhausted, est))
			return
		}
		var lastErr error
		minWindow := 0
		for i, m := range order {
			if minWindow > 0 && m.WindowTokens > 0 && m.WindowTokens <= minWindow {
				continue
			}
			skip, probeAcquired := p.tryEnterMember(order, i, minWindow)
			if skip {
				continue
			}
			releaseProbe := func() {
				if probeAcquired {
					p.health.ReleaseProbe(m.ID)
					probeAcquired = false
				}
			}
			emitted := false
			var failed error
			mReq := m.adaptedRequest(req)
			tag := "llmpool:" + m.Name
			if m.NonStreaming {
				var (
					msg   llm.Message
					sr    string
					usage llm.Usage
					err   error
				)
				if m.hasStreamRetryConfig() {
					msg, sr, usage, err = RunCompleteWithRetry(ctx, m.Prov, mReq, m.Retry, tag, nil)
				} else {
					msg, sr, usage, err = m.Prov.Complete(ctx, mReq)
				}
				if err != nil {
					if shouldFailover(ctx, err) || (isCrossFormatCompatError(err) && hasOtherFormat(order[i+1:], minWindow, m.Format)) {
						failed = err
					} else {
						if ctx.Err() != nil {
							releaseProbe()
						} else {
							p.health.Pass(m.ID)
						}
						yield(llm.StreamEvent{}, err)
						return
					}
				} else {
					p.health.Pass(m.ID)
					p.pinSession(sid, m.ID)
					emitCompleteAsStream(yield, msg, sr, usage)
					return
				}
			} else {
				var seq iter.Seq2[llm.StreamEvent, error]
				if m.hasStreamRetryConfig() {
					seq = RunStreamWithRetry(ctx, m.Prov, mReq, m.Retry, tag, nil)
				} else {
					seq = m.Prov.Stream(ctx, mReq)
				}
				for ev, err := range seq {
					if err != nil && !emitted && (shouldFailover(ctx, err) || (isCrossFormatCompatError(err) && hasOtherFormat(order[i+1:], minWindow, m.Format))) {
						failed = err
						break // safe window: nothing reached the caller yet
					}
					emitted = true
					if !yield(ev, err) {
						releaseProbe()
						return // caller stopped consuming (cancel / early exit)
					}
					if err != nil {
						if ctx.Err() != nil {
							releaseProbe()
						} else {
							p.health.Pass(m.ID)
						}
						return // terminal error already handed to the caller
					}
				}
				if failed == nil {
					p.health.Pass(m.ID) // completed cleanly
					p.pinSession(sid, m.ID)
					return
				}
			}
			lastErr = failed
			code := statusOf(failed)
			if code == 413 {
				releaseProbe()
				if m.WindowTokens > minWindow {
					minWindow = m.WindowTokens
				}
				log.Printf("[llmpool] %q(%s) 返回 413（载荷超限），跳过等长或更小窗口成员（不熔断）", m.Name, m.Model)
				continue
			}
			if isCrossFormatCompatError(failed) && hasOtherFormat(order[i+1:], minWindow, m.Format) {
				releaseProbe()
				if next := nextEligible(order[i+1:], minWindow); next != nil {
					log.Printf("[llmpool] %q(%s) 协议参数不兼容(400)，不熔断切换 → %q(%s)", m.Name, m.Model, next.Name, next.Model)
				}
				continue
			}
			hard := isHardFailure(failed)
			if p.health.Trip(m.ID, trimErr(failed), hard) {
				log.Printf("[llmpool] 配置 %q(%s) 已熔断：%s", m.Name, m.Model, trimErr(failed))
			}
			if next := nextEligible(order[i+1:], minWindow); next != nil {
				log.Printf("[llmpool] LLM 故障转移：%q(%s) → %q(%s)，原因：%s",
					m.Name, m.Model, next.Name, next.Model, trimErr(failed))
			}
		}
		if lastErr == nil {
			lastErr = ErrExhausted
		}
		log.Printf("[llmpool] 轮询链已耗尽(%d 个配置全部失败)，最后错误：%s", len(order), trimErr(lastErr))
		yield(llm.StreamEvent{}, fmt.Errorf("%w：%v", ErrExhausted, lastErr))
	}
}

// Complete implements llm.Provider with failover for non-streaming calls. A
// non-streaming request is atomic — it never delivers partial output — so every
// failover-eligible failure lands in the safe window and the next member can be
// tried without risk of duplicated output. Mirrors Stream's health-tripping and
// chain-exhaustion behavior.
func (p *Pool) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	ctx, _ = WithRetryHint(ctx)
	sid := sessionKey(ctx, req)
	order, est := p.orderFor(sid, req)
	if len(order) == 0 {
		return llm.Message{}, "", llm.Usage{}, fmt.Errorf("%w：请求估算约 %d tokens，超过所有配置的上下文窗口上限", ErrExhausted, est)
	}
	var lastErr error
	minWindow := 0
	for i, m := range order {
		if minWindow > 0 && m.WindowTokens > 0 && m.WindowTokens <= minWindow {
			continue
		}
		skip, probeAcquired := p.tryEnterMember(order, i, minWindow)
		if skip {
			continue
		}
		releaseProbe := func() {
			if probeAcquired {
				p.health.ReleaseProbe(m.ID)
				probeAcquired = false
			}
		}
		mReq := m.adaptedRequest(req)
		var (
			msg   llm.Message
			sr    string
			usage llm.Usage
			err   error
		)
		if m.hasStreamRetryConfig() {
			msg, sr, usage, err = RunCompleteWithRetry(ctx, m.Prov, mReq, m.Retry, "llmpool:"+m.Name, nil)
		} else {
			msg, sr, usage, err = m.Prov.Complete(ctx, mReq)
		}
		if err == nil {
			p.health.Pass(m.ID)
			p.pinSession(sid, m.ID)
			return msg, sr, usage, nil
		}
		if !shouldFailover(ctx, err) && !(isCrossFormatCompatError(err) && hasOtherFormat(order[i+1:], minWindow, m.Format)) {
			if ctx.Err() != nil {
				releaseProbe()
			} else {
				p.health.Pass(m.ID)
			}
			return llm.Message{}, "", llm.Usage{}, err
		}
		lastErr = err
		if statusOf(err) == 413 {
			releaseProbe()
			if m.WindowTokens > minWindow {
				minWindow = m.WindowTokens
			}
			log.Printf("[llmpool] %q(%s) 返回 413（载荷超限），跳过等长或更小窗口成员（不熔断）", m.Name, m.Model)
			continue
		}
		if isCrossFormatCompatError(err) && hasOtherFormat(order[i+1:], minWindow, m.Format) {
			releaseProbe()
			if next := nextEligible(order[i+1:], minWindow); next != nil {
				log.Printf("[llmpool] %q(%s) 协议参数不兼容(400)，不熔断切换 → %q(%s)", m.Name, m.Model, next.Name, next.Model)
			}
			continue
		}
		hard := isHardFailure(err)
		if p.health.Trip(m.ID, trimErr(err), hard) {
			log.Printf("[llmpool] 配置 %q(%s) 已熔断：%s", m.Name, m.Model, trimErr(err))
		}
		if next := nextEligible(order[i+1:], minWindow); next != nil {
			log.Printf("[llmpool] LLM 故障转移：%q(%s) → %q(%s)，原因：%s",
				m.Name, m.Model, next.Name, next.Model, trimErr(err))
		}
	}
	if lastErr == nil {
		lastErr = ErrExhausted
	}
	log.Printf("[llmpool] 轮询链已耗尽(%d 个配置全部失败)，最后错误：%s", len(order), trimErr(lastErr))
	return llm.Message{}, "", llm.Usage{}, fmt.Errorf("%w：%v", ErrExhausted, lastErr)
}

func nextEligible(rest []*Member, minWindow int) *Member {
	for _, m := range rest {
		if minWindow > 0 && m.WindowTokens > 0 && m.WindowTokens <= minWindow {
			continue
		}
		return m
	}
	return nil
}

// orderFor picks the members to try, in order: skip those in a cooling-off window
// (and half-open backends already being probed elsewhere while healthy ones exist)
// and those whose context window can't hold this request, rotate within equal-rank
// groups with sticky affinity, and keep same-format profiles ahead of cross-format
// ones so prompt KV cache stays hot. If every fitting member is tripped, probes
// ordered by earliest cooldown recovery; never forces a guaranteed-400 over-window request.
func (p *Pool) orderFor(sid string, req llm.CompletionRequest) ([]*Member, int) {
	est := estimateTokens(req)
	var open, fitting []*Member
	for _, m := range p.members {
		if m.WindowTokens > 0 && est > m.WindowTokens {
			continue // would 400 on length — not a useful target
		}
		fitting = append(fitting, m)
		if p.health.IsOpen(m.ID) || p.health.IsProbing(m.ID) {
			continue
		}
		open = append(open, m)
	}
	if len(open) == 0 {
		if len(fitting) == 0 {
			return nil, est
		}
		return p.allTrippedOrder(fitting), est
	}
	var rotated []*Member
	if sid == "" {
		rotated = rotateGroups(open, p.rr.Add(1)-1)
	} else {
		rotated = rotateGroupsSticky(open, sessionHash(sid), p.pinnedMember(sid))
	}
	return preferSameFormat(rotated, p.preferredFormat(sid, open)), est
}

func (p *Pool) allTrippedOrder(fitting []*Member) []*Member {
	out := slices.Clone(fitting)
	states := make(map[int64]State, len(out))
	probing := make(map[int64]bool, len(out))
	for _, m := range out {
		states[m.ID] = p.health.Get(m.ID)
		probing[m.ID] = p.health.IsProbing(m.ID)
	}
	tierOf := func(m *Member) (tier int, until time.Time) {
		st := states[m.ID]
		if !st.Open() {
			if probing[m.ID] {
				return 2, st.OpenUntil
			}
			return 0, st.OpenUntil // idle half-open ready to probe
		}
		if isHardFailureMsg(st.LastError) {
			return 3, st.OpenUntil
		}
		return 1, st.OpenUntil // soft trip cooling; try earliest-expiring first
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, ui := tierOf(out[i])
		tj, uj := tierOf(out[j])
		if ti != tj {
			return ti < tj
		}
		if !ui.Equal(uj) {
			return ui.Before(uj)
		}
		return false
	})
	return out
}

func isHardFailureMsg(msg string) bool {
	if msg == "" {
		return false
	}
	return isHardFailure(errors.New(msg))
}

func normFormat(f string) string {
	return strings.ToLower(strings.TrimSpace(f))
}

func (p *Pool) preferredFormat(sid string, open []*Member) string {
	if sid != "" {
		if pinID := p.pinnedMember(sid); pinID > 0 {
			for _, m := range open {
				if m.ID == pinID && normFormat(m.Format) != "" {
					return normFormat(m.Format)
				}
			}
		}
	}
	if len(p.members) > 0 && normFormat(p.members[0].Format) != "" {
		return normFormat(p.members[0].Format)
	}
	return ""
}

func preferSameFormat(in []*Member, pref string) []*Member {
	if pref == "" || len(in) <= 1 {
		return in
	}
	var same, diff []*Member
	for _, m := range in {
		mf := normFormat(m.Format)
		if mf == "" || mf == pref {
			same = append(same, m)
		} else {
			diff = append(diff, m)
		}
	}
	if len(same) == 0 || len(diff) == 0 {
		return in
	}
	out := make([]*Member, 0, len(in))
	out = append(out, same...)
	out = append(out, diff...)
	return out
}

func sessionKey(ctx context.Context, req llm.CompletionRequest) string {
	if sid := transcript.SessionIDFrom(ctx); sid != "" {
		return sid
	}
	return req.PromptCacheKey
}

func sessionHash(sid string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(sid))
	return h.Sum64()
}

func (p *Pool) pinnedMember(sid string) int64 {
	if sid == "" {
		return 0
	}
	p.stickyMu.Lock()
	defer p.stickyMu.Unlock()
	return p.sticky[sid].memberID
}

func (p *Pool) pinSession(sid string, memberID int64) {
	if sid == "" || memberID <= 0 {
		return
	}
	seq := p.stickySeq.Add(1)
	p.stickyMu.Lock()
	defer p.stickyMu.Unlock()
	if p.sticky == nil {
		p.sticky = make(map[string]stickyEntry, 64)
	} else if _, exists := p.sticky[sid]; !exists && len(p.sticky) >= maxStickySessions {
		// Evict the oldest 25% of sticky pins instead of wiping all 4096 active sessions.
		type kv struct {
			k   string
			seq uint64
		}
		entries := make([]kv, 0, len(p.sticky))
		for k, v := range p.sticky {
			entries = append(entries, kv{k: k, seq: v.seq})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
		drop := max(len(entries)/4, 1)
		for i := range drop {
			delete(p.sticky, entries[i].k)
		}
	}
	p.sticky[sid] = stickyEntry{memberID: memberID, seq: seq}
}

// rotateGroups rotates each run of equal-ranked members by n, so profiles sharing
// a priority take turns going first (free load-spreading across duplicate keys).
// The chain head has its own Rank and always stays at the front.
func rotateGroups(in []*Member, n uint64) []*Member {
	return rotateGroupsSticky(in, n, 0)
}

func rotateGroupsSticky(in []*Member, n uint64, stickyID int64) []*Member {
	out := make([]*Member, 0, len(in))
	for i := 0; i < len(in); {
		j := i + 1
		for j < len(in) && in[j].Rank == in[i].Rank {
			j++
		}
		g := in[i:j]
		if len(g) > 1 {
			pinIdx := -1
			if stickyID > 0 {
				for idx, m := range g {
					if m.ID == stickyID {
						pinIdx = idx
						break
					}
				}
			}
			if pinIdx >= 0 {
				out = append(out, g[pinIdx])
				for k := range g {
					if k != pinIdx {
						out = append(out, g[k])
					}
				}
			} else {
				off := int(n % uint64(len(g)))
				for k := range g {
					out = append(out, g[(k+off)%len(g)])
				}
			}
		} else {
			out = append(out, g...)
		}
		i = j
	}
	return out
}

// statusRe pulls the HTTP status out of the SDK's error text, which is formatted
// as "<prefix>: status <code>: <body>" (norma/llm/retry.go). The SDK exposes no
// typed error, so the string is what we have.
var statusRe = regexp.MustCompile(`status (\d{3})`)

// statusOf returns the HTTP status carried by err, or 0 if it isn't one.
func statusOf(err error) int {
	m := statusRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	code, _ := strconv.Atoi(m[1])
	return code
}

// shouldFailover reports whether err justifies trying the next profile.
//
// Never fails over on caller context cancellation — that's the user stopping a
// task or an outer task-level deadline, and burning a backup key on it would
// waste credit. Transport-level timeouts (while ctx.Err() == nil) DO fail over.
func shouldFailover(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if (ctx != nil && ctx.Err() != nil) || errors.Is(err, context.Canceled) {
		return false
	}
	if isQuotaError(err) {
		return true
	}
	switch code := statusOf(err); {
	case code == 0:
		return true // no status → transport-level failure (reset / DNS / timeout / stream drop)
	case code == 400:
		return false // bad or over-long request: identical everywhere
	case code == 401, code == 402, code == 403, code == 404, code == 408, code == 413, code == 422, code == 429:
		return true
	case code >= 500:
		return true
	}
	return false
}

// isCrossFormatCompatError matches parameter-rejection 400s (such as OpenAI
// max_tokens vs max_completion_tokens or unknown provider-specific fields) that
// may succeed on a backup profile using another API format.
func isCrossFormatCompatError(err error) bool {
	if err == nil || statusOf(err) != 400 {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unsupported_parameter") ||
		strings.Contains(s, "unknown parameter") ||
		strings.Contains(s, "unrecognized request argument") ||
		strings.Contains(s, "unsupported value")
}

// isHardFailure reports whether the failure is deterministic (the profile will
// keep failing until a human fixes it) rather than transient. Hard failures open
// the breaker on the first occurrence.
func isHardFailure(err error) bool {
	if err == nil {
		return false
	}
	if isQuotaError(err) {
		return true
	}
	switch statusOf(err) {
	case 401, 402, 403, 404:
		return true
	}
	return false
}

// trimErr shortens an error for logs/UI — provider bodies can be long.
func trimErr(err error) string {
	s := strings.TrimSpace(strings.ReplaceAll(err.Error(), "\n", " "))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// estimateTokens sizes a request conservatively for context-window filtering:
// ASCII averages ~4 bytes/token while CJK/non-ASCII runes tokenize closer to
// ~1.5-2 tokens per character. Counting non-ASCII runes avoids under-estimating
// Chinese/mixed security reports by ~2x.
func estimateTokens(req llm.CompletionRequest) int {
	tenths := 0
	for _, s := range req.System {
		tenths += textTokenTenths(s)
	}
	for _, m := range req.Messages {
		tenths += blocksTokenTenths(m.Content)
	}
	for _, t := range req.Tools {
		// InputSchema is a decoded map; charge ~35 tokens per top-level property.
		tenths += textTokenTenths(t.Name) + textTokenTenths(t.Description) + len(t.InputSchema)*350
	}
	if tenths <= 0 {
		return 0
	}
	return (tenths + 9) / 10
}

func textTokenTenths(s string) int {
	if s == "" {
		return 0
	}
	asciiBytes, nonASCII := 0, 0
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			asciiBytes++
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		nonASCII++
		i += size
	}
	// Conservative weights in tenths of a token: ASCII ~0.28/byte, non-ASCII ~1.6/rune.
	return (asciiBytes*28+9)/10 + nonASCII*16
}

func blocksTokenTenths(bs []llm.ContentBlock) int {
	n := 0
	for _, b := range bs {
		n += textTokenTenths(b.Text) + textTokenTenths(b.Thinking) + textTokenTenths(string(b.Input)) + textTokenTenths(b.Name)
		if len(b.Content) > 0 {
			n += blocksTokenTenths(b.Content)
		}
	}
	return n
}
