package agent

import (
	"context"
	"strconv"
	"strings"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/blade/sidequestion"
)

func attachSideCapture(ctx context.Context, opts *agentcore.Options) context.Context {
	ri := RunInfoFrom(ctx)
	p := sidequestion.Parent{TaskID: ri.TaskID, ExplorationID: ri.ExplorationID, IntentID: ri.IntentID}
	if strings.HasPrefix(ri.SessionID, "conv-") {
		p.ConversationID, _ = strconv.ParseInt(strings.TrimPrefix(ri.SessionID, "conv-"), 10, 64)
	}
	// 会话名与任务上下文同时存在时，检查点归属会话；归一化后 task/exploration/intent
	// 被清空，避免写入 side_question_sessions 时违反“二选一”CHECK。
	p = p.Normalized()
	if p.ConversationID == 0 && (p.TaskID == 0 || p.ExplorationID == 0) {
		return ctx
	}
	ctx, opts.Deps = sidequestion.Attach(ctx, p, opts.Deps, opts.Provider)
	return ctx
}
