package server

import (
	"context"
	"encoding/json"

	actool "github.com/lin9527ya/Norma/tool"
)

// runPrincipal is trusted runtime metadata, never a model argument or display
// name. An absent principal cannot perform platform-management writes.
type runPrincipal struct{ Role, Actor, ID, Surface string }
type runPrincipalKey struct{}

func principalFrom(ctx context.Context) runPrincipal {
	p, _ := ctx.Value(runPrincipalKey{}).(runPrincipal)
	return p
}
func withPrincipal(ctx context.Context, p runPrincipal) context.Context {
	return context.WithValue(ctx, runPrincipalKey{}, p)
}
func systemPrincipal() runPrincipal {
	return runPrincipal{Role: "system", Actor: "scheduler", ID: "system:scheduler"}
}
func canManagePlatform(ctx context.Context) bool {
	role := principalFrom(ctx).Role
	return role == "admin" || role == "system"
}
func isPlatformManagementTool(name string) bool {
	switch name {
	case "create_skill", "update_skill_file", "create_custom_tool", "update_custom_tool", "create_mcp", "update_mcp":
		return true
	}
	return false
}
func restrictManagementTools(ctx context.Context, tools []actool.CoreTool) []actool.CoreTool {
	if canManagePlatform(ctx) {
		return tools
	}
	out := make([]actool.CoreTool, 0, len(tools))
	for _, t := range tools {
		if !isPlatformManagementTool(t.Name()) {
			out = append(out, t)
		}
	}
	return out
}
func managementTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return wrTool(name, desc, schema, func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if !canManagePlatform(ctx) {
			return actool.Errorf("平台管理工具需要管理员权限"), nil
		}
		return run(ctx, in)
	})
}

// IM runs are limited to an explicit task-control surface. The allowlist is
// applied after the normal per-agent tool resolution, so a platform message can
// never reach Bash/files/MCP/platform management even if the bound agent would
// normally expose them. viewer is read-only; operator adds the task mutations
// that the mobile commands need.
var imViewerTools = map[string]bool{
	"list_tasks":              true,
	"get_task_graph":          true,
	"get_task_worker_trace":   true,
	"list_task_worker_traces": true,
	"list_task_findings":      true,
	"get_task_node_detail":    true,
}

// imSurfaceUnboundTools are task-op tools that stay unbound from Auto by default
// but are exposed to IM operators on demand. The tools row still gates enabled;
// this only relaxes the agent binding for the explicit IM surface.
var imSurfaceUnboundTools = map[string]bool{
	"update_task_metadata": true,
	"edit_task_scope":      true,
}

var imOperatorTools = func() map[string]bool {
	out := map[string]bool{}
	for name := range imViewerTools {
		out[name] = true
	}
	for _, name := range []string{
		"spawn_task", "pause_task", "resume_task",
		"update_task_metadata", "edit_task_scope",
	} {
		out[name] = true
	}
	return out
}()

func restrictIMSurfaceTools(ctx context.Context, tools []actool.CoreTool) []actool.CoreTool {
	p := principalFrom(ctx)
	if p.Surface != "im" {
		return tools
	}
	allowed := imViewerTools
	if p.Role == "operator" {
		allowed = imOperatorTools
	}
	out := make([]actool.CoreTool, 0, len(tools))
	for _, t := range tools {
		if allowed[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}
