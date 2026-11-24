package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

func TestParseIMCommand(t *testing.T) {
	cmd, ok := parseIMCommand(`/new "测试 目标" --name "My Task" --scope root_domain:example.com --scope ip:1.2.3.4`)
	if !ok {
		t.Fatal("command not parsed")
	}
	if cmd.Name != "new" || cmd.Text != "测试 目标" || imFlag(cmd, "name") != "My Task" {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	if got := imFlagAll(cmd, "scope"); !reflect.DeepEqual(got, []string{"root_domain:example.com", "ip:1.2.3.4"}) {
		t.Fatalf("scope flags: %v", got)
	}
	if _, ok := parseIMCommand("普通消息"); ok {
		t.Fatal("plain text must not be a command")
	}
	if cmd, _ := parseIMCommand("/TASKS running 5"); cmd.Name != "tasks" || !reflect.DeepEqual(cmd.Args, []string{"running", "5"}) {
		t.Fatalf("case/args: %+v", cmd)
	}
}

func TestIMCommandRoleGate(t *testing.T) {
	viewer := "viewer"
	operator := "operator"
	for _, tc := range []struct {
		role string
		cmd  string
		want bool
	}{
		{viewer, "/help", true},
		{viewer, "/tasks", true},
		{viewer, "/show 1", true},
		{viewer, "/scope 1 list", true},
		{viewer, "/scope 1 add root_domain example.com", false},
		{viewer, "/new test", false},
		{viewer, "/pause 1", false},
		{operator, "/new test", true},
		{operator, "/edit 1 --name x", true},
	} {
		cmd, ok := parseIMCommand(tc.cmd)
		if !ok {
			t.Fatalf("parse %q", tc.cmd)
		}
		if got := imCommandAllowed(tc.role, cmd); got != tc.want {
			t.Fatalf("role %s cmd %q: got %v want %v", tc.role, tc.cmd, got, tc.want)
		}
	}
}

func TestRestrictIMSurfaceTools(t *testing.T) {
	mk := func(name string) actool.CoreTool {
		return roTool(name, name, nil, func(context.Context, json.RawMessage) (actool.Result, error) {
			return actool.Text("ok"), nil
		})
	}
	tools := []actool.CoreTool{
		mk("list_tasks"), mk("get_task_graph"), mk("Bash"), mk("WebFetch"),
		mk("spawn_task"), mk("pause_task"), mk("resume_task"),
		mk("update_task_metadata"), mk("edit_task_scope"), mk("create_skill"),
	}
	names := func(in []actool.CoreTool) []string {
		out := make([]string, 0, len(in))
		for _, t := range in {
			out = append(out, t.Name())
		}
		return out
	}
	plain := restrictIMSurfaceTools(context.Background(), tools)
	if len(plain) != len(tools) {
		t.Fatalf("non-IM context must not filter: %v", names(plain))
	}
	viewerCtx := withPrincipal(context.Background(), runPrincipal{Role: "viewer", Surface: "im"})
	viewer := names(restrictIMSurfaceTools(viewerCtx, tools))
	if !reflect.DeepEqual(viewer, []string{"list_tasks", "get_task_graph"}) {
		t.Fatalf("viewer surface: %v", viewer)
	}
	opCtx := withPrincipal(context.Background(), runPrincipal{Role: "operator", Surface: "im"})
	op := names(restrictIMSurfaceTools(opCtx, tools))
	want := []string{"list_tasks", "get_task_graph", "spawn_task", "pause_task", "resume_task", "update_task_metadata", "edit_task_scope"}
	if !reflect.DeepEqual(op, want) {
		t.Fatalf("operator surface: %v want %v", op, want)
	}
}

func TestClipIMText(t *testing.T) {
	if got := clipIMText("  hello  ", 10); got != "hello" {
		t.Fatalf("trim: %q", got)
	}
	long := ""
	for i := 0; i < 20; i++ {
		long += "字"
	}
	if got := clipIMText(long, 5); got != "字字字字字…" {
		t.Fatalf("clip: %q", got)
	}
}
