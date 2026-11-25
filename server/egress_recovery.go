package server

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/traffic"
)

// 出口代理自愈：代理池把 active 置为 blockedProxyURL（本机关闭端口 127.0.0.1:9）时，
// 所有 worker 的 Bash 都会立刻拿到 `exit 7 / HTTP 000`。此前这条路径只能等 30 秒一轮
// 的健康检查自己恢复——而 worker 看到一个"拒绝连接"的代理，会花几十轮去诊断它
// （任务 25 的 work#4 实测卡了 26 分钟，最后靠 planner 人工 steer 才纠正）。
//
// 这里补两条自愈：
//   1. 工具输出里出现"当前出口是黑洞"的证据时，**立刻**触发一次代理池复查，而不是等
//      下一个 30 秒 tick；
//   2. 复查确认全部候选都不可用时，写一条**人话**的系统活动，明确告诉 agent 与操作者
//      "这是平台出口故障、不是目标问题、也不该用 rotate_egress"，并给出已用/待恢复状态。

// 黑洞代理在工具输出里的三种写法都要认：
//   - 环境变量 / 命令里：http://127.0.0.1:9
//   - curl 报错：Failed to connect to 127.0.0.1 port 9 after 0 ms
//   - 其它工具：127.0.0.1:9
//
// 只按完整 URL 匹配会漏掉后两种（任务 25 实测：worker 卡了 26 分钟没被识别）。
const blockedProxyHostPort = "127.0.0.1:9"

// egressBlockedMarkers 是"平台出口是黑洞"的全部可识别写法。
var egressBlockedMarkers = []string{
	blockedProxyURL,      // http://127.0.0.1:9
	blockedProxyHostPort, // 127.0.0.1:9
	"127.0.0.1 port 9",   // curl: (7) Failed to connect to 127.0.0.1 port 9
}

const (
	// egressBlockedProbeInterval 是触发立即复查的最小间隔。健康检查本身每 30 秒一轮，
	// 这里只负责把"黑洞或活跃代理明显报错"的时刻提前到秒级，同时避免每个工具输出都打一次复查。
	egressBlockedProbeInterval = 5 * time.Second
	// egressActiveProbeInterval 是活跃代理突发拒绝连接/407 时触发被动复查的最小间隔。
	egressActiveProbeInterval = 3 * time.Second
	// egressBlockedNotifyInterval 是"出口阻断"提示写进时间线的最小间隔：同一次故障
	// 只该留一条，而不是每条失败命令一条。
	egressBlockedNotifyInterval = 2 * time.Minute
)

// egressRecovery 管一个任务里"出口阻断"的探测与留痕节奏。
type egressRecovery struct {
	mu          sync.Mutex
	lastProbe   time.Time
	lastNotify  time.Time
	blockedSeen bool
}

// observeEgressBlocked 在工具输出显示"当前出口是平台黑洞"或"当前活跃代理本身拒绝连接/认证失败"时调用。
//
// 判据刻意收得很窄：
//  1. 出口确为 blockedProxyURL（127.0.0.1:9）且工具输出出现黑洞特征；
//  2. 或出口为正常活跃代理时，工具输出直接命中当前活跃代理 host:port 的代理层故障（如 proxyconnect/407/连接拒绝）。
// 普通的远端目标连接超时/拒绝连接不会触发代理池重查（以免把目标侧不通误判成平台出口坏了）。
func (e *Engine) observeEgressBlocked(t *Task, r db.Activity) {
	if e == nil || t == nil || e.m == nil || r.Kind != "tool_result" {
		return
	}
	text := r.Detail
	if strings.TrimSpace(text) == "" {
		text = r.Summary
	}
	active := e.m.EgressProxy()
	if active == "" {
		return
	}
	if active != blockedProxyURL {
		if activeProxyFailureInText(active, text) {
			e.m.ObserveProxyFailure(active, fmt.Errorf("tool_result matched active proxy failure"))
		}
		return
	}
	hit := false
	for _, m := range egressBlockedMarkers {
		if strings.Contains(text, m) {
			hit = true
			break
		}
	}
	if !hit {
		return
	}
	rec := e.taskEgressRecovery(t.ID)
	now := time.Now()
	rec.mu.Lock()
	rec.blockedSeen = true // 记下"这次故障窗口里出现过阻断"，恢复时据此提示
	shouldProbe := now.Sub(rec.lastProbe) >= egressBlockedProbeInterval
	shouldNotify := now.Sub(rec.lastNotify) >= egressBlockedNotifyInterval
	if shouldProbe {
		rec.lastProbe = now
	}
	if shouldNotify {
		rec.lastNotify = now
	}
	rec.mu.Unlock()

	if shouldProbe {
		// 立刻复查一次：把"已经坏了"的时刻提前到秒级，而不是等下一个 30 秒 tick。
		e.m.RecheckProxyPool()
	}
	if shouldNotify {
		detail := "平台出口代理当前不可用（全部候选探测失败，出口被阻断为黑洞地址 " + blockedProxyURL + "）。\n" +
			"这**不是目标侧问题**，也不是 WAF 封禁：不要用 rotate_egress（它换的是 Clash 节点，代理池本身挂了换节点也没用），" +
			"也不要用 --noproxy / 直连去绕过（会暴露真实 IP，平台会拦截）。\n" +
			"平台已自动触发一次代理池复查；恢复后新的工具调用会自动拿到正确出口。在此期间：先把不依赖网络的活干完（读图、写回已有结论），" +
			"需要联网的步骤等一条系统活动提示「出口已恢复」后再继续。"
		log.Printf("[egress] task %s 出口代理阻断（%s），已触发复查", t.ID, blockedProxyURL)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
			Summary: "出口代理不可用（平台侧），已自动触发复查", Detail: detail})
	}
}

