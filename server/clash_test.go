package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeClash(t *testing.T) (*httptest.Server, *sync.Mutex, map[string]string) {
	t.Helper()
	var mu sync.Mutex
	now := map[string]string{"BLADE": "A", "ALT": "A"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/proxies" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"proxies": map[string]any{
				"BLADE":  map[string]any{"type": "Selector", "now": now["BLADE"], "all": []string{"A", "B", "DIRECT", "C"}},
				"ALT":    map[string]any{"type": "Selector", "now": now["ALT"], "all": []string{"A", "B", "DIRECT", "C"}},
				"A":      map[string]any{"type": "Vmess", "history": []map[string]int{{"delay": 120}}},
				"B":      map[string]any{"type": "Vmess", "history": []map[string]int{{"delay": 30}}},
				"C":      map[string]any{"type": "Trojan"},
				"DIRECT": map[string]any{"type": "Direct"},
			}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/proxies/") && r.Method == http.MethodPut {
			group := strings.TrimPrefix(r.URL.Path, "/proxies/")
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name == "DIRECT" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("cannot select direct"))
				return
			}
			now[group] = body.Name
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &mu, now
}

func TestClashCandidateSafety(t *testing.T) {
	proxies := map[string]clashProxyEntry{
		"BLADE": {Type: "Selector", Now: "A", All: []string{"A", "B", "DIRECT", "C", "NESTED"}},
		"A": {Type: "Vmess", History: []struct {
			Delay int `json:"delay"`
		}{{Delay: 100}}},
		"B": {Type: "Vmess", History: []struct {
			Delay int `json:"delay"`
		}{{Delay: 20}}},
		"C":        {Type: "Trojan"},
		"DIRECT":   {Type: "Direct"},
		"NESTED":   {Type: "Selector", Now: "DIRECT", All: []string{"DIRECT"}},
		"NESTEDOK": {Type: "Selector", Now: "B", All: []string{"B"}},
	}
	proxies["BLADE"] = clashProxyEntry{Type: "Selector", Now: "A", All: []string{"A", "B", "DIRECT", "C", "NESTED", "NESTEDOK"}}
	names := clashCandidateNodes(proxies["BLADE"], proxies)
	for _, n := range names {
		if n == "DIRECT" || n == "NESTED" || n == "NESTEDOK" {
			t.Fatalf("DIRECT/嵌套组进入了候选：%v", names)
		}
	}
	// 当前节点会保留在列表里用于定位轮转起点，随后由调用方剔除。
	if len(names) != 3 || names[0] != "B" || names[1] != "A" || names[2] != "C" {
		t.Fatalf("候选应按延迟排序、排除不安全节点并保留当前节点：%v", names)
	}
	if clashNodeSafe("NESTED", proxies, 0) {
		t.Fatal("嵌套组最终解析到 DIRECT 应判不安全")
	}
}

