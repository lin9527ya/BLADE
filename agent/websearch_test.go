package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func braveHTMLFixture() string {
	return `<html><body><main>
<div class="snippet svelte" data-pos="0" data-type="web">
  <div class="result-body"><div class="result-content">
    <a href="https://www.nopcommerce.com/en/release-notes" class="svelte l1">
      <div class="title search-snippet-title">Release notes - nopCommerce</div>
    </a>
    <div class="generic-snippet"><div class="content">Release Notes - <strong>nopCommerce 5.00.0</strong> (October 27th, 2026)</div></div>
  </div></div>
</div>
<div class="snippet svelte" data-pos="1" data-type="web">
  <div class="result-body"><div class="result-content">
    <a href="https://github.com/nopSolutions/nopCommerce/releases" class="svelte l1">
      <div class="title search-snippet-title">Releases · nopSolutions/nopCommerce</div>
    </a>
    <div class="generic-snippet"><div class="content">Source releases</div></div>
  </div></div>
</div>
<div class="snippet nested"><a href="https://example.com/ignored">Nested</a></div>
</main></body></html>`
}

func TestParseBraveHTML(t *testing.T) {
	results, err := parseBraveHTML([]byte(braveHTMLFixture()), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(results), results)
	}
	if results[0].URL != "https://www.nopcommerce.com/en/release-notes" || !strings.Contains(results[0].Title, "nopCommerce") {
		t.Fatalf("unexpected first result: %+v", results[0])
	}
	if results[0].Position != 1 || results[1].Position != 2 {
		t.Fatalf("positions not set: %+v", results)
	}
}

func TestWebSearchBraveHTMLFallback(t *testing.T) {
	InvalidateWebSearchCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			t.Errorf("missing q")
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, braveHTMLFixture())
	}))
	defer srv.Close()
	old := braveHTMLEndpoint
	braveHTMLEndpoint = srv.URL
	defer func() { braveHTMLEndpoint = old }()

	// brave-free without a key fails immediately; Brave HTML must take over.
	results, actual, fallback, err := WebSearchProbe(WebSearchOpts{Enabled: true, Backend: "brave-free", BypassCache: true}, "nopCommerce", 2)
	if err != nil {
		t.Fatal(err)
	}
	if actual != "brave-html" || fallback != "brave-free" || len(results) != 2 {
		t.Fatalf("unexpected fallback: actual=%q fallback=%q results=%d", actual, fallback, len(results))
	}

	results, actual, _, err = WebSearchProbe(WebSearchOpts{Enabled: true, Backend: "brave-html", BypassCache: true}, "nopCommerce", 1)
	if err != nil || actual != "brave-html" || len(results) != 1 {
		t.Fatalf("explicit brave-html: actual=%q results=%d err=%v", actual, len(results), err)
	}
}

func TestNewWebSearchToolName(t *testing.T) {
	tool := NewWebSearchTool(WebSearchOpts{Enabled: true, Backend: "brave-html"})
	if tool.Name() != "web_search" {
		t.Fatalf("name=%q", tool.Name())
	}
	if !strings.Contains(tool.Description(), "Brave") {
		t.Fatalf("description does not mention fallback: %s", tool.Description())
	}
}

