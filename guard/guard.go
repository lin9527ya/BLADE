// Package guard implements the safety boundary layer (docs §11): an audit log,
// user-configured intercept-rule evaluation, Observer/G5 failure attribution,
// and the RoE authorization-scope gate (intercept.CheckScope: tool calls whose
// network targets fall outside the task's task_scope are gated before any rule
// runs; mode off|ask|deny, default off). Every tool call passes through the
// PreToolUse hook before executing. Destructive/exfil gating is not hard-coded
// here — it lives in the DB intercept rules (seeded as ordinary [内置] rules, so
// users can disable or delete them), evaluated via applyIntercept.
package guard

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/hook"
)

// AuditEntry records one gated tool call.
type AuditEntry struct {
	TS      int64  `json:"ts"`
	Tool    string `json:"tool"`
	Action  string `json:"action"` // allow|block
	Reason  string `json:"reason,omitempty"`
	Command string `json:"command,omitempty"`
}

// Guard enforces the side-effect policy via agent-core hooks.
type Guard struct {
	mu          sync.Mutex
	audit       []AuditEntry
	attrib      map[string]int // failure attribution counts (Observer / G5)
	reg         *hook.Registry
	interceptor *intercept.Interceptor // optional; nil disables user-configured rules
	taskID      int64                  // RoE scope-gate context; 0 = unscoped (no enforcement)
}

// New creates a Guard without user-configured intercept rules (used for pentest
// tasks where the Interceptor is not yet available).
func New() *Guard { return newGuard(nil, 0) }

// NewWithInterceptor creates a Guard with user-configured intercept rules and
// the owning task id (RoE scope enforcement; taskID=0 disables the gate).
func NewWithInterceptor(ic *intercept.Interceptor, taskID int64) *Guard { return newGuard(ic, taskID) }

func newGuard(ic *intercept.Interceptor, taskID int64) *Guard {
	g := &Guard{attrib: map[string]int{}, interceptor: ic, taskID: taskID}
	g.reg = hook.NewRegistry().
		On(hook.PreToolUse, g.preToolUse).
		On(hook.PostToolUse, g.postToolUse)
	return g
}

// Hooks returns the hook registry to attach to an agent session.
func (g *Guard) Hooks() *hook.Registry { return g.reg }

func (g *Guard) preToolUse(ctx context.Context, ev hook.Event) hook.Result {
	// Extract the shell-command surface for the audit log: Bash + the interactive-shell
	// tools (shell_open's command, shell_send's text). Destructive/exfil gating is no
	// longer hard-coded here — it now lives in the DB intercept rules, evaluated by
	// applyIntercept below. Other tools record an empty command.
	var cmd string
	switch ev.ToolName {
	case "Bash", "shell_open":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(ev.Input, &in)
		cmd = in.Command
	case "shell_send":
		var in struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Input, &in)
		cmd = in.Text
	}
	g.record(ev.ToolName, "allow", "", cmd)
	return g.applyIntercept(ctx, ev)
}

// applyIntercept evaluates user-configured intercept rules against the tool call.
// Both rules and the fallback judge receive the complete tool input.
func (g *Guard) applyIntercept(ctx context.Context, ev hook.Event) hook.Result {
	if g.interceptor == nil {
		return hook.Result{}
	}
	if !g.interceptor.IsToolEnabled(ev.ToolName) {
		return hook.Result{}
	}
	ctx = intercept.WithCall(ctx, ev.ToolName, ev.Input)
	// RoE 授权范围先行：越界目标高于一切规则配置（模式见 intercept.ScopeMode）。
	if dec, hit := g.interceptor.CheckScope(ctx, g.taskID, ev.ToolName, ev.Input); hit {
		return g.settle(ctx, ev, dec)
	}
	// 任务级规则（由该任务的操作约束编译而来，P18）与全局规则并集参与匹配。
	dec, matched := g.interceptor.MatchForTask(g.taskKey(), ev.ToolName, ev.Input)
	if !matched {
		// No rule matched. Ask the LLM fallback judge (if enabled); when it is off
		// or unwired, keep current behavior and allow.
		d, judged := g.interceptor.Judge(ctx, ev.ToolName, ev.Input)
		if !judged {
			return hook.Result{}
		}
		dec = d
	}
	return g.settle(ctx, ev, dec)
}

