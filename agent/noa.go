package agent

import (
	"log"
	"path/filepath"
	"reflect"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/Norma/noa"
	"github.com/lin9527ya/Norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noaState captures runtime hooks produced while wiring (or bypassing) noa for
// one session run so the outer context index and fallback materializer do not
// couple callers to noaadapter internals.
type noaState struct {
	enabled     bool
	compactedFn func() bool
	materialize func([]llm.Message) ([]llm.Message, bool)
}

// enableNoa attaches noa context compression when enabled() reports true, or
// prepares a read-only Materialize fallback when switching off a session that
// already holds persisted noa blocks on disk.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, contextLimit int, onWarn func(string)) noaState {
	if enabled == nil || !enabled() {
		return noaState{
			materialize: noaMaterializer(archiveRoot, sessionID, contextLimit, onWarn),
		}
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	origSysLen := len(opts.SystemPrompt)
	origAppendLen := len(opts.AppendSystemPrompt)
	origBoundary := opts.DynamicBoundary

	sess, err := noaadapter.EnableWithSession(opts, noaadapter.Options{
		ArchiveBaseDir:    filepath.Join(archiveRoot, "noa"),
		SessionID:         sessionID,
		ModelContextLimit: contextLimit,
		OnWarn:            onWarn,
	})
	if err != nil {
		if onWarn != nil {
			onWarn("noa 压缩启用失败,回退内置压缩:" + err.Error())
		}
		return noaState{
			materialize: noaMaterializer(archiveRoot, sessionID, contextLimit, onWarn),
		}
	}

	// noaadapter.Enable appends noa's static prompt sections to AppendSystemPrompt
	// (after opts.SystemPrompt's dynamic tails) and overwrites DynamicBoundary to
	// the combined end, which would pull per-round/per-task dynamic sections into
	// the cached prefix. Splice noa's static segments right before origBoundary and
	// shift DynamicBoundary forward by the inserted segment count instead.
	if len(opts.AppendSystemPrompt) > origAppendLen {
		added := append([]string(nil), opts.AppendSystemPrompt[origAppendLen:]...)
		opts.AppendSystemPrompt = opts.AppendSystemPrompt[:origAppendLen]
		spliceNoaStaticPrompt(opts, origSysLen, origBoundary, added)
	}

	// Compactor overrides Compaction; clear explicitly to avoid agentcore warnings.
	opts.Compaction = nil
	return noaState{
		enabled: true,
		compactedFn: func() bool {
			return len(noa.ActiveBlocks(sess.State())) > 0
		},
	}
}

func spliceNoaStaticPrompt(opts *agentcore.Options, origSysLen, origBoundary int, added []string) {
	if len(added) == 0 {
		return
	}
	insertAt := origBoundary
	if insertAt < 0 {
		insertAt = 0
	}
	if insertAt > origSysLen {
		insertAt = origSysLen
	}
	updated := make([]string, 0, origSysLen+len(added))
	updated = append(updated, opts.SystemPrompt[:insertAt]...)
	updated = append(updated, added...)
	updated = append(updated, opts.SystemPrompt[insertAt:]...)
	opts.SystemPrompt = updated
	opts.DynamicBoundary = insertAt + len(added)
}

func noaMaterializer(archiveRoot, sessionID string, contextLimit int, onWarn func(string)) func([]llm.Message) ([]llm.Message, bool) {
	if archiveRoot == "" || sessionID == "" {
		return nil
	}
	baseDir := filepath.Join(archiveRoot, "noa")
	store := noaadapter.NewStateStore(filepath.Join(baseDir, sessionID))
	if !store.Exists() {
		return nil
	}
	return func(msgs []llm.Message) ([]llm.Message, bool) {
		if len(msgs) == 0 {
			return msgs, false
		}
		out, err := noaadapter.Materialize(msgs, noaadapter.Options{
			ArchiveBaseDir:    baseDir,
			SessionID:         sessionID,
			ModelContextLimit: contextLimit,
			OnWarn:            onWarn,
		})
		if err != nil {
			if onWarn != nil {
				onWarn("noa 历史压缩状态物化失败,将使用原始历史:" + err.Error())
			}
			return msgs, false
		}
		changed := len(out) != len(msgs) || !reflect.DeepEqual(msgs, out)
		return out, changed
	}
}
