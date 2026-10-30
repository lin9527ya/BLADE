package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// P13 执行服务：把长任务（扫描、目录爆破、批量枚举、字典跑批）从 agent run 里摘出来
// 托管——进程独立会话组存活（暂停/换 run/worker 收笔都不杀它），输出落盘（绝对路径），
// 状态进 tool_executions 台账。意图收尾门（有活跃执行不许判完）与租约看门狗都以本
// 服务与台账为准。接口定义见 agent.ExecService。

const (
	execTailBytes      = 8 * 1024 // 内存里保留的输出尾巴
	execHeadBytes      = 8 * 1024 // 快照里回给模型的输出头部（扫描类命令的关键信息常在最前）
	execWaitTick       = 200 * time.Millisecond
	execCancelGrace    = 3 * time.Second // SIGTERM → SIGKILL 的宽限
	execReconcileGap   = 45 * time.Second
	execMaxCommandRune = 8192

	// execWaitDelay 是「直接子进程已退出、但输出管道还没关」时的强制收尾时限。
	//
	// 必须设：cmd.Stdout 是 io.Writer 而不是 *os.File，Go 会建一条管道并起 goroutine
	// 拷数据；命令用 `scanner &` 起的后台子进程如果继承了 stdout，就**一直**握着写端，
	// Wait 会卡到它退出为止——表现为台账永远 running、收尾门永远悬着，而 shell 早就退了。
	// 到点后 Wait 关闭管道并返回 exec.ErrWaitDelay（命令本身的状态仍看 ProcessState）。
	execWaitDelay = 2 * time.Second
)

// ExecutionService 托管全部在跑的长任务。
type ExecutionService struct {
	pg  *db.DB
	dir string // 数据目录；输出落 <dir>/tasks/<taskID>/executions/<id>.log

	mu     sync.Mutex
	live   map[int64]*execRun
	onDone func(*db.ToolExecution)
	// envFor 由 host 注入：托管执行必须拿到与 agent Bash 同一份环境（记录代理 +
	// CA 信任），否则长任务的流量绕过 MITM——证据链断在这里，而且 TLS 会失败。
	envFor func() []string
}

type execRun struct {
	id         int64
	taskID     string
	intentID   int64
	cmd        *exec.Cmd
	logFile    *os.File
	tail       *tailRing
	bytes      atomic.Int64
	cancelled  atomic.Bool
	hardKilled atomic.Bool
	done       chan struct{}
	hardTimer  *time.Timer
	// graceTimer 是硬超时后的 SIGKILL 宽限定时器。必须在 reap 里停掉：进程若在宽限期
	// 内退出，pid 可能被复用，到点再对 -pid 发 SIGKILL 就会误伤无关进程组。
	graceTimer *time.Timer
}

// NewExecutionService builds the service over the PG ledger and the data dir.
func NewExecutionService(pg *db.DB, dir string) *ExecutionService {
	return &ExecutionService{pg: pg, dir: dir, live: map[int64]*execRun{}}
}

// SetOnDone registers the completion callback (the engine wakes the planner and lets
// the owning intent resume when its execution settles).
func (s *ExecutionService) SetOnDone(fn func(*db.ToolExecution)) {
	s.mu.Lock()
	s.onDone = fn
	s.mu.Unlock()
}

// SetEnvResolver 注入每次提交时求值的子进程环境（记录代理地址与 CA 会随开关变化，
// 所以按次读取而不是构造时固定）。
func (s *ExecutionService) SetEnvResolver(fn func() []string) {
	s.mu.Lock()
	s.envFor = fn
	s.mu.Unlock()
}

// Submit starts one detached execution and returns its initial snapshot.
func (s *ExecutionService) Submit(ctx context.Context, taskID string, intentID int64, worker, command, desc string, timeout time.Duration) (agent.ExecStatus, error) {
	return s.SubmitTool(ctx, taskID, intentID, worker, "run_execution", command, desc, timeout)
}