func TestWebSearchDynamicProxyAndFallbackProxyFunc(t *testing.T) {
	InvalidateWebSearchCache()
	var hitA, hitB int32
	proxyA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitA, 1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, braveHTMLFixture())
	}))
	defer proxyA.Close()
	proxyB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitB, 1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, braveHTMLFixture())
	}))
	defer proxyB.Close()

	oldBraveHTML := braveHTMLEndpoint
	braveHTMLEndpoint = "http://brave.invalid/search"
	defer func() { braveHTMLEndpoint = oldBraveHTML }()

	activeProxy := proxyA.URL
	opts := WebSearchOpts{
		Enabled: true,
		Backend: "brave-html",
		ProxyFunc: func() string {
			return activeProxy
		},
	}
	if _, _, _, err := WebSearchProbe(opts, "proxy-a", 1); err != nil {
		t.Fatalf("search 1 via proxyA: %v", err)
	}
	activeProxy = proxyB.URL
	if _, _, _, err := WebSearchProbe(opts, "proxy-b", 1); err != nil {
		t.Fatalf("search 2 via proxyB: %v", err)
	}
	if atomic.LoadInt32(&hitA) != 1 || atomic.LoadInt32(&hitB) != 1 {
		t.Fatalf("expected failover A=1 B=1, got A=%d B=%d", hitA, hitB)
	}

	// 非 DDG/Brave 主源失败后兜底走 brave-html，未填专用 Proxy 时必须走 FallbackProxyFunc 而非直连。
	oldTavily := tavilyEndpoint
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"tavily rate limit exceeded"}`)
	}))
	defer tavilySrv.Close()
	tavilyEndpoint = tavilySrv.URL
	defer func() { tavilyEndpoint = oldTavily }()

	optsFallback := WebSearchOpts{
		Enabled:           true,
		Backend:           "tavily",
		TavilyKey:         "tvly-test",
		FallbackProxyFunc: func() string { return proxyB.URL },
		BypassCache:       true,
	}
	out, err := WebSearchProbeDetailed(context.Background(), optsFallback, "fallback-proxy", 1)
	if err != nil {
		t.Fatalf("fallback probe failed: %v", err)
	}
	if out.Backend != "brave-html" || out.FallbackFrom != "tavily" || !strings.Contains(out.FallbackReason, "tavily rate limit exceeded") {
		t.Fatalf("unexpected fallback outcome: %+v", out)
	}
	if atomic.LoadInt32(&hitB) != 2 {
		t.Fatalf("expected brave-html fallback to use FallbackProxyFunc (hitB=2), got %d", hitB)
	}
}

func TestWebSearchInvalidProxyFailsClosed(t *testing.T) {
	InvalidateWebSearchCache()
	var reached int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reached, 1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, braveHTMLFixture())
	}))
	defer srv.Close()
	old := braveHTMLEndpoint
	braveHTMLEndpoint = srv.URL
	defer func() { braveHTMLEndpoint = old }()

	for _, bad := range []string{"://bad-proxy", "ftp://127.0.0.1:21", "127.0.0.1:7890"} {
		_, _, _, err := WebSearchProbe(WebSearchOpts{
			Enabled: true, Backend: "brave-html", Proxy: bad, BypassCache: true,
		}, "bad-proxy", 1)
		if err == nil {
			t.Fatalf("expected invalid proxy %q to fail closed, got nil error", bad)
		}
	}
	if atomic.LoadInt32(&reached) != 0 {
		t.Fatalf("invalid proxy must never leak direct connection, reached=%d", reached)
	}
}

func TestWebSearchStepTimeoutDoesNotStarveFallback(t *testing.T) {
	InvalidateWebSearchCache()
	oldPrimaryTimeout := webSearchPrimaryTimeout
	oldFallbackTimeout := webSearchFallbackTimeout
	oldTavily := tavilyEndpoint
	oldBraveHTML := braveHTMLEndpoint
	webSearchPrimaryTimeout = 40 * time.Millisecond
	webSearchFallbackTimeout = 500 * time.Millisecond
	defer func() {
		webSearchPrimaryTimeout = oldPrimaryTimeout
		webSearchFallbackTimeout = oldFallbackTimeout
		tavilyEndpoint = oldTavily
		braveHTMLEndpoint = oldBraveHTML
	}()

	slowPrimary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}))
	defer slowPrimary.Close()
	tavilyEndpoint = slowPrimary.URL

	fastFallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, braveHTMLFixture())
	}))
	defer fastFallback.Close()
	braveHTMLEndpoint = fastFallback.URL

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := WebSearchProbeDetailed(ctx, WebSearchOpts{
		Enabled: true, Backend: "tavily", TavilyKey: "tvly-test", BypassCache: true,
	}, "slow-primary", 2)
	if err != nil {
		t.Fatalf("expected fallback to succeed despite hung primary, got err=%v", err)
	}
	if out.Backend != "brave-html" || len(out.Results) != 2 {
		t.Fatalf("unexpected fallback result: %+v", out)
	}
}

func TestWebSearchSearXNGAndDedupCache(t *testing.T) {
	InvalidateWebSearchCache()
	ResetWebSearchStats()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			t.Fatalf("unexpected SearXNG request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[
			{"title":"CVE-2024-1","url":"https://nvd.nist.gov/vuln/detail/CVE-2024-1","content":"critical advisory"},
			{"title":"Dup","url":"https://nvd.nist.gov/vuln/detail/CVE-2024-1","content":"dup"},
			{"title":"Vendor","url":"https://vendor.example/sec","snippet":"patched in 2.0"}
		]}`)
	}))
	defer srv.Close()

	opts := WebSearchOpts{Enabled: true, Backend: "searxng", SearxngURL: srv.URL}
	out1, err := WebSearchProbeDetailed(context.Background(), opts, "CVE-2024-1", 5)
	if err != nil || out1.Backend != "searxng" || out1.FromCache || len(out1.Results) != 2 {
		t.Fatalf("call 1 unexpected: out=%+v err=%v", out1, err)
	}
	if out1.Results[1].Description != "patched in 2.0" {
		t.Fatalf("expected snippet fallback in SearXNG: %+v", out1.Results[1])
	}

	out2, err := WebSearchProbeDetailed(context.Background(), opts, "CVE-2024-1", 5)
	if err != nil || !out2.FromCache || len(out2.Results) != 2 {
		t.Fatalf("call 2 should hit cache: out=%+v err=%v", out2, err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected server hits=1 on cache hit, got %d", hits)
	}

	bypass := opts
	bypass.BypassCache = true
	out3, err := WebSearchProbeDetailed(context.Background(), bypass, "CVE-2024-1", 5)
	if err != nil || out3.FromCache || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("BypassCache must query live server: out=%+v hits=%d err=%v", out3, hits, err)
	}

	InvalidateWebSearchCache()
	if _, err := WebSearchProbeDetailed(context.Background(), opts, "CVE-2024-1", 5); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Fatalf("InvalidateWebSearchCache should clear cache (hits=3), got %d", hits)
	}
	st := GetWebSearchStats()
	if st.Requests != 4 || st.CacheHits != 1 || st.PrimaryOK != 3 || st.Failures != 0 {
		t.Fatalf("unexpected search stats: %+v", st)
	}
}

