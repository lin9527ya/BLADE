package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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
			"timeout_ms":  intp("可选硬超时毫秒（到点杀进程并记 hard_timeout）；省略=按平台默认最大存活时长（默认 120 分钟，可在设置里调；不要用它当脚本超时）"),
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

func (t *ToolSet) grepExecution() actool.CoreTool {
	return readTool("grep_execution",
		"在后台执行的**完整输出文件**里按正则逐行检索，返回命中行（带行号）。\n"+
			"★ 扫描类命令（nmap / ffuf / nuclei）的命中行常淹在几千行里；get_execution 只回头部与尾部，"+
			"要在中间找命中就用本工具，比让模型自己拼 grep 更省事、也不会漏。\n"+
			"★ 输出文件很大时只扫描前 64MB；需要更多请缩小 pattern 或直接用输出文件路径自己处理。",
		obj(map[string]any{
			"execution_id": idp("run_execution 返回的 execution_id"),
			"pattern":      str("正则（Go/RE2 语法，区分大小写）"),
			"limit":        intp("最多返回多少条命中行（默认 50，上限 200）"),
		}, "execution_id", "pattern"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !t.execToolsReady() {
				return actool.Errorf("grep_execution: 执行服务未接线"), nil
			}
			var a struct {
				ID      json.RawMessage `json:"execution_id"`
				Pattern string          `json:"pattern"`
				Limit   int             `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("grep_execution: execution_id 必填"), nil
			}
			pat := strings.TrimSpace(a.Pattern)
			if pat == "" {
				return actool.Errorf("grep_execution: pattern 必填"), nil
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("grep_execution: 正则不合法: %v", err)), nil
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			if limit > 200 {
				limit = 200
			}
			st, err := t.execService.Get(ctx, id)
			if err != nil {
				return actool.Errorf("grep_execution: " + err.Error()), nil
			}
			if st.OutputPath == "" {
				return actool.Errorf(fmt.Sprintf("grep_execution: 执行 %d 还没有输出文件（可能尚未开始）", id)), nil
			}
			hits, scanned, err := grepFile(st.OutputPath, re, limit, execGrepMaxBytes)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("grep_execution: 读输出文件失败: %v", err)), nil
			}
			if len(hits) == 0 {
				return actool.Text(fmt.Sprintf("（在 %s 扫描的 %d 行里没有命中 %q）", st.OutputPath, scanned, pat)), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "在 %s 扫描 %d 行，命中 %d 条（最多显示 %d）：\n", st.OutputPath, scanned, len(hits), limit)
			for _, h := range hits {
				fmt.Fprintf(&b, "%d:%s\n", h.line, h.text)
			}
			return actool.Text(b.String()), nil
		})
}

// execGrepMaxBytes 是 grep_execution 单次最多扫描的字节数——输出文件可能上 GB，
// 全量扫会拖住工具调用；64MB 足够覆盖常规扫描结果的前部命中。
const execGrepMaxBytes = 64 << 20

type grepHit struct {
	line int
	text string
}

// grepFile 逐行匹配，命中即收集（最多 limit 条）。返回命中、扫描行数与错误。
func grepFile(path string, re *regexp.Regexp, limit int, maxBytes int64) ([]grepHit, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var (
		hits    []grepHit
		scanned int
		read    int64
	)
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			read += int64(len(line))
			scanned++
			if re.MatchString(strings.TrimRight(line, "\n")) {
				hits = append(hits, grepHit{line: scanned, text: strings.TrimRight(line, "\n")})
				if len(hits) >= limit {
					return hits, scanned, nil
				}
			}
		}
		if err != nil || (maxBytes > 0 && read >= maxBytes) {
			break
		}
	}
	return hits, scanned, nil
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