// SubmitTool is Submit with an explicit ledger tool label. The label is what the
// execution UI/audit shows; callers that are not run_execution (cve_exploit) use it
// so a script run is distinguishable from a generic shell command.
func (s *ExecutionService) SubmitTool(ctx context.Context, taskID string, intentID int64, worker, tool, command, desc string, timeout time.Duration) (agent.ExecStatus, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "run_execution"
	}
	if len(tool) > 40 {
		return agent.ExecStatus{}, errors.New("执行工具标签过长")
	}
	for _, r := range tool {
		if (r < 'a' || r > 'z') && r != '_' {
			return agent.ExecStatus{}, errors.New("执行工具标签只能包含 a-z 与 _")
		}
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return agent.ExecStatus{}, errors.New("命令为空")
	}
	if len([]rune(command)) > execMaxCommandRune {
		return agent.ExecStatus{}, fmt.Errorf("命令过长（>%d 字符）：长脚本请先落盘再执行短命令", execMaxCommandRune)
	}
	// 配额在 DB 事务内检查并占位，不能 COUNT 后另起 INSERT。读不到设置或
	// 计数时拒绝本次提交，避免数据库故障把配置的硬上限变成不限。
	maxActive, err := execMaxActiveFromSettings(s.pg)
	if err != nil {
		return agent.ExecStatus{}, fmt.Errorf("读取执行并发上限失败: %w", err)
	}
	logDir := filepath.Join(s.dir, "tasks", taskID, "executions")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return agent.ExecStatus{}, err
	}
	id, err := s.pg.CreateExecutionWithinLimit(ctx, &db.ToolExecution{
		TaskID: taskID, IntentID: intentID, Worker: worker, Tool: tool,
		Command: command, Description: strings.TrimSpace(desc),
	}, maxActive)
	if err != nil {
		return agent.ExecStatus{}, err
	}
	path, err := filepath.Abs(filepath.Join(logDir, fmt.Sprintf("%d.log", id)))
	if err != nil {
		path = filepath.Join(logDir, fmt.Sprintf("%d.log", id))
	}
	if err := s.pg.SetExecutionOutputPath(id, path); err != nil {
		_ = s.pg.FinishExecution(id, db.ExecFailed, -1, 0, "", "无法记录输出路径："+err.Error())
		return agent.ExecStatus{}, err
	}
	logFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = s.pg.FinishExecution(id, db.ExecFailed, -1, 0, "", "无法创建输出文件："+err.Error())
		return agent.ExecStatus{}, err
	}

	// P24 资源限额：按当前设置给这次执行加 ulimit 前导，并把"请求了什么/哪些真生效"
	// 落进台账（本机不支持某轴时如实标注，不静默当成已限住）。
	lim := execLimitsFromSettings(s.pg)
	runCommand, snap := applyExecLimits(command, lim)
	if len(marshalExecLimits(snap)) > 0 && !lim.IsZero() {
		if lerr := s.pg.SetExecutionLimits(id, marshalExecLimits(snap)); lerr != nil {
			log.Printf("[exec] %d 限额快照落库失败: %v", id, lerr)
		}
	}
	argv := shellCommandArgs(runCommand)
	cmd := exec.Command(argv[0], argv[1:]...)
	startDetached(cmd)
	cmd.WaitDelay = execWaitDelay // 见常量注释：后台子进程握着 stdout 时不要卡死 Wait
	// 与 agent Bash 同环境同目录：走记录代理、信任 CA、相对产物落在本意图目录。
	if taskID != "" {
		cmd.Dir = agent.EnsureRunDir(s.dir, taskID, intentID)
	}
	s.mu.Lock()
	envFor := s.envFor
	s.mu.Unlock()
	if envFor != nil {
		cmd.Env = append(os.Environ(), envFor()...)
	}
	// 与 agent Bash 同一套隔离：降权到 BLADE_AGENT_USER + 清洗疑似凭据的环境变量
	// （未启用时 no-op）。放在 env 拼好之后，清洗看到的就是子进程最终会拿到的那份。
	hardenAgentCmd(cmd)
	run := &execRun{id: id, taskID: taskID, intentID: intentID, cmd: cmd, logFile: logFile,
		tail: newTailRing(execTailBytes), done: make(chan struct{})}
	w := io.MultiWriter(logFile, writerFunc(func(p []byte) (int, error) {
		_, _ = run.tail.Write(p)
		run.bytes.Add(int64(len(p)))
		return len(p), nil
	}))
	cmd.Stdout, cmd.Stderr = w, w
	// 先登记 live 再 Start：对账器以 live 为「本进程拥有」的依据，若在 Start 之后才登记，
	// 45 秒对账可能把这个刚启动的执行误标 orphaned。Start 失败时再从 live 摘掉。
	s.mu.Lock()
	s.live[id] = run
	s.mu.Unlock()
	if err := cmd.Start(); err != nil {
		s.mu.Lock()
		delete(s.live, id)
		s.mu.Unlock()
		logFile.Close()
		_ = s.pg.FinishExecution(id, db.ExecFailed, -1, 0, "", "启动失败："+err.Error())
		return agent.ExecStatus{}, err
	}
	if err := s.pg.MarkExecutionRunning(id); err != nil {
		log.Printf("[exec] %d 状态落 running 失败: %v", id, err)
	}
	if timeout <= 0 {
		timeout = execMaxAgeFromSettings(s.pg)
	}
	if timeout > 0 {
		run.hardTimer = time.AfterFunc(timeout, func() {
			run.hardKilled.Store(true)
			killGroup(cmd)
			// SIGTERM 不是每个进程都听：宽限后强杀整组，兑现"硬超时上限"的承诺
			// （否则僵住的进程会让台账一直挂在 running，收尾门也跟着悬着）。
			run.graceTimer = time.AfterFunc(execCancelGrace, func() {
				select {
				case <-run.done: // 已在宽限期内退出：别对可能被复用的 pid 再发信号
				default:
					hardKillGroup(cmd)
				}
			})
		})
	}
	goSafe("execution/reap", nil, func() { s.reap(run) })
	row, _ := s.pg.GetExecution(id)
	if row == nil {
		row = &db.ToolExecution{ID: id, TaskID: taskID, IntentID: intentID, Worker: worker,
			Tool: tool, Command: command, Description: strings.TrimSpace(desc),
			Status: db.ExecRunning, OutputPath: path}
	}
	st := toExecStatus(row, run)
	if !lim.IsZero() {
		if b := marshalExecLimits(snap); len(b) > 0 {
			st.Limits = string(b)
		}
	}
	return st, nil
}

