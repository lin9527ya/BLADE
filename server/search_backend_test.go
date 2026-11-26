package server

import (
	"testing"

	"github.com/lin9527ya/blade/db"
)

func searchTestManager(t *testing.T) *Manager {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	return &Manager{pg: pg}
}

func TestIsOpencodeGateway(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"https://opencode.ai/zen/go", true},
		{"https://api.opencode.ai/v1", true},
		{"https://example.com/opencode.ai", false},
		{"http://127.0.0.1:3065", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isOpencodeGateway(c.base); got != c.want {
			t.Errorf("isOpencodeGateway(%q)=%v want %v", c.base, got, c.want)
		}
	}
}

func TestProfileForSearchPrefixMatch(t *testing.T) {
	m := searchTestManager(t)
	name := "opencode-prefix-" + t.Name()
	id, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: name + "-gs1", Format: "anthropic",
		BaseURL: "https://opencode.ai/zen/go", Model: "deepseek-v4.1-flash",
		APIKey: "test-key", SessionHeaderKey: "x-opencode-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id) })

	p, err := m.profileForSearch(name)
	if err != nil || p == nil || p.ID != id {
		t.Fatalf("前缀匹配失败: p=%+v err=%v", p, err)
	}
	if p.APIKey != "test-key" {
		t.Fatalf("前缀匹配必须取回完整配置（含 key），得到 %q", p.APIKey)
	}
	headers := profileSearchHeaders(p, name)
	if headers["x-opencode-session"] == "" {
		t.Fatalf("缺少会话头: %+v", headers)
	}
}

func TestSearchBackendFallbacks(t *testing.T) {
	m := searchTestManager(t)

	// commandcode 没有配置：不要静默摘掉工具，回退到可用的 DDG。
	if res := m.searchBackendForSDK(commandCodeWebSearchBackend); res.Backend != defaultWebSearchBackend || res.FallbackFrom != commandCodeWebSearchBackend {
		t.Fatalf("commandcode 缺失应回退 ddgs: %+v", res)
	}

	oldActive, _ := m.pg.ActiveProfile()
	id, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: "opencode", Format: "anthropic",
		BaseURL: "https://opencode.ai/zen/go", Model: "deepseek-v4.1-flash",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if oldActive != nil {
			_ = m.pg.SetActiveProfile(oldActive.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, id)
		}
		_ = m.pg.DeleteProfile(id)
	})

	// opencode Go 不执行服务端 web_search：backend=opencode 应回退 DDG。
	if res := m.searchBackendForSDK(opencodeWebSearchBackend); res.Backend != defaultWebSearchBackend || res.FallbackFrom != opencodeWebSearchBackend {
		t.Fatalf("opencode 网关应回退 ddgs: %+v", res)
	}

	// active 是 opencode 时选 deepseek，同样不能把 MissingSessionID/0 结果丢给用户。
	if err := m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	if res := m.searchBackendForSDK(deepSeekWebSearchBackend); res.Backend != defaultWebSearchBackend || res.FallbackFrom != deepSeekWebSearchBackend {
		t.Fatalf("deepseek + opencode 应回退 ddgs: %+v", res)
	}

	// 回退后 DDG 没填搜索代理时应走平台出口代理（直连 DDG 容易 202）。
	m.mu.Lock()
	m.webSearchOn = true
	m.webSearchBackend = opencodeWebSearchBackend
	m.webSearchProxy = ""
	m.egressProxy = "http://127.0.0.1:7898"
	m.proxyPool = nil
	m.mu.Unlock()
	o := m.WebSearchOpts()
	if o.Backend != defaultWebSearchBackend || o.Proxy != "http://127.0.0.1:7898" {
		t.Fatalf("回退后应使用出口代理: %+v", o)
	}
}

func TestSearchBackendCommandCodeUsesProfile(t *testing.T) {
	m := searchTestManager(t)
	id, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: "commandcode", Format: "anthropic",
		BaseURL: "https://api.commandcode.ai/provider/v1", Model: "claude-test",
		APIKey: "cc-test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id) })

	res := m.searchBackendForSDK(commandCodeWebSearchBackend)
	if res.Backend != commandCodeWebSearchBackend || res.CCBaseURL != "https://api.commandcode.ai/provider/v1" || res.CCAPIKey != "cc-test-key" {
		t.Fatalf("commandcode 配置未正确解析: %+v", res)
	}
}

func TestDeepSeekSearchCarriesSessionHeader(t *testing.T) {
	m := searchTestManager(t)
	oldActive, _ := m.pg.ActiveProfile()
	id, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: "deepseek-official-test", Format: "anthropic",
		BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-chat",
		APIKey: "ds-test-key", SessionHeaderKey: "x-opencode-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if oldActive != nil {
			_ = m.pg.SetActiveProfile(oldActive.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, id)
		}
		_ = m.pg.DeleteProfile(id)
	})
	if err := m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	res := m.searchBackendForSDK(deepSeekWebSearchBackend)
	if res.Backend != deepSeekWebSearchBackend {
		t.Fatalf("官方 DeepSeek 配置不应回退: %+v", res)
	}
	if res.Headers["x-opencode-session"] == "" {
		t.Fatalf("deepseek 搜索请求缺少会话头: %+v", res.Headers)
	}
}