// settle applies one intercept verdict — a rule, the LLM fallback judge, or the
// RoE scope gate — to a tool call: deny blocks, allow records the approval,
// ask waits for the user.
func (g *Guard) settle(ctx context.Context, ev hook.Event, dec intercept.Decision) hook.Result {
	switch dec.Action {
	case "deny":
		// 观测:deny 命中不阻塞审批,直接记一条 denied（历史/任务拦截页可见）。
		g.interceptor.Log(ctx, intercept.ConvIDFromContext(ctx), dec, ev.ToolName, ev.Input, "denied")
		return g.block(ev.ToolName, systemBlockMessage(dec.Message), "")
	case "allow":
		// Record explicit rule and model approvals so review details remain auditable.
		g.interceptor.Log(ctx, intercept.ConvIDFromContext(ctx), dec, ev.ToolName, ev.Input, "allowed")
		return hook.Result{}
	case "ask":
		// If the worker context is already cancelled (task stopped / killed), block
		// immediately without creating a pending record — avoids orphaned DB entries
		// and makes execOne complete fast, reducing the race against drainSynthetic.
		if ctx.Err() != nil {
			return g.block(ev.ToolName, systemBlockMessage("工作已取消，平台安全管控阻止执行"), "")
		}
		convID := intercept.ConvIDFromContext(ctx)
		out, ok := g.interceptor.HandleAsk(ctx, convID, dec, ev.ToolName, ev.Input)
		if !ok {
			return g.block(ev.ToolName, systemBlockMessage("人工审批未通过（用户拒绝或审批超时）"), "")
		}
		if len(out.Edited) == 0 {
			return hook.Result{}
		}
		// 改参放行（P15）：人改过的参数是【一次全新的执行】，必须先证明它比原来
		// 更保守（Narrow 只许收窄），再拿改后参数重跑 RoE 与规则——改参不能变成
		// 绕过裁决的后门。任一步不通过就拦下，绝不执行未复核的参数。
		narrowed, ok := intercept.Narrow(ev.ToolName, ev.Input, out.Edited)
		if !ok {
			g.record(ev.ToolName, "block", "改参越界："+strings.Join(narrowed.Diff, "；"), "")
			return g.block(ev.ToolName, systemBlockMessage("人工改参越界（只允许收窄）"), "")
		}
		if dec, hit := g.interceptor.CheckScope(ctx, g.taskID, ev.ToolName, narrowed.Input); hit && dec.Action == "deny" {
			g.interceptor.Log(ctx, convID, dec, ev.ToolName, narrowed.Input, "denied")
			return g.block(ev.ToolName, systemBlockMessage("改后参数超出授权范围"), "")
		}
		if dec, matched := g.interceptor.MatchForTask(g.taskKey(), ev.ToolName, narrowed.Input); matched && dec.Action == "deny" {
			g.interceptor.Log(ctx, convID, dec, ev.ToolName, narrowed.Input, "denied")
			return g.block(ev.ToolName, systemBlockMessage(dec.Message), "")
		}
		return hook.Result{UpdatedInput: narrowed.Input}
	}
	return hook.Result{}
}

// systemBlockMessage frames an intercept block as an ARTEX platform-governance
// decision so the agent does not mistake it for a target-side defense.
//
// The bare reasons ("禁止执行此工具" / "用户拒绝") read exactly like a WAF/403 on
// the target, so a pentest agent's instinct is to bypass them — rewrite the
// command, swap the payload, re-encode, retry. That is both futile (the platform
// blocks the class of action, not one string) and wrong (it's a policy decision,
// not an obstacle to defeat). This prefix states plainly that the block comes
// from the platform, is not the target's protection, and that the operation is
// forbidden — so the agent pivots to another approach instead of evading it.
// Audit/history rows keep the raw reason (see Interceptor.Log); only the
// model-facing tool_result carries this framing.
func systemBlockMessage(reason string) string {
	return "【ARTEX 平台管控·非目标防御】此调用被平台拦截。" +
		"原因：" + reason + "。此操作被禁止。"
}

var reBlocked = regexp.MustCompile(`(?i)\b(403|forbidden|waf|blocked|rate.?limit|429|captcha|denied)\b`)

// postToolUse is the Observer failure-attribution hook (G5): it classifies tool
// results into blocked / error / ok so the planner can change strategy instead
// of giving up at a WAF.
func (g *Guard) postToolUse(_ context.Context, ev hook.Event) hook.Result {
	if ev.ToolName != "Bash" {
		return hook.Result{}
	}
	class := "ok"
	switch {
	case reBlocked.Match(ev.Result):
		class = "blocked"
	case ev.IsError:
		class = "error"
	}
	g.mu.Lock()
	g.attrib[class]++
	g.mu.Unlock()
	return hook.Result{}
}

// Attributions returns failure-attribution counts (Observer / G5).
func (g *Guard) Attributions() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.attrib))
	for k, v := range g.attrib {
		out[k] = v
	}
	return out
}

// taskKey renders the guard's task id in the ledger's string form ("" = unscoped,
// e.g. a chat session — then only global rules apply).
func (g *Guard) taskKey() string {
	if g.taskID <= 0 {
		return ""
	}
	return strconv.FormatInt(g.taskID, 10)
}

func (g *Guard) block(tool, reason, cmd string) hook.Result {
	g.record(tool, "block", reason, cmd)
	return hook.Result{Decision: "block", Message: reason}
}

func (g *Guard) record(tool, action, reason, cmd string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.audit = append(g.audit, AuditEntry{TS: time.Now().Unix(), Tool: tool, Action: action, Reason: reason, Command: cmd})
	if len(g.audit) > 2000 {
		g.audit = g.audit[len(g.audit)-2000:]
	}
}

// Audit returns a snapshot of recent gated calls (most recent last).
func (g *Guard) Audit() []AuditEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]AuditEntry, len(g.audit))
	copy(out, g.audit)
	return out
}
