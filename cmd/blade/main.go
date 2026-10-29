// Command blade runs the BLADE backend: the dual SQLite graph stores,
// the event-driven exploration engine, and the JSON HTTP API consumed by the
// shadcn/ui frontend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/config"
	"github.com/lin9527ya/blade/selfupdate"
	"github.com/lin9527ya/blade/server"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=<tag>". Defaults to "dev" for local builds.
var version = "dev"

const banner = `
    _    ____ _____ _______  __
   / \  |  _ \_   _| ____\ \/ /
  / _ \ | |_) || | |  _|  \  /
 / ___ \|  _ < | | | |___ /  \
/_/   \_\_| \_\|_| |_____/_/\_\
`

// printBanner writes the startup banner + version/runtime info to stdout.
func printBanner(addr string) {
	fmt.Print(banner)
	fmt.Println("  AI 自主渗透测试系统")
	fmt.Printf("  版本 %s  ·  %s/%s  ·  %s  ·  监听 %s\n\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), addr)
}

// shutdownDrainGrace 是关停时等 in-flight run 收尾的上限。它直接决定退出（以及一键更新
// 换装）要等多久，所以必须有界：到点就走，残留的 running 意图由下次启动的
// ResetRunningIntents 兜回 open 重新执行。
const shutdownDrainGrace = 20 * time.Second

// main only maps run's result onto the process exit code. The exit code is part
// of the update protocol — the supervising start script reads it to decide
// whether to relaunch us (see selfupdate.ExitRestart) — so the body has to live
// in a function that can *return* rather than os.Exit past its own defers.
func main() {
	// 关掉 SDK 的 Bash 后台任务（AGENT_CORE_DISABLE_BACKGROUND_TASKS）：这类任务不进
	// 执行台账、run 一结束就被杀，而收尾门只看台账——一条"超时被自动转后台"的扫描会在
	// 意图收笔后静默消失，谁也说不清它到底跑没跑完。长任务一律走 run_execution
	// （独立进程组、输出落盘、进台账、可 grep）。确有需要时用 BLADE_ALLOW_BACKGROUND_BASH=1 放行。
	if strings.TrimSpace(os.Getenv("BLADE_ALLOW_BACKGROUND_BASH")) == "" {
		_ = os.Setenv("AGENT_CORE_DISABLE_BACKGROUND_TASKS", "1")
	}
	os.Exit(run())
}

// listenIsLoopback 判一个监听地址是不是只绑本机。空 host（如 ":9898"、"0.0.0.0:9898"）
// 表示绑全部网卡 → **不是** loopback。只认 127.0.0.0/8、::1、localhost。
func listenIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// 没有端口部分的写法（如裸 "9898"）交给 net.Listen 判，这里保守地当成"非 loopback"，
		// 宁可多喊一句告警，也不要漏报一次暴露。
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func run() int {
	var (
		// 默认**只绑 loopback**（P42）：测绘引擎与个人扫描收录的前提是"扫到你的端口"，
		// 所以默认不暴露是唯一有效的手段——要对外必须先显式写出来（`-addr :9898`）。
		// 公网部署的两条正路（Cloudflare Tunnel / SSH 隧道）都不需要它监听公网，见 deploy/。
		addr    = flag.String("addr", "127.0.0.1:9898", "HTTP listen address (default: loopback only)")
		dataDir = flag.String("data", filepath.Join(config.BaseDir(), "data"), "data directory for SQLite stores (default: data/ next to the executable)")
		proxy   = flag.String("proxy", "127.0.0.1:9899", "traffic recording proxy address (empty to disable)")
		// 只在明确知道自己在做什么时才允许把流量代理开成对外——见下面 run() 里的护栏。
		allowOpenProxy = flag.Bool("allow-open-proxy", false, "allow binding the traffic proxy to a non-loopback address (turns this host into an unauthenticated open proxy)")
	)
	flag.Parse()

	// ---- 暴露面护栏（P42）----
	//
	// 流量代理由 go-mitmproxy 提供，它的 Options 里**没有**来源过滤/认证 hook（只有
	// Addr/SslInsecure/CaRootPath 等），所以"绑到公网"就等于开了一个**无认证的开放代理**：
	// 任何人都能借它上网，测绘引擎还会直接把它标成 open proxy。这类状态一旦出现就是实质危险
	// （你在为别人的攻击提供跳板），所以这里 **fail-closed**：默认拒绝启动，要开放必须显式加
	// -allow-open-proxy。宁可服务起不来、日志里写得明明白白，也不要静默变成一个跳板。
	//
	// 放在 flag 解析之后、**碰数据库之前**：拒绝启动时不该连库、不该连 LLM。
	if *proxy != "" && !listenIsLoopback(*proxy) && !*allowOpenProxy {
		log.Printf("[安全] 拒绝启动：流量代理绑在 %s（非 loopback）。go-mitmproxy 不校验任何来源，"+
			"这会让本机成为**无认证的开放代理**——任何人可借它上网，测绘引擎会直接标记 open proxy。\n"+
			"  改回 127.0.0.1:9899（默认），或确需如此时显式加 -allow-open-proxy。", *proxy)
		return 1
	}
	if *proxy != "" && !listenIsLoopback(*proxy) {
		log.Printf("[安全] 流量代理已按显式要求绑在 %s：本机现在是**无认证的开放代理**，请确认这是你要的", *proxy)
	}
	// 管理端绑非 loopback 是**正常部署**（反代/隧道场景），不拦，但提醒一句正确的暴露方式。
	if !listenIsLoopback(*addr) {
		log.Printf("[安全] 管理端绑在 %s（非 loopback）：请确保它只对该信任的来源可达——"+
			"走 Cloudflare / 反代时用防火墙只放行其来源段，或改用 Cloudflare Tunnel / SSH 隧道"+
			"让源站零入站端口（见 deploy/）", *addr)
	}

	// hand the build version to the server package so GET /api/health can report it
	// to the frontend top bar.
	server.BuildVersion = version

	printBanner(*addr)

	// capture backend logs into the in-memory sink (still to stderr) so the /logs
	// page can show a live log stream. Do this first, to catch startup logs too.
	server.StartLogCapture()
	// Startup threshold: env applies before the DB exists; the stored setting is
	// applied once the server is constructed (see below).
	server.InitLogLevel(os.Getenv("BLADE_LOG_LEVEL"), "")

	// Self-update bootstrap: swap in a staged binary, or count a post-swap boot
	// attempt and roll back if the new build keeps dying. Must run before we open
	// the stores or bind a port — this may end with "exit and let the start script
	// relaunch me", and there is no point paying for either first.
	action, upState := selfupdate.Bootstrap()
	server.SetBootUpdateState(upState)
	if action == selfupdate.Restart {
		return selfupdate.ExitRestart
	}

	// surface which config file the binary reads (absolute, so `go run`'s relative
	// "config.json" — resolved against the CWD — is unambiguous).
	cfgPath := config.Path()
	if abs, e := filepath.Abs(cfgPath); e == nil {
		cfgPath = abs
	}
	if _, e := os.Stat(cfgPath); e == nil {
		log.Printf("[config] 配置文件: %s", cfgPath)
	} else {
		log.Printf("[config] 配置文件: %s (不存在 — 将仅尝试环境变量 BLADE_PG_DSN)", cfgPath)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := shutdownContext(sigCtx)
	defer shutdown(agent.AbortShutdown)

	mgr, err := server.NewManager(*dataDir, *proxy)
	if err != nil {
		log.Fatalf("open stores: %v", err)
	}
	defer mgr.Close()

	skillDir := config.SkillDir()
	if abs, err := filepath.Abs(skillDir); err == nil {
		skillDir = abs
	}
	log.Printf("[config] skill 目录: %s", skillDir)
	// agent 降权（可选）：BLADE_AGENT_USER 设置后，Bash / 长任务 / 自定义工具以该用户
	// 运行、文件工具被限制在 data+skill 目录内、子进程环境剔除疑似凭据。未设置时是
	// no-op（保持 agent 与服务端同权的旧行为，见 deploy/README.md「信任模型」）。
	// File-tool root confinement is independent of privilege dropping: install it
	// even when BLADE_AGENT_USER is unset.
	if err := server.InstallFileRootGuard(*dataDir, skillDir); err != nil {
		log.Fatalf("[file-guard] 安装文件工具边界失败：%v", err)
	}
	if err := server.InstallAgentIsolation(*dataDir, config.BaseDir(), skillDir); err != nil {
		log.Fatalf("[isolation] %v", err)
	}
	srv := server.New(ctx, mgr, skillDir, *dataDir, config.BaseDir())
	server.ApplyStoredLogLevel(mgr.PG())
	// 重启恢复：把 PG 里已准入的任务重新拉起（构造器只装配，不在这里恢复——
	// 见 Server.RestoreTaskRuntimes 的注释）。
	srv.RestoreTaskRuntimes()
	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
		// 请求头 10s：慢速握手（slowloris）在读完首行之前就被挡掉。
		ReadHeaderTimeout: 10 * time.Second,
		// 读整个请求（含体）30s。本服务的请求体都是小 JSON；请求读完之后不再读，
		// 因此长连接的 SSE 响应不受它影响。
		ReadTimeout: 30 * time.Second,
		// 空闲（keep-alive、无在途请求）连接 2 分钟后回收，避免连接堆积占 fd。
		IdleTimeout: 2 * time.Minute,
		// **有意不设 WriteTimeout**：/api/logs/stream、探索活动流、旁路提问、更新进度都是
		// SSE 长连接（server.go:3065/3137、side_questions.go:544、update.go:339），而
		// WriteTimeout 是按整段响应计时的——设了会把它们按「写了 N 秒」掐断，表现为
		// 「活动流跑一会儿就断」。要限制单次响应的写入时长，应在具体 handler 里用
		// http.ResponseController 按需设 deadline，而不是在这里一刀切。
	}

	// 升级稳定确认必须从「监听 + 任务恢复都完成」之后才开始计时：此前计时从
	// Bootstrap 后就启动，大库恢复/迁移超过 SettleDelay 时会在还没开始服务前就
	// 认定新版本稳定，失败的升级被误确认。恢复已在上面的 RestoreTaskRuntimes 完成。
	go func() {
		log.Printf("BLADE %s backend listening on %s (data=%s, workers=%d)", version, *addr, *dataDir, mgr.Workers())
		settle := time.AfterFunc(selfupdate.SettleDelay, selfupdate.Settle)
		defer settle.Stop()
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Two ways out: a signal (normal stop → exit 0, the start script stops looping)
	// or a staged update / rollback (→ exit 75, the script relaunches us and the
	// bootstrap above installs the new build).
	code := 0
	select {
	case <-ctx.Done():
	case <-server.RestartRequested():
		code = selfupdate.ExitRestart
		shutdown(agent.AbortShutdown)
	}

	log.Println("shutting down...")
	// 先把取消投出去（幂等，首次 cause 生效），再等 run 收尾——否则 drain 可能看到
	// inflight=0（run 还没收到取消）就立刻退出，等了个寂寞。
	shutdown(agent.AbortShutdown)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)

	// 等正在跑的 run 把终态写进库，再让 defers 去关连接池（mgr.Close）：worker 的收尾写入
	// 发生在收到取消**之后**，池子一关，那次写入就变成 "sql: database is closed"、意图停在
	// running。有界——到点照常退出，残留状态由下次启动的 ResetRunningIntents 兜住。
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), shutdownDrainGrace)
	defer cancelDrain()
	if busy := srv.WaitAllQuiescent(drainCtx); busy > 0 {
		log.Printf("[shutdown] 等满 %s 仍有 %d 处 run 未收尾：其意图终态未落库，下次启动会重置为 open",
			shutdownDrainGrace, busy)
	}
	return code
}

// shutdownContext deliberately does not derive from signalCtx. If it did, the
// parent's plain context.Canceled could win the race before AbortShutdown was
// attached to the child, losing the diagnostic cause in every running Agent.
func shutdownContext(signalCtx context.Context) (context.Context, context.CancelCauseFunc) {
	ctx, shutdown := context.WithCancelCause(context.Background())
	go func() {
		select {
		case <-signalCtx.Done():
			shutdown(agent.AbortShutdown)
		case <-ctx.Done():
		}
	}()
	return ctx, shutdown
}
