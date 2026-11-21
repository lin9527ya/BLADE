package server

import (
	"context"
	"encoding/json"

	actool "github.com/lin9527ya/Norma/tool"
)

// runPrincipal is trusted runtime metadata, never a model argument or display
// name. An absent principal cannot perform platform-management writes.
type runPrincipal struct{ Role, Actor, ID string }
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
