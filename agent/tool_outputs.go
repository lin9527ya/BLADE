package agent

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

// 工具输出台账与证据逐字回验（借鉴 Pojun v0.4.29 的 PentestGPT 机制，独立实现）。
//
// report_finding 的 evidence 经常是模型转述；一条“看起来像”的高危如果没有任何
// 原文能回到本次真实工具输出，报告读者无法复核。这里把本次 run 模型实际看到的
// 工具回显收进有界台账，上报时核对 evidence 的有效行是否能逐字找到；默认只告警，
// 档位可扩为 block。

type EvidenceVerifyMode int

const (
	EvidenceVerifyOff EvidenceVerifyMode = iota
	EvidenceVerifyWarn
	EvidenceVerifyBlock
)

func EvidenceVerifyModeFromString(v string) EvidenceVerifyMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "false", "0":
		return EvidenceVerifyOff
	case "block", "strict":
		return EvidenceVerifyBlock
	default:
		return EvidenceVerifyWarn
	}
}

func (m EvidenceVerifyMode) String() string {
	switch m {
	case EvidenceVerifyOff:
		return "off"
	case EvidenceVerifyBlock:
		return "block"
	default:
		return "warn"
	}
}

type toolOutputCtxKey struct{}

type toolOutputsCtx struct {
	ledger *ToolOutputLedger
	mode   EvidenceVerifyMode
}

func WithToolOutputs(ctx context.Context, ledger *ToolOutputLedger, mode EvidenceVerifyMode) context.Context {
	if ledger == nil {
		return ctx
	}
	return context.WithValue(ctx, toolOutputCtxKey{}, toolOutputsCtx{ledger: ledger, mode: mode})
}

func ToolOutputsFromContext(ctx context.Context) (*ToolOutputLedger, EvidenceVerifyMode) {
	v, ok := ctx.Value(toolOutputCtxKey{}).(toolOutputsCtx)
	if !ok || v.ledger == nil {
		return nil, EvidenceVerifyOff
	}
	return v.ledger, v.mode
}

const (
	toolOutputLedgerMaxEntries = 200
	toolOutputLedgerMaxBytes   = 4 << 20
	toolOutputLedgerEntryCap   = 16 << 10
)

// ToolOutputLedger stores normalized tool-result text visible to the model.
type ToolOutputLedger struct {
	mu      sync.Mutex
	entries []string
	bytes   int
}

func NewToolOutputLedger() *ToolOutputLedger { return &ToolOutputLedger{} }

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func normalizeForMatch(s string) string {
	s = ansiEscapeRe.ReplaceAllString(s, "")
	if !strings.ContainsAny(s, " \t\r\f\v") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\f', '\v':
			space = true
			continue
		case '\n':
			space = false
			b.WriteRune('\n')
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (l *ToolOutputLedger) Add(text string) {
	if l == nil || strings.TrimSpace(text) == "" {
		return
	}
	if len(text) > toolOutputLedgerEntryCap {
		text = text[:toolOutputLedgerEntryCap]
	}
	norm := normalizeForMatch(text)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, norm)
	l.bytes += len(norm)
	for len(l.entries) > toolOutputLedgerMaxEntries || l.bytes > toolOutputLedgerMaxBytes {
		l.bytes -= len(l.entries[0])
		l.entries = l.entries[1:]
	}
}

// VerifyEvidence returns how many meaningful evidence lines (>=20 bytes after
// trimming) occur verbatim in this run's visible tool output.
func (l *ToolOutputLedger) VerifyEvidence(evidence string) (matched, total int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	joined := strings.Join(l.entries, "\n")
	l.mu.Unlock()
	if joined == "" {
		return 0, 0
	}
	for _, line := range strings.Split(evidence, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 20 {
			continue
		}
		total++
		if strings.Contains(joined, normalizeForMatch(line)) {
			matched++
		}
	}
	return matched, total
}

func evidenceVerdict(matched, total int) string {
	if total < 2 {
		return "good"
	}
	if matched == 0 {
		return "none"
	}
	if matched*2 >= total {
		return "good"
	}
	return "partial"
}