// StageScript writes a server-generated, task-scoped script file for CVEScriptService.
// The path is never model-supplied. When agent isolation is on, the file and its
// parent directory are handed to the agent user so the dropped-privilege child can
// read the script (the execution log is inherited through an fd, so it can stay 0600).
func (s *ExecutionService) StageScript(_ context.Context, taskID string, _ int64, name string, content []byte) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || strings.ContainsAny(taskID, `/\`) || taskID == "." || taskID == ".." {
		return "", errors.New("无效的任务 id")
	}
	if len(content) == 0 {
		return "", errors.New("脚本为空")
	}
	if len(content) > 1<<20 {
		return "", errors.New("脚本不能超过 1 MiB")
	}
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		name = "script"
	}
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	dir := filepath.Join(s.dir, "tasks", taskID, "cve")
	if err := os.MkdirAll(dir, 0o710); err != nil {
		return "", err
	}
	// 脚本目录由服务端持有（agent 只读）：避免降权后的 agent 在目录里放 symlink，
	// 让 root 的 StageScript 顺着链接写到目录外。组给 agent 只留 x（可进入、不可列目录），
	// 文件 0640；随机文件名由 os.CreateTemp 生成，agent 无法预放同名 symlink。
	if agentIso.enabled {
		if err := os.Chown(dir, os.Geteuid(), int(agentIso.gid)); err != nil {
			return "", fmt.Errorf("把脚本目录交给 agent 组：%w", err)
		}
		_ = os.Chmod(dir, 0o710|os.ModeSetgid)
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		base = "script"
	}
	f, err := os.CreateTemp(dir, base+"-*"+ext)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o640); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if agentIso.enabled {
		if err := os.Lchown(path, os.Geteuid(), int(agentIso.gid)); err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("把脚本文件交给 agent 组：%w", err)
		}
	}
	return path, nil
}

// reap waits for the process, lands the terminal state in the ledger, and fires onDone.
func (s *ExecutionService) reap(run *execRun) {
	err := run.cmd.Wait()
	// WaitDelay 到点关管道时，若命令本身**成功退出**，Wait 返回 ErrWaitDelay 而不是 nil。
	// 那是"后台子进程还占着输出"，不是命令失败——按退出码判定，别误报 failed。
	if errors.Is(err, exec.ErrWaitDelay) && run.cmd.ProcessState != nil && run.cmd.ProcessState.Success() {
		err = nil
	}
	// 清场整组：`sh -c 'scanner &'` 这种写法会让 shell 先退出、后台子进程留在同一进程组里。
	// 只 Wait 不收尾的话它们就成了孤儿：没有 TTL（定时器随本进程内存一起消失）、没人再
	// 引用，一直跑下去，累积起来能把机器 CPU 吃满。直接子进程已退出，这里只可能杀到
	// 它留下的后台后代——正是要清的。
	//
	// 注：pid 已被 Wait 回收，理论上存在 PID 复用误伤的可能；窗口是 Wait 返回到 kill
	// 之间的几微秒，且 Setsid 让该 pid 同时是会话首进程，实际风险可忽略。
	hardKillGroup(run.cmd)
	if run.hardTimer != nil {
		run.hardTimer.Stop()
	}
	if run.graceTimer != nil {
		run.graceTimer.Stop()
	}
	_ = run.logFile.Close()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	status := db.ExecCompleted
	errMsg := ""
	switch {
	case run.hardKilled.Load():
		status = db.ExecHardTimeout
		errMsg = agent.AbortExecutionHardTimeout.Text
	case run.cancelled.Load():
		status = db.ExecCancelled
	case err != nil:
		status = db.ExecFailed
	}
	if status == db.ExecFailed && err != nil {
		errMsg = err.Error()
	}
	// 撞 CPU 限额：内核先发 SIGXCPU(24)，进程若继续再 SIGKILL(9)——具体收到哪个取决于
	// 进程是否处理了 SIGXCPU，所以两个都认。归类仍是 failed，但理由写明"被信号终止"，
	// 让台账与模型能区分"被自己的限额杀掉"和"命令本身报错"（限额快照在 limits 字段）。
	if status == db.ExecFailed {
		if sig, ok := signaledBy(run.cmd); ok && (sig == 24 || sig == 9) {
			errMsg = fmt.Sprintf("进程被信号 %d 终止（信号 24=CPU 限额 SIGXCPU，9=硬杀）：若本次设了 CPU 限额（ulimit -t）就是撞限的表现；台账 limits 字段有本次限额快照（原始错误：%s）", sig, errMsg)
		}
	}
	if ferr := s.pg.FinishExecution(run.id, status, exitCode, run.bytes.Load(), run.tail.String(), errMsg); ferr != nil {
		log.Printf("[exec] %d 终态落库失败: %v", run.id, ferr)
	}
	close(run.done)
	s.mu.Lock()
	delete(s.live, run.id)
	onDone := s.onDone
	s.mu.Unlock()
	log.Printf("[exec] %d 结束: %s exit=%d bytes=%d", run.id, status, exitCode, run.bytes.Load())
	if onDone != nil {
		if row, gerr := s.pg.GetExecution(run.id); gerr == nil && row != nil {
			onDone(row)
		}
	}
}

// Wait blocks up to timeout; a still-running result at the deadline is a completed
// observation, NOT an error (对齐 CyberStrikeAI 的语义纪律).
func (s *ExecutionService) Wait(ctx context.Context, id int64, timeout time.Duration) (agent.ExecStatus, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Get(ctx, id)
		if err != nil {
			return st, err
		}
		if !agent.ExecActive(st.Status) || time.Now().After(deadline) {
			return st, nil
		}
		s.mu.Lock()
		run := s.live[id]
		s.mu.Unlock()
		wait := execWaitTick
		if rem := time.Until(deadline); rem < wait {
			wait = rem
		}
		if wait < 0 {
			wait = 0
		}
		if run != nil {
			select {
			case <-run.done:
			case <-time.After(wait):
			case <-ctx.Done():
				return st, ctx.Err()
			}
		} else {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return st, ctx.Err()
			}
		}
	}
}

// Get returns the current snapshot, preferring the in-memory tail while live and
// persisting it best-effort so the page/agent see the same bytes.
func (s *ExecutionService) Get(ctx context.Context, id int64) (agent.ExecStatus, error) {
	row, err := s.pg.GetExecution(id)
	if err != nil {
		return agent.ExecStatus{}, err
	}
	if row == nil {
		return agent.ExecStatus{}, fmt.Errorf("execution %d 不存在", id)
	}
	s.mu.Lock()
	run := s.live[id]
	s.mu.Unlock()
	if run == nil {
		return toExecStatus(row, nil), nil
	}
	if tail := run.tail.String(); tail != row.PartialTail {
		_ = s.pg.TouchExecutionTail(id, tail, run.bytes.Load())
		row.PartialTail, row.OutputBytes = tail, run.bytes.Load()
	}
	return toExecStatus(row, run), nil
}

// Cancel kills one execution's process group and records why (the reason travels
// back to the model in the tool result).
func (s *ExecutionService) Cancel(ctx context.Context, id int64, reason string) (bool, error) {
	if strings.TrimSpace(reason) == "" {
		reason = agent.AbortExecutionCancelledByUser.Text
	}
	if err := s.pg.SetExecutionCancelNote(id, reason); err != nil {
		return false, err
	}
	s.mu.Lock()
	run := s.live[id]
	s.mu.Unlock()
	if run != nil {
		run.cancelled.Store(true)
		killGroup(run.cmd)
		goSafe("execution/cancel-grace", nil, func() {
			select {
			case <-run.done:
			case <-time.After(execCancelGrace):
				if run.cmd.Process != nil {
					hardKillGroup(run.cmd)
				}
			}
		})
		return true, nil
	}
	row, err := s.pg.GetExecution(id)
	if err != nil {
		return false, err
	}
	if row != nil && db.ExecutionActive(row.Status) {
		// 活跃行但本进程没有对应进程（重启遗留）：无进程可杀，落 cancelled 而不是假装成功。
		if ferr := s.pg.FinishExecution(id, db.ExecCancelled, -1, row.OutputBytes, row.PartialTail,
			agent.AbortExecutionCancelledByUser.Text); ferr != nil {
			return false, ferr
		}
		return true, nil
	}
	return false, nil
}

// Shutdown kills every live execution's process group and waits (bounded) for the
// reapers to write terminal rows.
//
// 为什么必须在停机时杀掉：托管进程是 Setsid 出来的（活过 agent run 是本功能的目的），
// 所以**服务退出不会顺带结束它们**。不杀的话，每次重启/更新都会留下一批还在跑的
// nmap/nuclei，且它们的台账行下次启动只会被标 orphaned——进程还在烧 CPU，输出也没人取。
// 关停是一次明确的停止：记 cancelled 并写清原因，而不是让它们变成没有归属的孤儿。
func (s *ExecutionService) Shutdown(reason string, wait time.Duration) int {
	if strings.TrimSpace(reason) == "" {
		reason = agent.AbortExecutionCancelledByUser.Text
	}
	s.mu.Lock()
	runs := make([]*execRun, 0, len(s.live))
	for _, run := range s.live {
		runs = append(runs, run)
	}
	s.mu.Unlock()
	if len(runs) == 0 {
		return 0
	}
	for _, run := range runs {
		run.cancelled.Store(true)
		_ = s.pg.SetExecutionCancelNote(run.id, reason)
		killGroup(run.cmd)
	}
	// SIGTERM 之后给一个**共用**的宽限，到点整组硬杀；期间等 reaper 把终态写进库。
	timeout := time.NewTimer(wait)
	defer timeout.Stop()
	for _, run := range runs {
		select {
		case <-run.done:
			continue
		case <-timeout.C:
		}
		break
	}
	// 到点仍未收尾的，整组 SIGKILL，再等一小段让 reaper 落库（进程已死，Wait 立刻返回）。
	remaining := 0
	grace := time.NewTimer(execCancelGrace)
	defer grace.Stop()
	for _, run := range runs {
		select {
		case <-run.done:
		default:
			remaining++
			hardKillGroup(run.cmd)
		}
	}
	for _, run := range runs {
		select {
		case <-run.done:
		case <-grace.C:
		}
	}
	if remaining > 0 {
		log.Printf("[exec] 停机：%d/%d 条执行未在 %s 内退出，已整组 SIGKILL", remaining, len(runs), wait)
	}
	return len(runs)
}

// CancelAll cancels every in-flight execution of one task (task stop/delete).
func (s *ExecutionService) CancelAll(taskID, reason string) int {
	rows, err := s.pg.ListActiveExecutions(taskID)
	if err != nil {
		return 0
	}
	n := 0
	for _, row := range rows {
		if ok, _ := s.Cancel(context.Background(), row.ID, reason); ok {
			n++
		}
	}
	return n
}

// ListForIntent returns every execution of one intent, newest first (执行收束摘要用).
func (s *ExecutionService) ListForIntent(intentID int64) ([]*db.ToolExecution, error) {
	return s.pg.ListExecutionsByIntent(intentID, 50)
}

// CountRanForIntent counts the intent's executions that ran to a terminal state —
// the negative-evidence gate's "有过真实执行记录" criterion.
func (s *ExecutionService) CountRanForIntent(intentID int64) (int64, error) {
	return s.pg.CountRanExecutions(intentID)
}

// ActiveForIntent lists the in-flight executions of one intent (收尾门与看门狗共用).
func (s *ExecutionService) ActiveForIntent(intentID int64) []*db.ToolExecution {
	rows, err := s.pg.ListActiveExecutionsByIntent(intentID)
	if err != nil {
		return nil
	}
	return rows
}

// ActiveForTask lists the in-flight executions of one task (settle drain 计入用).
func (s *ExecutionService) ActiveForTask(taskID string) []*db.ToolExecution {
	rows, err := s.pg.ListActiveExecutions(taskID)
	if err != nil {
		return nil
	}
	return rows
}

// ReconcileOrphaned marks in-flight ledger rows with no live process as orphaned
// (server restart / lost goroutine). Deliberately not "failed" — nothing was observed.
func (s *ExecutionService) ReconcileOrphaned() int64 {
	s.mu.Lock()
	live := make(map[int64]bool, len(s.live))
	for id := range s.live {
		live[id] = true
	}
	s.mu.Unlock()
	n, err := s.pg.ReconcileOrphanedExecutions(live, agent.AbortExecutionOrphaned.Text)
	if err != nil {
		log.Printf("[exec] 孤儿对账失败: %v", err)
		return 0
	}
	if n > 0 {
		log.Printf("[exec] 启动对账：%d 条遗留执行标记 orphaned", n)
	}
	return n
}

// StartOrphanReconciler runs the periodic sweep (restart leftovers + lost goroutines).
func (s *ExecutionService) StartOrphanReconciler(ctx context.Context) {
	s.ReconcileOrphaned()
	goSafe("execution/orphan-reconciler", nil, func() {
		t := time.NewTicker(execReconcileGap)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.ReconcileOrphaned()
			}
		}
	})
}

// readOutputHead 读文件头部 n 字节；读不到就返回空串（快照不该因此失败）。
func readOutputHead(path string, n int) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, n)
	m, _ := io.ReadFull(f, buf)
	return string(buf[:m])
}

// toExecStatus 转换台账行到跨层快照；live 非空时以内存尾巴/字节数为准。
func toExecStatus(row *db.ToolExecution, run *execRun) agent.ExecStatus {
	st := agent.ExecStatus{
		ID: row.ID, Status: row.Status, IntentID: row.IntentID, TaskID: row.TaskID,
		Tool: row.Tool, Command: row.Command, OutputPath: row.OutputPath,
		OutputBytes: row.OutputBytes, Tail: row.PartialTail, CancelNote: row.CancelNote, Error: row.Error,
		ExitCode: -1,
	}
	if row.ExitCode != nil {
		st.ExitCode = *row.ExitCode
	}
	// 头部与尾部都给：只看尾部会漏掉扫描输出最前面的命中行。
	st.Head = readOutputHead(row.OutputPath, execHeadBytes)
	if run != nil {
		st.Tail = run.tail.String()
		st.OutputBytes = run.bytes.Load()
	}
	if len(row.Limits) > 0 && string(row.Limits) != "{}" {
		st.Limits = string(row.Limits)
	}
	return st
}

// tailRing 是固定容量的字节尾巴（保留最近 N 字节，供部分输出展示）。
type tailRing struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func newTailRing(max int) *tailRing { return &tailRing{max: max} }

// writerFunc 让一个函数充当 io.Writer（输出同时进内存尾巴与字节计数）。
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func (r *tailRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	if len(r.data) > r.max {
		r.data = r.data[len(r.data)-r.max:]
	}
	return len(p), nil
}

func (r *tailRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.data)
}

const defaultExecMaxAgeMin = 120 // 2 小时；想不限就显式把设置写成 0

// execMaxAgeMinutes 是 execMaxAgeFromSettings 的分钟视图（设置页回显用）。
func execMaxAgeMinutes(pg *db.DB) int {
	d := execMaxAgeFromSettings(pg)
	if d <= 0 {
		return 0
	}
	return int(d.Minutes())
}

// execMaxAgeFromSettings 读单条托管执行的最大存活时长（0 = 不限）。
func execMaxAgeFromSettings(pg *db.DB) time.Duration {
	fallback := time.Duration(defaultExecMaxAgeMin) * time.Minute
	if pg == nil {
		return fallback
	}
	v, ok, _ := pg.GetSetting(settingExecMaxAge)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return fallback
	}
	return time.Duration(n) * time.Minute
}

// execMaxActiveFromSettings 读「本任务同时可有多少条后台执行」的上限（0 = 不限）。
// execMaxActiveSetting is for read-only configuration snapshots: a transient
// read failure falls back to the default. Submit uses the strict variant below.
func execMaxActiveSetting(pg *db.DB) int {
	n, err := execMaxActiveFromSettings(pg)
	if err != nil {
		return defaultExecMaxActive
	}
	return n
}

func execMaxActiveFromSettings(pg *db.DB) (int, error) {
	if pg == nil {
		return 0, errors.New("执行数据库不可用")
	}
	v, ok, err := pg.GetSetting(settingExecMaxActive)
	if err != nil {
		return 0, err
	}
	if !ok || strings.TrimSpace(v) == "" {
		return defaultExecMaxActive, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return defaultExecMaxActive, nil
	}
	return n, nil
}