func TestRotateEgressSwitchesNodeAndVerifiesIP(t *testing.T) {
	s, _, now := fakeClash(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = m.Close() })
	_ = m.pg.SetSetting(settingClashAPIURL, s.URL)
	_ = m.pg.SetSetting(settingClashProxyGroup, "BLADE")
	_ = m.pg.SetSetting(settingClashRotateCooldown, "0")
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2,$3)`, settingClashAPIURL, settingClashProxyGroup, settingClashRotateCooldown)
	})
	m.mu.Lock()
	m.proxyPool = nil // 用固定的测试出口，不受共享测试库里的代理池配置影响
	m.egressProxy = "http://127.0.0.1:7898"
	m.mu.Unlock()

	oldProbe := clashProbeEgressIP
	t.Cleanup(func() { clashProbeEgressIP = oldProbe })
	ip := "203.0.113.9"
	clashProbeEgressIP = func(context.Context, string, string, time.Duration) (string, error) { return ip, nil }

	res, err := m.RotateEgress(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	// 按组内顺序从当前 A 之后轮转 → B（延迟排序不再跨区域打乱顺序）。
	if res.From != "A" || res.To != "B" || res.EgressIP != ip {
		t.Fatalf("rotation=%+v, want A→B ip=%s", res, ip)
	}
	if now["BLADE"] != "B" {
		t.Fatalf("clash group now=%q, want B", now["BLADE"])
	}
	// 第二次：即使 Clash 侧节点被别人改成 A（与缓存节点不同），出口 IP 没变化
	// 也必须失败——“切成功但 IP 没变”是假切换。
	now["BLADE"] = "A"
	ip = "203.0.113.9"
	if _, err := m.RotateEgress(context.Background(), "same ip"); err == nil || !strings.Contains(err.Error(), "未变化") {
		t.Fatalf("出口 IP 未变化时必须报错，got %v", err)
	}
}

func TestTaskEgressGroupsAreIsolated(t *testing.T) {
	s, _, now := fakeClash(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_ = m.pg.SetSetting(settingClashAPIURL, s.URL)
	_ = m.pg.SetSetting(settingClashProxyGroup, "BLADE")
	_ = m.pg.SetSetting(settingClashProxyGroups, "BLADE\nALT")
	_ = m.pg.SetSetting(settingClashTaskEndpoints, "BLADE=http://127.0.0.1:7801\nALT=http://127.0.0.1:7802")
	_ = m.pg.SetSetting(settingClashRotateCooldown, "0")
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2,$3,$4,$5)`,
			settingClashAPIURL, settingClashProxyGroup, settingClashProxyGroups, settingClashTaskEndpoints, settingClashRotateCooldown)
	})
	m.mu.Lock()
	m.proxyPool = nil
	m.egressProxy = "http://127.0.0.1:7898"
	m.mu.Unlock()

	oldProbe := clashProbeEgressIP
	t.Cleanup(func() { clashProbeEgressIP = oldProbe })
	clashProbeEgressIP = func(context.Context, string, string, time.Duration) (string, error) {
		return "203.0.113.9", nil
	}

	if got := m.TaskEgressGroup("task-1"); got != "BLADE" {
		t.Fatalf("task-1 group=%q, want BLADE", got)
	}
	if got := m.TaskEgressGroup("task-2"); got != "ALT" {
		t.Fatalf("task-2 should get the least-loaded group ALT, got %q", got)
	}
	if got := m.TaskEgressProxy("task-1"); got != "http://127.0.0.1:7801" {
		t.Fatalf("task-1 endpoint=%q, want its dedicated BLADE listener", got)
	}
	if got := m.TaskEgressProxy("task-2"); got != "http://127.0.0.1:7802" {
		t.Fatalf("task-2 endpoint=%q, want its dedicated ALT listener", got)
	}
	res, err := m.RotateEgressForTask(context.Background(), "task-1", "isolation test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Group != "BLADE" {
		t.Fatalf("task-1 rotated group=%q, want BLADE", res.Group)
	}
	if now["ALT"] != "A" {
		t.Fatalf("rotating task-1 must not move task-2's ALT group, got %q", now["ALT"])
	}
	if now["BLADE"] == "A" {
		t.Fatalf("task-1's BLADE group did not rotate: %q", now["BLADE"])
	}
}

func TestRotateEgressRefusesWithoutProxy(t *testing.T) {
	s, _, _ := fakeClash(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = m.Close() })
	_ = m.pg.SetSetting(settingClashAPIURL, s.URL)
	_ = m.pg.SetSetting(settingClashProxyGroup, "BLADE")
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, settingClashAPIURL, settingClashProxyGroup)
	})
	m.mu.Lock()
	m.egressProxy = ""
	m.proxyPool = nil
	m.globalProxy = ""
	m.mu.Unlock()
	if _, err := m.RotateEgress(context.Background(), "test"); err == nil || !strings.Contains(err.Error(), "拒绝切换") {
		t.Fatalf("没有可用代理时必须拒绝切换，got %v", err)
	}
}

