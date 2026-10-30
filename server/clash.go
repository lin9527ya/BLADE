package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Clash 出口切换：目标 WAF 封的是**出口 IP**，代理端口本身仍然健康，现有的代理池
// 健康检查永远不会因此切换。要让 BLADE 换出口，必须调用 Clash 的 external-controller
// API，在一个 Selector 组里改选另一个节点。
//
// 安全约束（与代理池同一条纪律：绝不直连）：
//   - 只允许选择非 DIRECT/REJECT 的叶子节点；嵌套组会递归解析，解析到 DIRECT 就拒绝。
//   - 切换后必须经**当前生效代理**查询出口 IP：IP 必须变化且不等于本机网卡地址，
//     否则继续换下一个；全部失败就恢复原节点并报错，绝不退回直连。
//   - 自动检测只处理强 WAF 特征（Cloudflare/Incapsula/Akamai 等挑战页 + 403/429），
//     普通登录 403 不会触发。

const (
	settingClashAPIURL         = "clash_api_url"
	settingClashAPISecret      = "clash_api_secret"
	settingClashProxyGroup     = "clash_proxy_group"
	settingClashAutoRotate     = "clash_auto_rotate"
	settingClashRotateCooldown = "clash_rotate_cooldown_seconds"

	defaultClashRotateCooldown = 120 * time.Second
)

// ClashEgressConfig 是设置页保存的 Clash 控制器配置。
type ClashEgressConfig struct {
	APIURL   string
	Secret   string
	Group    string
	Auto     bool
	Cooldown time.Duration
}

// ClashRotation 是一次成功切换的结果。
type ClashRotation struct {
	Group    string `json:"group"`
	From     string `json:"from"`
	To       string `json:"to"`
	EgressIP string `json:"egress_ip"`
}

func (r ClashRotation) Summary() string {
	return fmt.Sprintf("Clash 出口已切换：%s → %s（组 %s，出口 IP %s）", r.From, r.To, r.Group, r.EgressIP)
}

