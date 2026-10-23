package agent

import (
	"bytes"
	"text/template"
	"time"
)

// PromptOverride, if set, returns the stored system-prompt template for an agent
// key and whether one exists. The server wires it to the PG agent_prompts table.
// When nil or no override exists, agents use their built-in default prompt — so
// behavior is identical until a user edits a prompt in the UI.
var PromptOverride func(agentKey string) (string, bool)

// PromptAppend 返回某个 agent 的**追加段**：渲染在正文之后、代码尾段之前。
// 与 PromptOverride 分开的理由：外部场景（benchmark 等）往往只需要加几句场景说明，
// 不该为此复制整份提示词——复制出去的副本会随内置提示词演进而悄悄过时。
var PromptAppend func(agentKey string) (string, bool)

// Prompt-variable structs — fields mirror each agent's catalog (docs §5a) so a
// user template referencing a catalog variable renders; referencing anything else
// fails template execution and falls back to the built-in default.
type PlannerVars struct{ Goal, Scope, AssetSummary, DataDir, Now string }
type WorkerVars struct{ ProxyAddr, WorkerName, DataDir, Now string }
type MainVars struct{ Goal, AssetSummary, FindingsSummary, DataDir, Now string }
type GoalsVars struct{ EngagementDescription, DataDir, Now string }

// nowStr 是 universal {{.Now}} 变量渲染出来的服务端**日期**（只到天）。
//
// 刻意不带时分秒：{{.Now}} 渲染进的是 system prompt，而 renderSystem 每轮/每次 run 都会
// 重跑——带秒就等于每次调用都换一份前缀，system 后面的工具 schema 与全部历史会一起掉出
// prompt cache（当前内置模板与库内模板都没用它，但它在变量目录里对用户可见，一旦有人写进
// 提示词就会变成每次调用 100% miss）。确有需要精确时间的场景，不要放进 system prompt。
func nowStr() string { return time.Now().Format("2006-01-02") }

// renderSystem returns the rendered system-prompt BODY (段 [A]) for agentKey.
// Precedence: the DB-stored template (if any) over the built-in default template
// (def). BOTH are Go templates now — the built-in default is seeded into the DB
// verbatim, so the two paths render identically until a user edits the prompt.
// Rendering always runs (def used to be pre-substituted plain text; it is now a
// {{.Var}} template like the DB one). On any render error we fall back to the
// default template, then to the raw default string — an agent never starts with a
// half-rendered prompt. Callers append the code-owned tail (trafficTool / 中间产物
// 输出规约) AFTER this, so those can't be edited away via the DB body.
func renderSystem(agentKey, def string, vars any) string {
	tmpl := def
	if PromptOverride != nil {
		if t, ok := PromptOverride(agentKey); ok && t != "" {
			tmpl = t
		}
	}
	// 追加段拼在正文之后、调用方的代码尾段之前；对「DB 正文」与「内置默认」一视同仁，
	// 所以只加一段场景说明也能作用在内置提示词上。
	if PromptAppend != nil {
		if extra, ok := PromptAppend(agentKey); ok && extra != "" {
			tmpl = tmpl + "\n\n" + extra
		}
	}
	if out, err := renderTmpl(tmpl, vars); err == nil {
		return out
	}
	// DB template broke (e.g. references an out-of-catalog var) → code default.
	if out, err := renderTmpl(def, vars); err == nil {
		return out
	}
	return def
}

func renderTmpl(tmpl string, vars any) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}