func TestDetectWAFBlock(t *testing.T) {
	hit := []string{
		`<title>Just a moment...</title> HTTP/1.1 403 Forbidden https://target.example/login`,
		`HTTP/2 429 Too Many Requests` + "\n" + `Server: cloudflare`,
		`Incapsula incident ID: 123-456 403 https://target.example/`,
		`Request unsuccessful. Incapsula incident ID 403`,
		`Reference #18.abc 403 Access Denied https://target.example/`,
	}
	for _, s := range hit {
		if _, ok := detectWAFBlock(s); !ok {
			t.Fatalf("应识别为 WAF 拦截：%q", s)
		}
	}
	miss := []string{
		`HTTP/1.1 403 Forbidden` + "\n" + `{"error":"login required"}`,
		`HTTP/1.1 200 OK` + "\n" + `cloudflare analytics script`,
		`HTTP/1.1 404 Not Found https://target.example/nope`,
		`正常业务返回：用户名或密码错误`,
	}
	for _, s := range miss {
		if _, ok := detectWAFBlock(s); ok {
			t.Fatalf("不应识别为 WAF 拦截：%q", s)
		}
	}
}

// 每次切换必须沿组内顺序推进，而不是从头重选导致在两个节点之间来回跳。
func TestRotateEgressAdvancesThroughCandidates(t *testing.T) {
	var mu sync.Mutex
	now := map[string]string{"G": "A"}
	proxies := map[string]any{
		"G": map[string]any{"type": "Selector", "now": "A", "all": []string{"A", "B", "C", "D"}},
		"A": map[string]any{"type": "Vmess"}, "B": map[string]any{"type": "Vmess"},
		"C": map[string]any{"type": "Vmess"}, "D": map[string]any{"type": "Vmess"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/proxies" {
			// 每次返回当前 now，模拟 Clash 的实际状态
			_ = now
			proxies["G"] = map[string]any{"type": "Selector", "now": now["G"], "all": []string{"A", "B", "C", "D"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"proxies": proxies})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/proxies/") {
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			now["G"] = body.Name
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// LIFO：t.Cleanup 在本函数的 defer **之后**执行（Go 的测试清理顺序），所以关库
	// 只能注册成 Cleanup——写 defer 会让它先于下面的数据清理跑，清理语句全部落到
	// 已关闭的连接池上、静默失败，残留行污染共享测试库。
	t.Cleanup(func() { _ = m.Close() })
	_ = m.pg.SetSetting(settingClashAPIURL, srv.URL)
	_ = m.pg.SetSetting(settingClashProxyGroup, "G")
	_ = m.pg.SetSetting(settingClashRotateCooldown, "0")
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2,$3)`, settingClashAPIURL, settingClashProxyGroup, settingClashRotateCooldown)
	})
	m.mu.Lock()
	m.proxyPool = nil
	m.egressProxy = "http://127.0.0.1:7898"
	m.mu.Unlock()
	oldProbe := clashProbeEgressIP
	t.Cleanup(func() { clashProbeEgressIP = oldProbe })
	ipOf := map[string]string{"A": "198.51.100.1", "B": "198.51.100.2", "C": "198.51.100.3", "D": "198.51.100.4"}
	clashProbeEgressIP = func(context.Context, string, string, time.Duration) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return ipOf[now["G"]], nil
	}

	var got []string
	for i := 0; i < 3; i++ {
		res, err := m.RotateEgress(context.Background(), "advance")
		if err != nil {
			t.Fatalf("第 %d 次切换: %v", i+1, err)
		}
		got = append(got, res.To)
	}
	want := []string{"B", "C", "D"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("切换序列=%v, want %v（不应在两个节点间来回跳）", got, want)
		}
	}
}

func TestClashRegionPlansCapThreePerRegion(t *testing.T) {
	all := []string{
		"🇭🇰 H1", "🇭🇰 H2", "🇭🇰 H3", "🇭🇰 H4", "🇭🇰 H5",
		"🇹🇼 T1", "🇹🇼 T2", "🇹🇼 T3", "🇹🇼 T4",
		"🇯🇵 J1", "🇯🇵 J2", "🇯🇵 J3",
	}
	proxies := map[string]clashProxyEntry{}
	for _, name := range all {
		proxies[name] = clashProxyEntry{Type: "Vmess"}
	}
	group := clashProxyEntry{Type: "Selector", Now: "🇭🇰 H1", All: all}
	plans := clashRegionPlans(group, proxies, nil)
	if len(plans) < 2 || plans[0].Region != "香港" || len(plans[0].Nodes) != 3 {
		t.Fatalf("first region should be 香港 with 3 attempts, got %+v", plans)
	}
	if plans[0].Nodes[0] != "🇭🇰 H2" || plans[0].Nodes[2] != "🇭🇰 H4" {
		t.Fatalf("香港 should advance after current node and cap at 3: %+v", plans[0].Nodes)
	}
	if plans[1].Region != "台湾" || len(plans[1].Nodes) != 3 {
		t.Fatalf("second region should be 台湾 with 3 attempts, got %+v", plans[1])
	}
	// 默认组回退时跳过已经试过的地区。
	plans = clashRegionPlans(group, proxies, map[string]bool{"香港": true, "台湾": true})
	if len(plans) == 0 || plans[0].Region != "日本" {
		t.Fatalf("avoiding 香港/台湾 should start at 日本, got %+v", plans)
	}
}

func TestRotateEgressForTaskFallsBackToDefaultAndSkipsTriedRegions(t *testing.T) {
	var mu sync.Mutex
	now := map[string]string{"ASIA": "🇭🇰 H1", "BLADE": "🇬🇧 G1"}
	proxies := map[string]any{
		"ASIA":  map[string]any{"type": "Selector", "now": now["ASIA"], "all": []string{"🇭🇰 H1", "🇭🇰 H2", "🇭🇰 H3", "🇹🇼 T1", "🇹🇼 T2"}},
		"BLADE": map[string]any{"type": "Selector", "now": now["BLADE"], "all": []string{"🇬🇧 G1", "🇺🇸 U1"}},
		"🇭🇰 H1": map[string]any{"type": "Vmess"}, "🇭🇰 H2": map[string]any{"type": "Vmess"},
		"🇭🇰 H3": map[string]any{"type": "Vmess"}, "🇹🇼 T1": map[string]any{"type": "Vmess"},
		"🇹🇼 T2": map[string]any{"type": "Vmess"}, "🇬🇧 G1": map[string]any{"type": "Vmess"},
		"🇺🇸 U1": map[string]any{"type": "Vmess"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/proxies" {
			proxies["ASIA"].(map[string]any)["now"] = now["ASIA"]
			proxies["BLADE"].(map[string]any)["now"] = now["BLADE"]
			_ = json.NewEncoder(w).Encode(map[string]any{"proxies": proxies})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/proxies/") {
			group := strings.TrimPrefix(r.URL.Path, "/proxies/")
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			now[group] = body.Name
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_ = m.pg.SetSetting(settingClashAPIURL, srv.URL)
	_ = m.pg.SetSetting(settingClashProxyGroup, "BLADE")
	_ = m.pg.SetSetting(settingClashProxyGroups, "ASIA")
	_ = m.pg.SetSetting(settingClashTaskEndpoints, "ASIA=http://asia.local\nBLADE=http://default.local")
	_ = m.pg.SetSetting(settingClashRotateCooldown, "0")
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2,$3,$4,$5)`,
			settingClashAPIURL, settingClashProxyGroup, settingClashProxyGroups, settingClashTaskEndpoints, settingClashRotateCooldown)
	})
	m.mu.Lock()
	m.proxyPool = nil
	m.egressProxy = "http://127.0.0.1:7898"
	m.mu.Unlock()

	oldProbe := clashProbeEgressIP
	t.Cleanup(func() { clashProbeEgressIP = oldProbe })
	clashProbeEgressIP = func(_ context.Context, proxyURL, _ string, _ time.Duration) (string, error) {
		if proxyURL == "http://asia.local" {
			return "", fmt.Errorf("asia nodes blocked")
		}
		return "198.51.100.44", nil
	}

	if got := m.TaskEgressGroup("task-1"); got != "ASIA" {
		t.Fatalf("task group=%q, want ASIA", got)
	}
	res, err := m.RotateEgressForTask(context.Background(), "task-1", "fallback test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Group != "BLADE" || res.To != "🇺🇸 U1" {
		t.Fatalf("fallback should skip tried HK/TW and use US: %+v", res)
	}
	if got := m.TaskEgressGroup("task-1"); got != "BLADE" {
		t.Fatalf("task should stay on default fallback group, got %q", got)
	}
}