// taskEgressRecovery 取（或建）本任务的自愈节奏记录。
func (e *Engine) taskEgressRecovery(taskID string) *egressRecovery {
	if v, ok := e.egressRecovery.Load(taskID); ok {
		return v.(*egressRecovery)
	}
	v, _ := e.egressRecovery.LoadOrStore(taskID, &egressRecovery{})
	return v.(*egressRecovery)
}

// notifyEgressRestored 在出口从阻断恢复时写一条提示，让等待中的 worker 知道可以继续。
func (e *Engine) notifyEgressRestored(t *Task, active string) {
	if e == nil || t == nil {
		return
	}
	rec := e.taskEgressRecovery(t.ID)
	rec.mu.Lock()
	was := rec.blockedSeen
	rec.blockedSeen = false
	rec.mu.Unlock()
	if !was {
		return
	}
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
		Summary: "出口代理已恢复",
		Detail:  fmt.Sprintf("平台出口已恢复为 %s，需要联网的步骤可以继续。此前失败的请求请重试一次（不要改写成直连）。", active)})
}

// RecheckProxyPool 立刻跑一次代理池健康检查（不等下一个 tick）。
// 供"工具输出显示出口是黑洞"时调用；未配置代理池时是无害空操作。
func (m *Manager) RecheckProxyPool() {
	if m == nil {
		return
	}
	m.mu.RLock()
	pool := m.proxyPool
	m.mu.RUnlock()
	if pool == nil {
		return
	}
	pool.RequestCheck()
}

// EgressProxy 之外的辅助：供测试与调用方判断"当前是不是阻断态"。
func (m *Manager) EgressBlocked() bool {
	if m == nil {
		return false
	}
	return m.EgressProxy() == blockedProxyURL
}

// ObserveProxyFailure 在工具或 MITM 上游遇到代理层连接/鉴权报错时触发（带节流，绝不阻塞调用方）。
func (m *Manager) ObserveProxyFailure(proxyURL string, err error) {
	if m == nil || err == nil {
		return
	}
	active := strings.TrimSpace(m.EgressProxy())
	if active == "" || active == blockedProxyURL {
		return
	}
	if !sameProxyTarget(active, proxyURL) {
		return
	}
	now := time.Now()
	m.proxyFailMu.Lock()
	if now.Sub(m.lastProxyFailProbe) < egressActiveProbeInterval {
		m.proxyFailMu.Unlock()
		return
	}
	m.lastProxyFailProbe = now
	m.proxyFailMu.Unlock()
	log.Printf("[egress] 检测到当前活跃代理 %s 报错，提前触发健康复查：%s",
		traffic.RedactProxyURL(active), traffic.SanitizeProxyError(active, err))
	m.RecheckProxyPool()
}

func sameProxyTarget(a, b string) bool {
	if strings.TrimSpace(b) == "" {
		return false
	}
	ua, ea := url.Parse(strings.TrimSpace(a))
	ub, eb := url.Parse(strings.TrimSpace(b))
	if ea != nil || eb != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return strings.EqualFold(ua.Host, ub.Host)
}

func activeProxyFailureInText(active, text string) bool {
	if strings.TrimSpace(active) == "" || strings.TrimSpace(text) == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(active))
	if err != nil || u.Host == "" {
		return false
	}
	lower := strings.ToLower(text)
	hostPort := strings.ToLower(u.Host)
	curlHostPort := hostPort
	if h, p, serr := strings.Cut(hostPort, ":"); serr {
		curlHostPort = h + " port " + p
	}
	mentionsProxy := strings.Contains(lower, hostPort) || strings.Contains(lower, curlHostPort)
	if !mentionsProxy {
		return strings.Contains(lower, "proxyconnect") ||
			strings.Contains(lower, "proxy authentication required") ||
			strings.Contains(lower, "received http code 407 from proxy")
	}
	for _, m := range []string{"connection refused", "couldn't connect", "failed to connect", "connection reset", "407", "socks", "proxyconnect", "timed out", "reset by peer"} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// notifyEgressRestoredAll 在出口恢复时给所有在跑任务留一条提示（由 Manager 回调）。
func (e *Engine) notifyEgressRestoredAll(active string) {
	if e == nil || e.m == nil {
		return
	}
	for _, t := range e.m.List() {
		if t == nil || t.lifecycleSnapshot().Paused {
			continue
		}
		e.notifyEgressRestored(t, active)
	}
}