func TestDeepSeekSearchPrefersNamedProfile(t *testing.T) {
	m := searchTestManager(t)
	oldActive, _ := m.pg.ActiveProfile()
	activeID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: "opencode-active-test", Format: "anthropic",
		BaseURL: "https://opencode.ai/zen/go", Model: "deepseek-v4.1-flash",
		APIKey: "active-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	searchID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: "Deepseek", Format: "anthropic",
		BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-flash",
		APIKey: "search-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if oldActive != nil {
			_ = m.pg.SetActiveProfile(oldActive.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id IN ($1,$2)`, activeID, searchID)
		}
		_ = m.pg.DeleteProfile(activeID)
		_ = m.pg.DeleteProfile(searchID)
	})
	if err := m.pg.SetActiveProfile(activeID); err != nil {
		t.Fatal(err)
	}

	base, key, model, _ := m.deepSeekSearchCreds()
	if base != "https://api.deepseek.com/anthropic" || key != "search-key" || model != "deepseek-flash" {
		t.Fatalf("deepseek 搜索应优先同名配置，得到 base=%q model=%q key=%q", base, model, key)
	}
	res := m.searchBackendForSDK(deepSeekWebSearchBackend)
	if res.Backend != deepSeekWebSearchBackend || res.FallbackFrom != "" || res.BaseURL != base || res.Model != model {
		t.Fatalf("deepseek 解析错误: %+v", res)
	}
}

func TestSearchBackendDeepSeekRejectsOpenAIFormat(t *testing.T) {
	m := searchTestManager(t)
	oldActive, _ := m.pg.ActiveProfile()
	id, err := m.pg.SaveProfile(&db.LLMProfile{
		Name:    "deepseek-openai-test",
		Format:  "openai-responses",
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-reasoner",
		APIKey:  "ds-test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if oldActive != nil {
			_ = m.pg.SetActiveProfile(oldActive.ID)
		} else {
			_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id=$1`, id)
		}
		_ = m.pg.DeleteProfile(id)
	})
	if err := m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}

	res := m.searchBackendForSDK(deepSeekWebSearchBackend)
	if res.Backend != defaultWebSearchBackend || res.FallbackFrom != deepSeekWebSearchBackend {
		t.Fatalf("expected fallback to ddgs on openai-responses format, got %+v", res)
	}
	if res.DiagnosticError == "" || res.FallbackReason == "" {
		t.Fatalf("expected explicit DiagnosticError for openai-responses DeepSeek profile, got %+v", res)
	}
}

func TestManagerWebSearchOptsDynamicProxyAndFallbackFunc(t *testing.T) {
	m := &Manager{
		webSearchOn:      true,
		webSearchBackend: "tavily",
		tavilyKey:        "tvly-test",
		egressProxy:      "http://127.0.0.1:7890",
	}
	opts := m.WebSearchOpts()
	if opts.Proxy != "" {
		t.Fatalf("expected static Proxy to remain empty when webSearchProxy unset so FallbackProxyFunc runs, got %q", opts.Proxy)
	}
	if opts.ProxyFunc != nil {
		t.Fatalf("expected tavily primary ProxyFunc nil under 1.A, got non-nil")
	}
	if opts.FallbackProxyFunc == nil {
		t.Fatal("expected FallbackProxyFunc to be non-nil")
	}
	if got := opts.FallbackProxyFunc(); got != "http://127.0.0.1:7890" {
		t.Fatalf("expected FallbackProxyFunc to track manager egress proxy, got %q", got)
	}

	m.mu.Lock()
	m.egressProxy = "http://127.0.0.1:7891"
	m.mu.Unlock()
	if got := opts.FallbackProxyFunc(); got != "http://127.0.0.1:7891" {
		t.Fatalf("FallbackProxyFunc must reflect updated egress proxy live, got %q", got)
	}

	m.mu.Lock()
	m.webSearchBackend = "ddgs"
	m.mu.Unlock()
	ddgOpts := m.WebSearchOpts()
	if ddgOpts.Proxy != "" || ddgOpts.ProxyFunc == nil || ddgOpts.ProxyFunc() != "http://127.0.0.1:7891" {
		t.Fatalf("ddgs must resolve egress dynamically via ProxyFunc without static Proxy shadow: %+v", ddgOpts)
	}
}

func TestNormalizeSearxngURLAndProxyMode(t *testing.T) {
	if got, err := normalizeSearxngURL("http://127.0.0.1:8888/"); err != nil || got != "http://127.0.0.1:8888" {
		t.Fatalf("unexpected normalizeSearxngURL: got=%q err=%v", got, err)
	}
	if _, err := normalizeSearxngURL("ftp://127.0.0.1:8888"); err == nil {
		t.Fatal("expected ftp scheme to be rejected")
	}
	m := &Manager{egressProxy: "http://127.0.0.1:7890"}
	if got := m.proxyModeForSearchProxy("socks5://127.0.0.1:1080"); got != "custom" {
		t.Fatalf("proxyMode(custom)=%q", got)
	}
	if got := m.proxyModeForSearchProxy(""); got != "egress_pool" {
		t.Fatalf("proxyMode(egress_pool)=%q", got)
	}
	m.egressProxy = blockedProxyURL
	if got := m.proxyModeForSearchProxy(""); got != "proxy_blocked" {
		t.Fatalf("proxyMode(proxy_blocked)=%q", got)
	}
	m.egressProxy = ""
	if got := m.proxyModeForSearchProxy(""); got != "direct" {
		t.Fatalf("proxyMode(direct)=%q", got)
	}
}