func TestWebSearchBraveHTMLChallengeAndFallbackSelector(t *testing.T) {
	InvalidateWebSearchCache()
	oldBraveHTML := braveHTMLEndpoint
	oldDDGS := ddgsEndpoint
	defer func() {
		braveHTMLEndpoint = oldBraveHTML
		ddgsEndpoint = oldDDGS
	}()

	challengeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><script src="/pow-captcha.js"></script>Checking your browser</body></html>`)
	}))
	defer challengeSrv.Close()
	ddgChallengeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>Unfortunately, bots use DuckDuckGo too <form id="challenge-form"></form></body></html>`)
	}))
	defer ddgChallengeSrv.Close()

	braveHTMLEndpoint = challengeSrv.URL
	ddgsEndpoint = ddgChallengeSrv.URL
	_, err := WebSearchProbeDetailed(context.Background(), WebSearchOpts{
		Enabled: true, Backend: "brave-html", BypassCache: true,
	}, "test", 2)
	if err == nil || !strings.Contains(err.Error(), "Brave") || !strings.Contains(err.Error(), "DuckDuckGo") {
		t.Fatalf("expected readable challenge error for both backends, got: %v", err)
	}

	// 无 data-type="web" 仅带外链和 snippet-title / snippet-description 的新版布局应可容错解析，且过滤 brave.com 内链。
	altHTML := `<html><body>
	<div class="snippet"><a href="https://search.brave.com/internal"><div class="snippet-title">Brave Nav</div></a></div>
	<div class="snippet"><a href="https://example.org/advisory"><div class="snippet-title">Alt Title</div></a><p class="snippet-description">Alt Desc</p></div>
	</body></html>`
	results, parseErr := parseBraveHTML([]byte(altHTML), 5)
	if parseErr != nil || len(results) != 1 || results[0].URL != "https://example.org/advisory" || results[0].Title != "Alt Title" || results[0].Description != "Alt Desc" {
		t.Fatalf("unexpected fallback selector parse: res=%+v err=%v", results, parseErr)
	}
}

func TestNewWebSearchToolWithResolver(t *testing.T) {
	InvalidateWebSearchCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"DynHit","url":"https://dyn.example/1","content":"ok"}]}`)
	}))
	defer srv.Close()

	current := WebSearchOpts{Enabled: true, Backend: "searxng", SearxngURL: srv.URL, BypassCache: true}
	tl := NewWebSearchToolWithResolver(func() WebSearchOpts { return current })
	res, err := tl.Call(context.Background(), json.RawMessage(`{"query":"dyn","limit":2}`), nil)
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), "DynHit") {
		t.Fatalf("expected live resolved tool call to succeed: res=%+v err=%v", res, err)
	}

	current.Enabled = false
	res2, err := tl.Call(context.Background(), json.RawMessage(`{"query":"dyn"}`), nil)
	if err != nil || !res2.IsError || !strings.Contains(res2.Flatten(), "disabled") {
		t.Fatalf("expected disabled resolver call to return error: res=%+v err=%v", res2, err)
	}
}
