package server

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lin9527ya/blade/db"
)

type imCommand struct {
	Name  string
	Args  []string
	Flags map[string][]string
	Text  string // arguments joined, flags removed (the /new goal)
}

func parseIMCommand(text string) (imCommand, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return imCommand{}, false
	}
	tokens := splitIMTokens(text)
	if len(tokens) == 0 {
		return imCommand{}, false
	}
	name := strings.ToLower(strings.TrimPrefix(tokens[0], "/"))
	if name == "" {
		return imCommand{}, false
	}
	cmd := imCommand{Name: name, Flags: map[string][]string{}}
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if strings.HasPrefix(tok, "--") {
			key := strings.TrimPrefix(tok, "--")
			if key == "" {
				continue
			}
			value := ""
			if before, after, ok := strings.Cut(key, "="); ok {
				key, value = before, after
			} else if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "--") {
				value = tokens[i+1]
				i++
			}
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" {
				continue
			}
			cmd.Flags[key] = append(cmd.Flags[key], value)
			continue
		}
		cmd.Args = append(cmd.Args, tok)
	}
	cmd.Text = strings.TrimSpace(strings.Join(cmd.Args, " "))
	return cmd, true
}

func splitIMTokens(s string) []string {
	var out []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if quote != 0 {
			if r == '\\' {
				escaped = true
				continue
			}
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		switch {
		case r == '"' || r == '\'':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

func imFlag(cmd imCommand, key string) string {
	values := cmd.Flags[strings.ToLower(key)]
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[len(values)-1])
}

func imFlagAll(cmd imCommand, key string) []string {
	return cmd.Flags[strings.ToLower(key)]
}

func imFirstArg(cmd imCommand) string {
	if len(cmd.Args) == 0 {
		return ""
	}
	return strings.TrimSpace(cmd.Args[0])
}

func (svc *imService) executeCommand(ctx context.Context, job imJob, cmd imCommand) imResult {
	res := imResult{action: "command:" + cmd.Name, status: 200}
	if !imCommandAllowed(job.user.Role, cmd) {
		res.status = 403
		res.err = fmt.Errorf("只读账号不能执行该命令")
		res.reply = "只读账号只能查看任务与范围；请联系管理员在 IM 允许用户中提升为 operator。"
		return res
	}
	switch cmd.Name {
	case "help":
		res.reply = imHelpText(job.user.Role)
	case "tasks":
		res.reply = svc.commandTasks(cmd)
	case "show":
		reply, taskID, err := svc.commandShow(cmd)
		res.reply, res.taskID = reply, taskID
		if err != nil {
			res.status, res.err = 400, err
		}
	case "new":
		reply, taskID, err := svc.commandNew(ctx, cmd)
		res.reply, res.taskID = reply, taskID
		if err != nil {
			res.status, res.err = 400, err
		}
	case "pause", "resume":
		reply, taskID, err := svc.commandControl(cmd)
		res.reply, res.taskID = reply, taskID
		if err != nil {
			res.status, res.err = 400, err
		}
	case "edit":
		reply, taskID, err := svc.commandEdit(cmd)
		res.reply, res.taskID = reply, taskID
		if err != nil {
			res.status, res.err = 400, err
		}
	case "scope":
		reply, taskID, err := svc.commandScope(cmd)
		res.reply, res.taskID = reply, taskID
		if err != nil {
			res.status, res.err = 400, err
		}
	default:
		res.status, res.err = 400, fmt.Errorf("未知命令: /%s", cmd.Name)
		res.reply = "未知命令。发送 /help 查看可用命令。"
	}
	res.detail = res.reply
	return res
}

func imCommandAllowed(role string, cmd imCommand) bool {
	if role == "operator" {
		return true
	}
	switch cmd.Name {
	case "help", "tasks", "show":
		return true
	case "scope":
		op := "list"
		if len(cmd.Args) > 1 {
			op = strings.ToLower(strings.TrimSpace(cmd.Args[1]))
		}
		return op == "list"
	default:
		return false
	}
}

func (svc *imService) taskByID(raw string) (*Task, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("需要 task_id，例如 /show 42")
	}
	t, ok := svc.s.m.Task(raw)
	if !ok || t == nil {
		return nil, fmt.Errorf("任务 #%s 不存在", raw)
	}
	return t, nil
}

func (svc *imService) commandTasks(cmd imCommand) string {
	statusFilter, limit := "all", 15
	if v := imFirstArg(cmd); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		} else {
			statusFilter = strings.ToLower(v)
		}
	}
	if len(cmd.Args) > 1 {
		if n, err := strconv.Atoi(strings.TrimSpace(cmd.Args[1])); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 50 {
		limit = 50
	}
	tasks := svc.s.m.List()
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt == tasks[j].CreatedAt {
			return tasks[i].ID > tasks[j].ID
		}
		return tasks[i].CreatedAt > tasks[j].CreatedAt
	})
	var b strings.Builder
	n := 0
	for _, t := range tasks {
		status := svc.s.deriveTaskStatus(t)
		if statusFilter != "all" && status != statusFilter {
			continue
		}
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = t.Description
		}
		goal := strings.ReplaceAll(strings.TrimSpace(t.Goal), "\n", " ")
		fmt.Fprintf(&b, "#%s [%s] %s — %s\n", t.ID, status, clipIMText(name, 40), clipIMText(goal, 80))
		n++
		if n >= limit {
			break
		}
	}
	if n == 0 {
		return "没有匹配的任务。"
	}
	return strings.TrimRight(b.String(), "\n")
}

