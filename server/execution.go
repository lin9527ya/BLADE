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
	// 同时执行数上限：与 P24 的资源限额互补——那三条限的是「每一条能吃多少」（ulimit 前导），
	// 这条限的是「同时有几条」。单条被 CPU 限额兜住，但 agent 若起了一百条，机器照样被打满。
	// 0 = 不限。读不到计数就放行（与资产闸门同一条纪律：一次 DB 抖动不该挡死 agent 的正常工具
	// 调用），但必须留日志——不静默。
	if maxActive := execMaxActiveFromSettings(s.pg); maxActive > 0 {
		if n, err := s.pg.CountActiveExecutions(taskID); err != nil {
			log.Printf("[exec] 活跃执行数读取失败，本次不拦: %v", err)
		} else if n >= maxActive {
			return agent.ExecStatus{}, fmt.Errorf(
				"本任务同时运行的后台执行已达上限（%d/%d）：先 wait_execution 等已有的收尾，或用 cancel_execution 取消一条，再起新的", n, maxActive)
		}
	}
	logDir := filepath.Join(s.dir, "tasks", taskID, "executions")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return agent.ExecStatus{}, err
	}
	id, err := s.pg.CreateExecution(&db.ToolExecution{
		TaskID: taskID, IntentID: intentID, Worker: worker, Tool: tool,
		Command: command, Description: strings.TrimSpace(desc),
	})
	if err != nil {
		return agent.ExecStatus{}, err
	}
	path, err := filepath.Abs(filepath.Join(logDir, fmt.Sprintf("%d.log", id)))
	if err != nil {
		path = filepath.Join(logDir, fmt.Sprintf("%d.log", id))
	}
	if err := s.pg.SetExecutionOutputPath(id, path); err != nil {
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
			time.AfterFunc(execCancelGrace, func() { hardKillGroup(cmd) })
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
	if run.hardTimer != nil {
		run.hardTimer.Stop()
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
func execMaxActiveFromSettings(pg *db.DB) int {
	if pg == nil {
		return defaultExecMaxActive
	}
	v, ok, _ := pg.GetSetting(settingExecMaxActive)
	if !ok || strings.TrimSpace(v) == "" {
		return defaultExecMaxActive
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return defaultExecMaxActive
	}
	return n
}