// ClashEgressStatus 是设置页展示的运行时状态（不实时请求 Clash，避免拖慢 GET）。
type ClashEgressStatus struct {
	Configured  bool   `json:"configured"`
	Auto        bool   `json:"auto"`
	Group       string `json:"group,omitempty"`
	Node        string `json:"node,omitempty"`
	EgressIP    string `json:"egress_ip,omitempty"`
	RotatedAt   string `json:"rotated_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastReason  string `json:"last_reason,omitempty"`
	CooldownSec int    `json:"cooldown_seconds"`
}

type clashEgressState struct {
	mu       sync.Mutex
	group    string
	node     string
	egressIP string
	lastAt   time.Time
	lastErr  string
	reason   string
	rotating bool
}

// clashProbeEgressIP 是变量以便测试替换（真实实现只走代理，没有直连分支）。
var clashProbeEgressIP = probeEgressIP

// ClashEgressConfig reads the persisted Clash controller config.
func (m *Manager) ClashEgressConfig() ClashEgressConfig {
	c := ClashEgressConfig{Cooldown: defaultClashRotateCooldown}
	if m == nil || m.pg == nil {
		return c
	}
	get := func(k, def string) string {
		if v, ok, err := m.pg.GetSetting(k); err == nil && ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
		return def
	}
	c.APIURL = strings.TrimRight(get(settingClashAPIURL, ""), "/")
	c.Secret = get(settingClashAPISecret, "")
	c.Group = get(settingClashProxyGroup, "")
	c.Auto = m.pg.GetBool(settingClashAutoRotate, true)
	if v := get(settingClashRotateCooldown, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Cooldown = time.Duration(n) * time.Second
		}
	}
	if c.Cooldown > 10*time.Minute {
		c.Cooldown = 10 * time.Minute
	}
	return c
}

// ClashAutoRotateEnabled reports whether automatic WAF-driven rotation should run.
func (m *Manager) ClashAutoRotateEnabled() bool {
	c := m.ClashEgressConfig()
	return c.Auto && c.APIURL != ""
}

// ClashEgressStatus returns cached last-rotation info for the settings page.
func (m *Manager) ClashEgressStatus() ClashEgressStatus {
	cfg := m.ClashEgressConfig()
	st := ClashEgressStatus{Configured: cfg.APIURL != "", Auto: cfg.Auto, Group: cfg.Group, CooldownSec: int(cfg.Cooldown.Seconds())}
	if m == nil {
		return st
	}
	s := m.clashState()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.group != "" {
		st.Group = s.group
	}
	st.Node, st.EgressIP, st.LastError, st.LastReason = s.node, s.egressIP, s.lastErr, s.reason
	if !s.lastAt.IsZero() {
		st.RotatedAt = s.lastAt.Format(time.RFC3339)
	}
	return st
}

func (m *Manager) clashState() *clashEgressState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clash == nil {
		m.clash = &clashEgressState{}
	}
	return m.clash
}

// RotateEgress switches the configured Clash Selector group to the next safe node
// and verifies the egress IP actually changed. It never falls back to a direct
// connection: without a usable proxy to probe through, it refuses to rotate.
func (m *Manager) RotateEgress(reason string) (ClashRotation, error) {
	var out ClashRotation
	cfg := m.ClashEgressConfig()
	if cfg.APIURL == "" {
		return out, fmt.Errorf("未配置 Clash 外部控制器（设置 → 网络与代理 → Clash 出口切换）")
	}
	active := m.EgressProxy()
	if active == "" || active == blockedProxyURL {
		return out, fmt.Errorf("出口代理未配置或已阻断：无法验证新出口 IP，拒绝切换（绝不直连）")
	}
	s := m.clashState()
	s.mu.Lock()
	if s.rotating {
		s.mu.Unlock()
		return out, fmt.Errorf("已有一次出口切换正在进行")
	}
	if cfg.Cooldown > 0 && !s.lastAt.IsZero() {
		if remain := cfg.Cooldown - time.Since(s.lastAt); remain > 0 {
			s.mu.Unlock()
			return out, fmt.Errorf("出口刚切换过，请 %.0f 秒后再试", remain.Seconds()+0.5)
		}
	}
	s.rotating = true
	cachedGroup, cachedIP := s.group, s.egressIP
	s.mu.Unlock()
	success := false
	defer func() {
		s.mu.Lock()
		s.rotating = false
		if success {
			s.group, s.node, s.egressIP = out.Group, out.To, out.EgressIP
			s.lastAt = time.Now()
			s.lastErr = ""
			s.reason = strings.TrimSpace(reason)
		}
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := clashClient{apiURL: cfg.APIURL, secret: cfg.Secret}
	proxies, err := client.proxies(ctx)
	if err != nil {
		s.setErr(err)
		return out, err
	}
	groupName, group, err := clashPickSelector(proxies, cfg.Group)
	if err != nil {
		s.setErr(err)
		return out, err
	}
	out.Group, out.From = groupName, group.Now
	lastIP := ""
	if cachedGroup == groupName {
		// 同一组内比较旧出口 IP：即使 Clash 侧节点被别人改过，也要保证这次切换
		// 真的换了出口——否则“切成功但 IP 没变”就是假切换。
		lastIP = cachedIP
	}
	if !clashNodeSafe(group.Now, proxies, 0) {
		// 当前节点是 DIRECT/REJECT：先不要探测（那会把真实 IP 发给出口检测服务），
		// 直接换到一个安全节点，再验证新出口。
		lastIP = ""
	}
	candidates := clashCandidateNodes(group, proxies)
	if len(candidates) == 0 {
		err = fmt.Errorf("代理组 %q 没有可用的非 DIRECT 节点（现有：%s）", groupName, strings.Join(group.All, ", "))
		s.setErr(err)
		return out, err
	}
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	var lastErr error
	for _, node := range candidates {
		if err := client.selectNode(ctx, groupName, node); err != nil {
			lastErr = err
			continue
		}
		now, err := client.groupNow(ctx, groupName)
		if err != nil || now != node {
			if err == nil {
				err = fmt.Errorf("Clash 未接受节点 %q（当前仍是 %q）", node, now)
			}
			lastErr = err
			continue
		}
		ip, err := clashProbeEgressIP(ctx, active, m.EgressIPURL(), 8*time.Second)
		if err != nil {
			lastErr = fmt.Errorf("节点 %q 已选中但出口 IP 检测失败：%w", node, err)
			continue
		}
		if isLocalEgressIP(ip) {
			lastErr = fmt.Errorf("节点 %q 的出口 IP %s 与本机网卡地址相同（疑似未走代理），拒绝采用", node, ip)
			continue
		}
		if lastIP != "" && ip == lastIP {
			lastErr = fmt.Errorf("节点 %q 的出口 IP 仍为 %s（未变化），继续尝试下一个", node, ip)
			continue
		}
		out.To, out.EgressIP = node, ip
		success = true
		log.Printf("[clash] %s；原因：%s", out.Summary(), clipRunes(strings.TrimSpace(reason), 120))
		return out, nil
	}
	// 全部失败：尽力恢复原节点（仅当它是安全节点时），保持原来的出口。
	if clashNodeSafe(group.Now, proxies, 0) && group.Now != "" {
		_ = client.selectNode(ctx, groupName, group.Now)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可切换的安全节点")
	}
	s.setErr(lastErr)
	return out, lastErr
}

func (s *clashEgressState) setErr(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// isLocalEgressIP reports whether ip equals one of this host's interface addresses.
func isLocalEgressIP(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return true // 解析不了就不采信
	}
	for _, local := range localInterfaceIPs() {
		if local.Equal(ip) {
			return true
		}
	}
	return false
}

// --- Clash external-controller client ---

type clashClient struct {
	apiURL string
	secret string
}

type clashProxyEntry struct {
	Type    string   `json:"type"`
	Now     string   `json:"now"`
	All     []string `json:"all"`
	History []struct {
		Delay int `json:"delay"`
	} `json:"history"`
}

type clashProxiesResp struct {
	Proxies map[string]clashProxyEntry `json:"proxies"`
}

func (c clashClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.apiURL, "/")+path, rdr)
	if err != nil {
		return fmt.Errorf("Clash 控制器地址无效：%w", err)
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true} // 控制器通常在本机/局域网，绝不走目标代理
	cl := &http.Client{Transport: tr, Timeout: 15 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("连接 Clash 控制器失败：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len([]rune(msg)) > 160 {
			msg = string([]rune(msg)[:160]) + "…"
		}
		return fmt.Errorf("Clash 控制器 HTTP %d：%s", resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("Clash 控制器返回无法解析：%w", err)
		}
	}
	return nil
}

func (c clashClient) proxies(ctx context.Context) (map[string]clashProxyEntry, error) {
	var out clashProxiesResp
	if err := c.do(ctx, http.MethodGet, "/proxies", nil, &out); err != nil {
		return nil, err
	}
	return out.Proxies, nil
}

func (c clashClient) selectNode(ctx context.Context, group, node string) error {
	path := "/proxies/" + url.PathEscape(group)
	return c.do(ctx, http.MethodPut, path, map[string]string{"name": node}, nil)
}

func (c clashClient) groupNow(ctx context.Context, group string) (string, error) {
	proxies, err := c.proxies(ctx)
	if err != nil {
		return "", err
	}
	p, ok := proxies[group]
	if !ok {
		return "", fmt.Errorf("Clash 代理组 %q 不存在", group)
	}
	return p.Now, nil
}

// clashPickSelector selects the configured group (or the first deterministic
// Selector with more than one node when unset).
func clashPickSelector(proxies map[string]clashProxyEntry, want string) (string, clashProxyEntry, error) {
	if want != "" {
		p, ok := proxies[want]
		if !ok {
			return want, clashProxyEntry{}, fmt.Errorf("Clash 代理组 %q 不存在（可用 Selector：%s）", want, clashSelectorNames(proxies))
		}
		if !strings.EqualFold(p.Type, "Selector") {
			return want, clashProxyEntry{}, fmt.Errorf("Clash 组 %q 类型是 %s，不是 Selector：无法手动切换", want, p.Type)
		}
		return want, p, nil
	}
	names := strings.Split(clashSelectorNames(proxies), ", ")
	if len(names) == 0 || names[0] == "" {
		return "", clashProxyEntry{}, fmt.Errorf("Clash 里没有可用的 Selector 组，请在设置页指定组名")
	}
	p := proxies[names[0]]
	return names[0], p, nil
}

func clashSelectorNames(proxies map[string]clashProxyEntry) string {
	var names []string
	for name, p := range proxies {
		if strings.EqualFold(p.Type, "Selector") && len(p.All) > 1 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// clashCandidateNodes returns safe, non-current **leaf** nodes ordered by observed
// delay. Nested groups are skipped: selecting a group name hands control back to
// Clash (url-test/fallback inside it), and the egress IP may not change at all —
// the caller must be able to name and verify the exact exit node.
func clashCandidateNodes(group clashProxyEntry, proxies map[string]clashProxyEntry) []string {
	delay := map[string]int{}
	if p, ok := proxies[group.Now]; ok && len(p.History) > 0 {
		delay[group.Now] = p.History[len(p.History)-1].Delay
	}
	type cand struct {
		name  string
		delay int
	}
	var out []cand
	for _, name := range group.All {
		if name == group.Now || !clashNodeSafe(name, proxies, 0) {
			continue
		}
		if p, ok := proxies[name]; ok && clashGroupType(p.Type) {
			continue // 嵌套组不作为切换目标：出口由组内策略决定，无法保证换 IP
		}
		d := 1 << 30
		if p, ok := proxies[name]; ok && len(p.History) > 0 && p.History[len(p.History)-1].Delay > 0 {
			d = p.History[len(p.History)-1].Delay
		}
		out = append(out, cand{name, d})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].delay != out[j].delay {
			return out[i].delay < out[j].delay
		}
		return out[i].name < out[j].name
	})
	nodes := make([]string, 0, len(out))
	for _, c := range out {
		nodes = append(nodes, c.name)
	}
	return nodes
}

// clashNodeSafe rejects built-in DIRECT/REJECT leaves and resolves nested groups
// recursively. Unknown names are rejected (safer than guessing what they are).
func clashNodeSafe(name string, proxies map[string]clashProxyEntry, depth int) bool {
	if clashBlockedName(name) || depth > 4 {
		return false
	}
	p, ok := proxies[name]
	if !ok {
		return false
	}
	if clashGroupType(p.Type) {
		return p.Now != "" && clashNodeSafe(p.Now, proxies, depth+1)
	}
	return true
}

func clashGroupType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "selector", "urltest", "fallback", "loadbalance", "relay":
		return true
	}
	return false
}

func clashBlockedName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE":
		return true
	}
	return false
}
