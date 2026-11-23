package llmpool

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"iter"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	Prov         llm.Provider
}

// RankActive is the Rank the caller gives the chain head (the active profile, or
// an explicitly bound one) so it always outranks any configured priority.
const RankActive = int(^uint(0)>>1) - 1

// ErrExhausted is returned when every member of the chain failed.
var ErrExhausted = errors.New("LLM 轮询：所有配置均不可用")

// Pool is an llm.Provider that fails over across an ordered chain of members.
// It is safe for concurrent use: members are immutable after construction and
// all mutable state lives in the shared Registry.
type Pool struct {
	members  []*Member // in chain order (active first, then priority DESC)
	health   *Registry
	rr       atomic.Uint64 // rotates the starting point within an equal-priority group when no session id exists
	stickyMu sync.Mutex
	sticky   map[string]int64 // sessionID -> last succeeded Member.ID for prefix KV cache affinity
}

const maxStickySessions = 4096

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
			emitted := false
			var failed error
			for ev, err := range m.Prov.Stream(ctx, req) {
				if err != nil && !emitted && shouldFailover(ctx, err) {
					failed = err
					break // safe window: nothing reached the caller yet
				}
				emitted = true
				if !yield(ev, err) {
					return // caller stopped consuming (cancel / early exit)
				}
				if err != nil {
					return // terminal error already handed to the caller
				}
			}
			if failed == nil {
				p.health.Pass(m.ID) // completed (or failed in a non-failover way)
				p.pinSession(sid, m.ID)
				return
			}
			lastErr = failed
			code := statusOf(failed)
			if code == 413 {
				if m.WindowTokens > minWindow {
					minWindow = m.WindowTokens
				}
				log.Printf("[llmpool] %q(%s) 返回 413（载荷超限），跳过等长或更小窗口成员（不熔断）", m.Name, m.Model)
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
		msg, sr, usage, err := m.Prov.Complete(ctx, req)
		if err == nil {
			p.health.Pass(m.ID)
			p.pinSession(sid, m.ID)
			return msg, sr, usage, nil
		}
		if !shouldFailover(ctx, err) {
			// Non-failover error (e.g. ctx cancel, deterministic 4xx): surface as-is
			// without tripping health, matching Stream's non-failover path.
			p.health.Pass(m.ID)
			return llm.Message{}, "", llm.Usage{}, err
		}
		lastErr = err
		if statusOf(err) == 413 {
			if m.WindowTokens > minWindow {
				minWindow = m.WindowTokens
			}
			log.Printf("[llmpool] %q(%s) 返回 413（载荷超限），跳过等长或更小窗口成员（不熔断）", m.Name, m.Model)
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
// and those whose context window can't hold this request, rotate within equal-rank
// groups with sticky affinity, and keep same-format profiles ahead of cross-format
// ones so prompt KV cache stays hot. If every fitting member is tripped, probes
// the first fitting member; never forces a guaranteed-400 over-window request.
func (p *Pool) orderFor(sid string, req llm.CompletionRequest) ([]*Member, int) {
	est := estimateTokens(req)
	var open, fitting []*Member
	for _, m := range p.members {
		if m.WindowTokens > 0 && est > m.WindowTokens {
			continue // would 400 on length — not a useful target
		}
		fitting = append(fitting, m)
		if p.health.IsOpen(m.ID) {
			continue
		}
		open = append(open, m)
	}
	if len(open) == 0 {
		if len(fitting) == 0 {
			return nil, est
		}
		return fitting[:1], est // all fitting members tripped: probe first fitting member
	}
	var rotated []*Member
	if sid == "" {
		rotated = rotateGroups(open, p.rr.Add(1)-1)
	} else {
		rotated = rotateGroupsSticky(open, sessionHash(sid), p.pinnedMember(sid))
	}
	return preferSameFormat(rotated, p.preferredFormat(sid, open)), est
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
	return p.sticky[sid]
}

func (p *Pool) pinSession(sid string, memberID int64) {
	if sid == "" || memberID <= 0 {
		return
	}
	p.stickyMu.Lock()
	defer p.stickyMu.Unlock()
	if p.sticky == nil {
		p.sticky = make(map[string]int64, 64)
	} else if _, exists := p.sticky[sid]; !exists && len(p.sticky) >= maxStickySessions {
		p.sticky = make(map[string]int64, 64)
	}
	p.sticky[sid] = memberID
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
// Never fails over on context cancellation — that's the user stopping a task or a
// task-level timeout, and burning a backup key on it would both waste credit and
// pollute the run's termination diagnosis. Never on 400 either: a malformed or
// over-long request fails identically everywhere.
func shouldFailover(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch code := statusOf(err); {
	case code == 0:
		return true // no status → transport-level failure (reset / DNS / timeout)
	case code == 400:
		return false // bad or over-long request: identical everywhere
	case code == 401, code == 402, code == 403, code == 404, code == 408, code == 413, code == 422, code == 429:
		return true
	case code >= 500:
		return true
	}
	return false
}

// isHardFailure reports whether the failure is deterministic (the profile will
// keep failing until a human fixes it) rather than transient. Hard failures open
// the breaker on the first occurrence.
func isHardFailure(err error) bool {
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
