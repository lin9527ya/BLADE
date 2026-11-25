package server

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 出口阻断时：立刻触发一次代理池复查，并留一条能让人/agent 看懂的系统活动。
func TestObserveEgressBlockedTriggersRecovery(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("egress blocked", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)

	// 让平台当前出口处于阻断态（等价于代理池全部候选探测失败）。
	// 注意：测试里 Manager 可能自带一个空候选的池，EgressProxy 会走 pool.Active()，
	// 所以这里直接把池置空、只留 egressProxy 兜底值。
	m.mu.Lock()
	m.proxyPool = nil
	m.egressProxy = blockedProxyURL
	m.mu.Unlock()

	r := db.Activity{Kind: "tool_result",
		Detail: "curl: (7) Failed to connect to 127.0.0.1 port 9 after 0 ms: Couldn't connect to server\nHTTP 000"}
	e.observeEgressBlocked(task, r)

	acts, _, err := task.Store.ActivityList(nil, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range acts {
		if !strings.Contains(a.Summary, "出口代理不可用") {
			continue
		}
		found = true
		// Detail 在列表里是懒加载的（json:"-"），要单独取。
		detail, derr := task.Store.ActivityDetail(a.ID)
		if derr != nil {
			t.Fatalf("读取活动详情: %v", derr)
		}
		if !strings.Contains(detail, "不是目标侧问题") || !strings.Contains(detail, "rotate_egress") {
			t.Fatalf("提示应说清不是目标问题、也不该用 rotate_egress：%s", detail)
		}
	}
	if !found {
		t.Fatalf("应写一条出口不可用的系统活动，实际 %+v", acts)
	}

	// 同一故障窗口内不重复刷屏。
	before := len(acts)
	e.observeEgressBlocked(task, r)
	after, _, _ := task.Store.ActivityList(nil, 0, 50)
	if len(after) != before {
		t.Fatalf("同一故障窗口不应重复提示：%d → %d", before, len(after))
	}
}

// 出口正常时，命令里残留的旧黑洞值（会话变量等）不该触发自愈——否则平台明明正常
// 也会被反复复查。
func TestObserveEgressBlockedIgnoresStaleValue(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("egress stale", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)
	m.mu.Lock()
	m.proxyPool = nil
	m.egressProxy = "http://192.168.10.137:7898" // 平台出口正常
	m.mu.Unlock()

	e.observeEgressBlocked(task, db.Activity{Kind: "tool_result",
		Detail: "http_proxy=http://127.0.0.1:9 (残留值)"})
	acts, _, _ := task.Store.ActivityList(nil, 0, 50)
	for _, a := range acts {
		if strings.Contains(a.Summary, "出口代理不可用") {
			t.Fatalf("平台出口正常时不该报出口不可用：%s", a.Detail)
		}
	}
}

// 恢复时给在跑任务留一条「可以继续了」。
func TestEgressRestoredNotifies(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("egress restored", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)
	// 必须先让平台出口处于阻断态，否则观察函数会按"平台正常"直接返回（设计如此：
	// 会话里残留的旧值不该触发自愈）。
	m.mu.Lock()
	m.proxyPool = nil
	m.egressProxy = blockedProxyURL
	m.mu.Unlock()
	e.observeEgressBlocked(task, db.Activity{Kind: "tool_result", Detail: "connect to 127.0.0.1:9 refused"})
	// 恢复回调
	m.mu.Lock()
	m.proxyPool = nil
	m.egressProxy = "http://192.168.10.137:7898"
	m.mu.Unlock()
	e.notifyEgressRestored(task, "http://192.168.10.137:7898")
	acts, _, _ := task.Store.ActivityList(nil, 0, 50)
	var found bool
	for _, a := range acts {
		if strings.Contains(a.Summary, "出口代理已恢复") {
			found = true
		}
	}
	if !found {
		t.Fatalf("恢复后应提示可以继续：%+v", acts)
	}
}

func TestObserveActiveProxyFailureTriggersRecheck(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("active proxy fail", "x", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(tid)
	e := NewEngine(m)

	var probes atomic.Int32
	cfg, _ := NormalizeProxyPoolConfig(ProxyPoolConfig{
		Candidates: []string{"http://127.0.0.1:7890"},
		Interval:   time.Hour,
	})
	pool := newProxyPool(cfg, func(context.Context, string, string, time.Duration) error {
		probes.Add(1)
		return nil
	}, nil)
	pool.Start()
	defer pool.Close()

	// Wait for initial probe so active becomes http://127.0.0.1:7890.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pool.Active() != "http://127.0.0.1:7890" {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Active() != "http://127.0.0.1:7890" {
		t.Fatalf("pool did not become ready: %q", pool.Active())
	}
	before := probes.Load()

	m.mu.Lock()
	oldPool := m.proxyPool
	m.proxyPool = pool
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.proxyPool = oldPool
		m.mu.Unlock()
	}()

	// Tool result hits connection refused specifically on active proxy 127.0.0.1:7890.
	e.observeEgressBlocked(task, db.Activity{
		Kind:   "tool_result",
		Detail: "curl: (7) Failed to connect to 127.0.0.1 port 7890 after 0 ms: Connection refused",
	})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && probes.Load() <= before {
		time.Sleep(10 * time.Millisecond)
	}
	if probes.Load() <= before {
		t.Fatalf("active proxy connection refused should trigger immediate recheck (before=%d after=%d)", before, probes.Load())
	}
}