func (svc *imService) commandShow(cmd imCommand) (string, *int64, error) {
	t, err := svc.taskByID(imFirstArg(cmd))
	if err != nil {
		return err.Error(), nil, err
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	status := svc.s.deriveTaskStatus(t)
	var b strings.Builder
	fmt.Fprintf(&b, "#%s [%s]\n", t.ID, status)
	if t.Name != "" {
		fmt.Fprintf(&b, "名称：%s\n", clipIMText(t.Name, 120))
	}
	fmt.Fprintf(&b, "描述：%s\n", clipIMText(t.Description, 300))
	fmt.Fprintf(&b, "目标：%s\n", clipIMText(t.Goal, 600))
	if t.Remark != "" {
		fmt.Fprintf(&b, "备注：%s\n", clipIMText(t.Remark, 300))
	}
	fmt.Fprintf(&b, "范围策略：%s\n", t.AssetScopePolicy)
	if t.ParentRef != "" {
		fmt.Fprintf(&b, "父任务：%s\n", t.ParentRef)
	}
	if t.Paused && t.PauseReason != "" {
		fmt.Fprintf(&b, "暂停原因：%s\n", clipIMText(t.PauseReason, 200))
	}
	if t.CreatedAt > 0 {
		fmt.Fprintf(&b, "创建于：%s\n", time.Unix(t.CreatedAt, 0).Format("2006-01-02 15:04"))
	}
	return strings.TrimRight(b.String(), "\n"), &id, nil
}

func (svc *imService) commandNew(ctx context.Context, cmd imCommand) (string, *int64, error) {
	goal := strings.TrimSpace(cmd.Text)
	if goal == "" {
		return "用法：/new <目标> [--name 名称] [--policy exact|target] [--scope kind:value] [--timeout 秒]", nil, fmt.Errorf("goal 不能为空")
	}
	name := imFlag(cmd, "name")
	policy := imFlag(cmd, "policy")
	timeout := 0
	if raw := imFlag(cmd, "timeout"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return "timeout 必须是非负整数（秒）。", nil, fmt.Errorf("invalid timeout")
		}
		timeout = n
	}
	description := name
	if description == "" {
		description = clipIMText(goal, 60)
	}
	scopes := imFlagAll(cmd, "scope")
	beforeLaunch := func(bctx context.Context, t *Task) error {
		if len(scopes) == 0 {
			return nil
		}
		as := svc.s.m.Assets()
		if as == nil {
			return fmt.Errorf("资产库未启用")
		}
		taskID, err := strconv.ParseInt(t.ID, 10, 64)
		if err != nil {
			return err
		}
		for _, raw := range scopes {
			kind, value, ok := strings.Cut(raw, ":")
			if !ok || strings.TrimSpace(kind) == "" || strings.TrimSpace(value) == "" {
				return fmt.Errorf("范围格式应为 kind:value，收到 %q", raw)
			}
			if _, err := as.AddAgentScope(taskID, strings.TrimSpace(kind), strings.TrimSpace(value), "IM 创建时指定", "im"); err != nil {
				return err
			}
		}
		return nil
	}
	t, err := svc.s.createTaskFromSpawn(ctx, spawnTaskArgs{
		Description: description, Goal: goal, Name: name,
		AssetScopePolicy: policy, TimeoutSeconds: timeout,
	}, beforeLaunch)
	if err != nil {
		return "创建失败：" + err.Error(), nil, err
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	return fmt.Sprintf("已创建任务 #%s [%s]。发送 /show %s 查看，/pause %s 暂停。", t.ID, svc.s.deriveTaskStatus(t), t.ID, t.ID), &id, nil
}

func (svc *imService) commandControl(cmd imCommand) (string, *int64, error) {
	t, err := svc.taskByID(imFirstArg(cmd))
	if err != nil {
		return err.Error(), nil, err
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	out, err := svc.s.applyTaskControl(t, cmd.Name)
	if err != nil {
		return "操作失败：" + err.Error(), &id, err
	}
	status := out.Status
	if status == "" {
		status = svc.s.deriveTaskStatus(t)
	}
	word := map[string]string{"pause": "已暂停", "resume": "已继续"}[cmd.Name]
	return fmt.Sprintf("任务 #%s %s（%s）。", t.ID, word, status), &id, nil
}

func (svc *imService) commandEdit(cmd imCommand) (string, *int64, error) {
	t, err := svc.taskByID(imFirstArg(cmd))
	if err != nil {
		return err.Error(), nil, err
	}
	var patch taskMetadataPatch
	if v := imFlag(cmd, "name"); v != "" {
		patch.Name = &v
	}
	if v := imFlag(cmd, "remark"); v != "" {
		patch.Remark = &v
	}
	if v := imFlag(cmd, "policy"); v != "" {
		patch.AssetScopePolicy = &v
	}
	if raw := imFlag(cmd, "budget"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < -1 {
			return "budget 必须是 >= -1 的整数（-1=继承全局，0=不限）。", nil, fmt.Errorf("invalid budget")
		}
		patch.CredentialAttemptBudget = &n
	}
	if patch.Name == nil && patch.Remark == nil && patch.AssetScopePolicy == nil && patch.CredentialAttemptBudget == nil {
		return "用法：/edit <task_id> [--name 名称] [--remark 备注] [--policy exact|target] [--budget N]", nil, fmt.Errorf("no fields")
	}
	updated, status, err := svc.s.updateTaskMetadataCore(t.ID, patch)
	if err != nil {
		return fmt.Sprintf("更新失败（%d）：%s", status, err.Error()), nil, err
	}
	id, _ := strconv.ParseInt(updated.ID, 10, 64)
	return fmt.Sprintf("已更新任务 #%s。", updated.ID), &id, nil
}

func (svc *imService) commandScope(cmd imCommand) (string, *int64, error) {
	t, err := svc.taskByID(imFirstArg(cmd))
	if err != nil {
		return err.Error(), nil, err
	}
	id, _ := strconv.ParseInt(t.ID, 10, 64)
	op := "list"
	if len(cmd.Args) > 1 {
		op = strings.ToLower(strings.TrimSpace(cmd.Args[1]))
	}
	if v := imFlag(cmd, "op"); v != "" {
		op = strings.ToLower(v)
	}
	as := svc.s.m.Assets()
	if as == nil {
		return "资产库未启用。", &id, fmt.Errorf("assets disabled")
	}
	switch op {
	case "list":
		rows, err := as.ListTaskScope(id)
		if err != nil {
			return "读取范围失败：" + err.Error(), &id, err
		}
		if len(rows) == 0 {
			return "该任务暂无显式范围行。", &id, nil
		}
		var b strings.Builder
		for _, sc := range rows {
			fmt.Fprintf(&b, "#%d %s %s (%s)\n", sc.ID, sc.Kind, imScopeLabel(sc), sc.Source)
			if b.Len() > 3000 {
				break
			}
		}
		return strings.TrimRight(b.String(), "\n"), &id, nil
	case "add":
		kind, value := "", ""
		if len(cmd.Args) > 3 {
			kind, value = cmd.Args[2], cmd.Args[3]
		}
		if v := imFlag(cmd, "kind"); v != "" {
			kind = v
		}
		if v := imFlag(cmd, "value"); v != "" {
			value = v
		}
		ts, err := as.AddAgentScope(id, strings.TrimSpace(kind), strings.TrimSpace(value), "IM 添加", "im")
		if err != nil {
			return "添加失败：" + err.Error(), &id, err
		}
		return fmt.Sprintf("已添加范围 #%d %s %s。", ts.ID, ts.Kind, imScopeLabel(ts)), &id, nil
	case "del":
		raw := ""
		if len(cmd.Args) > 2 {
			raw = cmd.Args[2]
		}
		if v := imFlag(cmd, "scope-id"); v != "" {
			raw = v
		}
		scopeID, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || scopeID <= 0 {
			return "用法：/scope <task_id> del <scope_id>", &id, fmt.Errorf("invalid scope id")
		}
		ok, err := as.DeleteTaskScope(id, scopeID)
		if err != nil {
			return "删除失败：" + err.Error(), &id, err
		}
		if !ok {
			return "范围行不存在。", &id, fmt.Errorf("scope not found")
		}
		return fmt.Sprintf("已删除范围 #%d。", scopeID), &id, nil
	default:
		return "用法：/scope <task_id> [list | add kind value | del scope_id]", &id, fmt.Errorf("unknown scope op")
	}
}

func imScopeLabel(sc db.TaskScope) string {
	switch {
	case sc.CompanyName != "":
		return sc.CompanyName
	case sc.Domain != "":
		return sc.Domain
	case sc.Net != "":
		return sc.Net
	case sc.Value != "":
		return sc.Value
	default:
		return ""
	}
}

func imHelpText(role string) string {
	base := `可用命令：
/tasks [all|running|paused|done] [n] — 任务列表
/show <task_id> — 任务详情
/scope <task_id> list — 查看资产范围
/help — 本帮助`
	if role != "operator" {
		return base + "\n\n当前为只读账号：创建、暂停、继续、编辑和修改范围需要 operator 权限。"
	}
	return base + `

/new <目标> [--name 名称] [--policy exact|target] [--scope kind:value] [--timeout 秒] — 创建任务
/pause <task_id> — 暂停任务
/resume <task_id> — 继续任务
/edit <task_id> [--name 名称] [--remark 备注] [--policy exact|target] [--budget N] — 编辑元数据
/scope <task_id> [add kind value | del scope_id] — 修改资产范围

也可以直接发自然语言，例如“列出正在运行的任务”或“新建一个对 example.com 的测试任务”。`
}
