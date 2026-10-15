package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
)

// P13 长任务执行工具：与 Bash 的关键区别是**活过本次 agent run**——命令交给 host
// 的执行服务后立即返回 execution_id，worker 可以继续干别的或续等；run 结束、任务
// 暂停、服务恢复都不再静默杀进程。收尾门（有活跃执行时意图不许判完）以此为准。

// execToolsReady 报告执行服务是否已注入（未注入时工具给出可读拒绝）。
func (t *ToolSet) execToolsReady() bool { return t.execService != nil }

func (t *ToolSet) runExecution() actool.CoreTool {
	return writeTool("run_execution",
		"把一条【预计较久】的命令交给后台执行（扫描、目录爆破、批量枚举、字典跑批），立即返回 execution_id 与输出文件的【绝对路径】。\n"+
			"★ 长任务一律用本工具，不要用 Bash 硬等（Bash 有单次时长上限且随本次运行结束被杀）。\n"+
			"★ 超时是观察不是失败：wait_execution 到点仍 running 属正常，可继续等或先做别的；"+
			"get_execution 读中途输出；cancel_execution 取消（理由会记录下来）。\n"+
			"★ 只要还有执行在跑，你就**不能**声称这条意图探完了——先等它收尾。",
		obj(map[string]any{
			"command":     str("要执行的 shell 命令（完整、非交互；长脚本请先 write_file 落盘再执行短命令）"),
			"timeout_ms":  intp("可选硬超时毫秒（到点杀进程并记 hard_timeout）；省略=不限，由你/任务收尾控制"),
			"description": str("一句话说明这条命令在做什么（进台账，便于事后复盘）"),
		}, "command"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !t.execToolsReady() {
				return actool.Errorf("run_execution: 执行服务未接线（当前运行环境不支持后台执行），请改用 Bash"), nil
			}
			var a struct {
				Command     string `json:"command"`
				TimeoutMs   int    `json:"timeout_ms"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Command) == "" {
				return actool.Errorf("run_execution: command 不能为空"), nil
			}
			var timeout time.Duration
			if a.TimeoutMs > 0 {
				timeout = time.Duration(a.TimeoutMs) * time.Millisecond
			}
			st, err := t.execService.Submit(ctx, t.taskIDStr(), t.ownerNode, t.worker, strings.TrimSpace(a.Command), strings.TrimSpace(a.Description), timeout)
			if err != nil {
				return actool.Errorf("run_execution: " + err.Error()), nil
			}
			raw, _ := json.Marshal(st)
			return actool.Text(fmt.Sprintf("execution started: %d\n%s\n提示：超时是观察不是失败——wait_execution(%d, timeout_ms) 续等；只要它还在跑，就不能声称本条意图探完了。", st.ID, raw, st.ID)), nil
		})
}

func (t *ToolSet) waitExecution() actool.CoreTool {
	return readTool("wait_execution",
		"有界等待一次后台执行：到点仍是 running 就返回当前状态与部分输出——**这是正常观察，不是失败**。exit_code/error 仅在已终态时才有意义。",
		obj(map[string]any{
			"execution_id": idp("run_execution 返回的 execution_id"),
			"timeout_ms":   intp("本次等待上限（毫秒，默认 60000，上限 600000）"),
		}, "execution_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !t.execToolsReady() {
				return actool.Errorf("wait_execution: 执行服务未接线"), nil
			}
			var a struct {
				ID        json.RawMessage `json:"execution_id"`
				TimeoutMs int             `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("wait_execution: execution_id 必填"), nil
			}
			timeout := time.Duration(a.TimeoutMs) * time.Millisecond
			if timeout <= 0 {
				timeout = 60 * time.Second
			}
			if timeout > 10*time.Minute {
				timeout = 10 * time.Minute
			}
			st, err := t.execService.Wait(ctx, id, timeout)
			if err != nil {
				return actool.Errorf("wait_execution: " + err.Error()), nil
			}
			return execSnapshotResult("wait", st), nil
		})
}

func (t *ToolSet) getExecution() actool.CoreTool {
	return readTool("get_execution",
		"读一次后台执行的当前快照（状态、退出码、输出文件绝对路径与部分输出尾部）。",
		obj(map[string]any{"execution_id": idp("run_execution 返回的 execution_id")}, "execution_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !t.execToolsReady() {
				return actool.Errorf("get_execution: 执行服务未接线"), nil
			}
			var a struct {
				ID json.RawMessage `json:"execution_id"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("get_execution: execution_id 必填"), nil
			}
			st, err := t.execService.Get(ctx, id)
			if err != nil {
				return actool.Errorf("get_execution: " + err.Error()), nil
			}
			return execSnapshotResult("get", st), nil
		})
}

func (t *ToolSet) cancelExecution() actool.CoreTool {
	return writeTool("cancel_execution",
		"取消一次后台执行（杀进程组）。reason 会写进台账并回传——写清为什么取消，便于复盘。",
		obj(map[string]any{
			"execution_id": idp("run_execution 返回的 execution_id"),
			"reason":       str("取消原因（必填）"),
		}, "execution_id", "reason"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !t.execToolsReady() {
				return actool.Errorf("cancel_execution: 执行服务未接线"), nil
			}
			var a struct {
				ID     json.RawMessage `json:"execution_id"`
				Reason string          `json:"reason"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.ID)
			reason := strings.TrimSpace(a.Reason)
			if id <= 0 || reason == "" {
				return actool.Errorf("cancel_execution: execution_id 与 reason 必填"), nil
			}
			ok, err := t.execService.Cancel(ctx, id, reason)
			if err != nil {
				return actool.Errorf("cancel_execution: " + err.Error()), nil
			}
			if !ok {
				return actool.Text(fmt.Sprintf("execution %d 已不在运行（无需取消）。取消理由已记录：%s", id, reason)), nil
			}
			return actool.Text(fmt.Sprintf("execution %d 已取消（理由已记录并回传：%s）。取消是一次完成的操作，不是失败。", id, reason)), nil
		})
}

// taskIDStr 把 ToolSet 的数字任务 id 转成台账的字符串口径。
func (t *ToolSet) taskIDStr() string {
	if t.taskID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", t.taskID)
}

// execSnapshotResult 渲染一次执行快照；running 时明确标注「超时是观察不是失败」。
func execSnapshotResult(verb string, st ExecStatus) actool.Result {
	raw, _ := json.Marshal(st)
	note := ""
	if ExecActive(st.Status) {
		note = "\n（仍是 running：超时是观察不是失败——可继续 wait_execution，或先做别的；执行在跑期间不得声称意图已探完）"
	}
	return actool.Text(fmt.Sprintf("execution %s: %d [%s]%s\n%s", verb, st.ID, st.Status, note, raw))
}
