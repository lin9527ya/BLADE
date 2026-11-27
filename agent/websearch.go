package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"golang.org/x/net/html"
)

var (
	braveHTMLEndpoint        = "https://search.brave.com/search"
	braveAPIEndpoint         = "https://api.search.brave.com/res/v1/web/search"
	tavilyEndpoint           = "https://api.tavily.com/search"
	ddgsEndpoint             = "https://html.duckduckgo.com/html/"
	webSearchPrimaryTimeout  = 20 * time.Second
	webSearchDeepSeekTimeout = 60 * time.Second
	webSearchFallbackTimeout = 15 * time.Second
)

const (
	webSearchMaxResponseBytes = 2 << 20
	webSearchHTMLMaxBytes     = 4 << 20
	webSearchCacheTTL         = 3 * time.Minute
	webSearchCacheMaxEntries  = 128
	webSearchDefaultBrowserUA = "Mozilla/5.0 (X11; Linux x86_64; rv:152.0) Gecko/20100101 Firefox/152.0"
)

// WebSearchOutcome carries rich result + diagnostic metadata from a search probe.
type WebSearchOutcome struct {
	Results        []actool.SearchResult
	Backend        string
	FallbackFrom   string
	FallbackReason string
	FromCache      bool
}

// WebSearchStatsSnapshot tracks cumulative runtime search execution metrics.
type WebSearchStatsSnapshot struct {
	Requests         int64  `json:"requests"`
	CacheHits        int64  `json:"cache_hits"`
	PrimaryOK        int64  `json:"primary_ok"`
	FallbackOK       int64  `json:"fallback_ok"`
	Failures         int64  `json:"failures"`
	LastBackend      string `json:"last_backend,omitempty"`
	LastFallbackFrom string `json:"last_fallback_from,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	LastAt           string `json:"last_at,omitempty"`
}

func resolveWebSearchOpts(static WebSearchOpts, fn func() WebSearchOpts) WebSearchOpts {
	if fn != nil {
		return fn()
	}
	return static
}

type searchCacheEntry struct {
	outcome   WebSearchOutcome
	expiresAt time.Time
}

var (
	searchCacheMu      sync.Mutex
	searchCacheEntries = map[string]searchCacheEntry{}
	searchCacheOrder   []string

	searchStatsMu sync.Mutex
	searchStats   WebSearchStatsSnapshot
)

// InvalidateWebSearchCache clears all cached web_search results (called when
// settings, keys, backends, or search proxies are modified).
func InvalidateWebSearchCache() {
	searchCacheMu.Lock()
	searchCacheEntries = map[string]searchCacheEntry{}
	searchCacheOrder = nil
	searchCacheMu.Unlock()
}

// ResetWebSearchStats resets cumulative search counters (for unit tests).
func ResetWebSearchStats() {
	searchStatsMu.Lock()
	searchStats = WebSearchStatsSnapshot{}
	searchStatsMu.Unlock()
}

// GetWebSearchStats returns a point-in-time snapshot of runtime web search telemetry.
func GetWebSearchStats() WebSearchStatsSnapshot {
	searchStatsMu.Lock()
	defer searchStatsMu.Unlock()
	return searchStats
}

func cloneSearchResults(in []actool.SearchResult) []actool.SearchResult {
	if len(in) == 0 {
		return nil
	}
	out := make([]actool.SearchResult, len(in))
	copy(out, in)
	return out
}

func recordSearchHit(backend, fallbackFrom string) {
	searchStatsMu.Lock()
	searchStats.Requests++
	searchStats.CacheHits++
	searchStats.LastBackend = backend
	searchStats.LastFallbackFrom = fallbackFrom
	searchStats.LastError = ""
	searchStats.LastAt = time.Now().UTC().Format(time.RFC3339)
	searchStatsMu.Unlock()
}

func recordSearchResult(backend, fallbackFrom string, err error) {
	searchStatsMu.Lock()
	searchStats.Requests++
	searchStats.LastAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		searchStats.Failures++
		searchStats.LastBackend = backend
		searchStats.LastFallbackFrom = fallbackFrom
		searchStats.LastError = err.Error()
	} else {
		searchStats.LastBackend = backend
		searchStats.LastFallbackFrom = fallbackFrom
		searchStats.LastError = ""
		if fallbackFrom != "" && fallbackFrom != backend {
			searchStats.FallbackOK++
		} else {
			searchStats.PrimaryOK++
		}
	}
	searchStatsMu.Unlock()
}

func getSearchCache(key string, now time.Time) (WebSearchOutcome, bool) {
	searchCacheMu.Lock()
	defer searchCacheMu.Unlock()
	e, ok := searchCacheEntries[key]
	if !ok {
		return WebSearchOutcome{}, false
	}
	if !now.Before(e.expiresAt) {
		delete(searchCacheEntries, key)
		return WebSearchOutcome{}, false
	}
	out := e.outcome
	out.Results = cloneSearchResults(out.Results)
	out.FromCache = true
	return out, true
}

func putSearchCache(key string, outcome WebSearchOutcome, now time.Time) {
	if len(outcome.Results) == 0 {
		return
	}
	stored := outcome
	stored.Results = cloneSearchResults(outcome.Results)
	stored.FromCache = false

	searchCacheMu.Lock()
	defer searchCacheMu.Unlock()
	if _, exists := searchCacheEntries[key]; !exists {
		for len(searchCacheOrder) >= webSearchCacheMaxEntries {
			oldest := searchCacheOrder[0]
			searchCacheOrder = searchCacheOrder[1:]
			delete(searchCacheEntries, oldest)
		}
		searchCacheOrder = append(searchCacheOrder, key)
	}
	searchCacheEntries[key] = searchCacheEntry{
		outcome:   stored,
		expiresAt: now.Add(webSearchCacheTTL),
	}
}

// WebSearchProbe runs a search through the configured primary backend and, when
// it is rate-limited or unavailable, falls back to Brave's public HTML results
// (no key) and then DuckDuckGo. It returns the actual backend and the original
// backend when a fallback ran.
func WebSearchProbe(o WebSearchOpts, query string, limit int) ([]actool.SearchResult, string, string, error) {
	return WebSearchProbeContext(context.Background(), o, query, limit)
}

// WebSearchProbeContext is the request-scoped variant preserving legacy callers.
func WebSearchProbeContext(ctx context.Context, o WebSearchOpts, query string, limit int) ([]actool.SearchResult, string, string, error) {
	res, err := WebSearchProbeDetailed(ctx, o, query, limit)
	return res.Results, res.Backend, res.FallbackFrom, err
}

// WebSearchProbeDetailed returns results together with fallback diagnostics and cache state.
func WebSearchProbeDetailed(ctx context.Context, o WebSearchOpts, query string, limit int) (WebSearchOutcome, error) {
	return o.searchDetailed(ctx, query, limit)
}

func (o WebSearchOpts) effectiveProxy(forFallback bool) string {
	if p := strings.TrimSpace(o.Proxy); p != "" {
		return p
	}
	if forFallback && o.FallbackProxyFunc != nil {
		return strings.TrimSpace(o.FallbackProxyFunc())
	}
	if o.ProxyFunc != nil {
		return strings.TrimSpace(o.ProxyFunc())
	}
	return ""
}

func (o WebSearchOpts) cacheKey(primary, primaryProxy, fallbackProxy, query string, limit int) string {
	return strings.Join([]string{
		primary,
		primaryProxy,
		fallbackProxy,
		strings.TrimSpace(o.SearxngURL),
		strings.TrimSpace(o.DeepSeekBaseURL),
		strings.TrimSpace(o.CommandCodeBaseURL),
		fmt.Sprintf("%d", limit),
		strings.ToLower(strings.TrimSpace(query)),
	}, "|")
}

func withStepTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func (o WebSearchOpts) searchDetailed(ctx context.Context, query string, limit int) (WebSearchOutcome, error) {
	primary := strings.ToLower(strings.TrimSpace(o.Backend))
	if primary == "" {
		primary = "ddgs"
	}
	if primary == "opencode" {
		primary = "deepseek"
	}
	primaryProxy := o.effectiveProxy(false)
	fallbackProxy := o.effectiveProxy(true)
	cacheKey := o.cacheKey(primary, primaryProxy, fallbackProxy, query, limit)
	if !o.BypassCache {
		if cached, ok := getSearchCache(cacheKey, time.Now()); ok {
			recordSearchHit(cached.Backend, cached.FallbackFrom)
			return cached, nil
		}
	}

	var primaryErr error
	if primary != "brave-html" {
		if msg := strings.TrimSpace(o.DiagnosticError); msg != "" {
			primaryErr = errors.New(msg)
		} else {
			stepTimeout := webSearchPrimaryTimeout
			if primary == "deepseek" {
				stepTimeout = webSearchDeepSeekTimeout
			}
			pctx, cancel := withStepTimeout(ctx, stepTimeout)
			res, err := o.runPrimaryBackend(pctx, primary, primaryProxy, query, limit)
			cancel()
			if err == nil && len(res) > 0 {
				outcome := WebSearchOutcome{Results: res, Backend: primary}
				if !o.BypassCache {
					putSearchCache(cacheKey, outcome, time.Now())
				}
				recordSearchResult(primary, "", nil)
				return outcome, nil
			}
			if err == nil {
				err = fmt.Errorf("%s 返回 0 条结果", primary)
			}
			primaryErr = err
		}
	}

	bctx, bcancel := withStepTimeout(ctx, webSearchFallbackTimeout)
	brave, braveErr := braveHTMLSearch(bctx, query, limit, fallbackProxy)
	bcancel()
	if braveErr == nil && len(brave) > 0 {
		fallbackFrom := ""
		fallbackReason := ""
		if primary != "brave-html" {
			fallbackFrom = primary
			if primaryErr != nil {
				fallbackReason = primaryErr.Error()
			}
		}
		outcome := WebSearchOutcome{
			Results:        brave,
			Backend:        "brave-html",
			FallbackFrom:   fallbackFrom,
			FallbackReason: fallbackReason,
		}
		if !o.BypassCache {
			putSearchCache(cacheKey, outcome, time.Now())
		}
		recordSearchResult("brave-html", fallbackFrom, nil)
		return outcome, nil
	}
	if braveErr == nil {
		braveErr = fmt.Errorf("brave-html 返回 0 条结果")
	}

	if primary != "ddgs" {
		dctx, dcancel := withStepTimeout(ctx, webSearchFallbackTimeout)
		res, err := ddgsSearch(dctx, query, limit, fallbackProxy)
		dcancel()
		if err == nil && len(res) > 0 {
			fallbackReason := fmt.Sprintf("brave-html: %v", braveErr)
			if primaryErr != nil {
				fallbackReason = fmt.Sprintf("primary: %v; brave-html: %v", primaryErr, braveErr)
			}
			outcome := WebSearchOutcome{
				Results:        res,
				Backend:        "ddgs",
				FallbackFrom:   primary,
				FallbackReason: fallbackReason,
			}
			if !o.BypassCache {
				putSearchCache(cacheKey, outcome, time.Now())
			}
			recordSearchResult("ddgs", primary, nil)
			return outcome, nil
		}
		if err == nil {
			err = fmt.Errorf("ddgs 返回 0 条结果")
		}
		var combined error
		if primaryErr != nil {
			combined = fmt.Errorf("primary: %v; brave-html: %v; ddgs: %v", primaryErr, braveErr, err)
		} else {
			combined = fmt.Errorf("brave-html: %v; ddgs: %v", braveErr, err)
		}
		recordSearchResult(primary, "", combined)
		return WebSearchOutcome{}, combined
	}

	var combined error
	if primaryErr != nil {
		combined = fmt.Errorf("primary: %v; brave-html: %v", primaryErr, braveErr)
	} else {
		combined = braveErr
	}
	recordSearchResult(primary, "", combined)
	return WebSearchOutcome{}, combined
}

func (o WebSearchOpts) runPrimaryBackend(ctx context.Context, primary, proxyAddr, query string, limit int) ([]actool.SearchResult, error) {
	switch primary {
	case "ddgs":
		return ddgsSearch(ctx, query, limit, proxyAddr)
	case "brave-free":
		return braveAPISearch(ctx, o.BraveKey, query, limit, proxyAddr)
	case "tavily":
		return tavilySearch(ctx, o.TavilyKey, query, limit, proxyAddr)
	case "commandcode":
		return commandCodeSearch(ctx, o.CommandCodeBaseURL, o.CommandCodeAPIKey, query, limit, proxyAddr)
	case "searxng":
		return searxngSearch(ctx, o.SearxngURL, query, limit, proxyAddr)
	case "deepseek":
		if strings.TrimSpace(proxyAddr) != "" {
			if _, err := searchHTTPClient(proxyAddr); err != nil {
				return nil, err
			}
		}
		cfg := actool.WebSearchConfig{
			Backend:         "deepseek",
			DeepSeekBaseURL: o.DeepSeekBaseURL,
			DeepSeekAPIKey:  o.DeepSeekAPIKey,
			DeepSeekModel:   o.DeepSeekModel,
			DeepSeekHeaders: o.Headers,
			Proxy:           proxyAddr,
		}
		return actool.WebSearchProbe(ctx, cfg, query, limit)
	default:
		return nil, fmt.Errorf("未知搜索后端 %q", primary)
	}
}

// NewWebSearchTool builds BLADE's own web_search tool from a static WebSearchOpts.
func NewWebSearchTool(o WebSearchOpts) actool.CoreTool {
	return NewWebSearchToolWithResolver(func() WebSearchOpts { return o })
}

// NewWebSearchToolWithResolver builds web_search with a per-invocation resolver so
// long-lived hot sessions see settings/key/backend/proxy updates immediately.
func NewWebSearchToolWithResolver(resolve func() WebSearchOpts) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "web_search",
		Description: "Search the web and return a ranked list of results (title, URL, description), up to 5 by default. If the configured backend is rate-limited, this tool automatically falls back to Brave's public HTML results (no key) and then DuckDuckGo. Use WebFetch to read a specific URL.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "The search query."},
				"limit": map[string]any{"type": "integer", "description": "Maximum number of results (1-20). Default 5.", "minimum": 1, "maximum": 20},
			},
			"required": []any{"query"},
		},
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(_ context.Context, input json.RawMessage, _ permission.Context) permission.Decision {
			var in struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(input, &in)
			return permission.AskUser("web search: " + in.Query)
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var in struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return actool.Errorf("Error: query must not be empty"), nil
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 5
			}
			if limit > 20 {
				limit = 20
			}
			var o WebSearchOpts
			if resolve != nil {
				o = resolve()
			}
			if !o.Enabled {
				return actool.Errorf("Error: web_search has been disabled in system or agent settings"), nil
			}
			wall := 60 * time.Second
			if strings.EqualFold(strings.TrimSpace(o.Backend), "deepseek") {
				wall = 120 * time.Second // server-side model call + search
			}
			cctx, cancel := context.WithTimeout(ctx, wall)
			defer cancel()
			outcome, err := o.searchDetailed(cctx, query, limit)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("Error searching web: %s", err.Error())), nil
			}
			out := map[string]any{
				"backend": outcome.Backend,
				"query":   query,
				"results": outcome.Results,
			}
			if outcome.FromCache {
				out["cached"] = true
			}
			if outcome.FallbackFrom != "" && outcome.FallbackFrom != outcome.Backend {
				out["fallback_from"] = outcome.FallbackFrom
				if outcome.FallbackReason != "" {
					out["fallback_reason"] = outcome.FallbackReason
				}
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return actool.Text(string(b)), nil
		},
	})
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxBytes))
}

func extractUpstreamError(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) == nil {
		for _, key := range []string{"error", "message", "detail"} {
			v, ok := payload[key]
			if !ok || v == nil {
				continue
			}
			switch tv := v.(type) {
			case string:
				if s := strings.TrimSpace(tv); s != "" {
					return s
				}
			case map[string]any:
				if msg, ok := tv["message"].(string); ok && strings.TrimSpace(msg) != "" {
					return strings.TrimSpace(msg)
				}
				if b, err := json.Marshal(tv); err == nil {
					return string(b)
				}
			default:
				if b, err := json.Marshal(tv); err == nil {
					return string(b)
				}
			}
		}
	}
	if len([]rune(trimmed)) > 240 {
		runes := []rune(trimmed)
		trimmed = string(runes[:240]) + "…"
	}
	return trimmed
}

func formatHTTPError(provider string, status int, raw []byte) error {
	hint := ""
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = "（API Key 无效、过期或无权限）"
	case http.StatusTooManyRequests:
		hint = "（请求频率或配额超限）"
	}
	detail := extractUpstreamError(raw)
	if detail != "" {
		return fmt.Errorf("%s 返回 HTTP %d%s: %s", provider, status, hint, detail)
	}
	return fmt.Errorf("%s 返回 HTTP %d%s", provider, status, hint)
}

func isAntiBotChallenge(raw []byte) string {
	lower := strings.ToLower(string(raw))
	switch {
	case strings.Contains(lower, "anomaly.js"),
		strings.Contains(lower, "unfortunately, bots use duckduckgo"),
		strings.Contains(lower, "challenge-form"):
		return "遇到 DuckDuckGo 人机验证/风控挑战（建议切换健康代理节点或改用 API 搜索源）"
	case strings.Contains(lower, "pow-captcha"),
		strings.Contains(lower, "brave.com/cdn-cgi"),
		strings.Contains(lower, "checking your browser"),
		strings.Contains(lower, "just a moment...") && strings.Contains(lower, "cf-"):
		return "遇到 Brave 人机验证/PoW 挑战（建议切换出口节点或改用官方 Brave API / SearXNG）"
	case strings.Contains(lower, "g-recaptcha"),
		strings.Contains(lower, "hcaptcha"),
		strings.Contains(lower, "cf-turnstile"):
		return "目标搜索页面返回验证码拦截（CAPTCHA）"
	default:
		return ""
	}
}

func ddgsSearch(ctx context.Context, query string, limit int, proxyAddr string) ([]actool.SearchResult, error) {
	client, err := searchHTTPClient(proxyAddr)
	if err != nil {
		return nil, err
	}
	form := url.Values{"q": {query}, "kl": {"wt-wt"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ddgsEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", webSearchDefaultBrowserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://html.duckduckgo.com/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("DuckDuckGo 限流（HTTP %d）：请稍后重试或检查/切换出口代理", resp.StatusCode)
	}
	raw, err := readLimited(resp.Body, webSearchMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPError("DuckDuckGo", resp.StatusCode, raw)
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("could not parse DuckDuckGo response: %w", err)
	}
	results := parseDDGS(doc, limit)
	if len(results) == 0 {
		if msg := isAntiBotChallenge(raw); msg != "" {
			return nil, errors.New(msg)
		}
	}
	return results, nil
}

func parseDDGS(doc *html.Node, limit int) []actool.SearchResult {
	type hit struct{ title, href string }
	var hits []hit
	var snippets []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "result__a") {
			hits = append(hits, hit{title: nodeText(n), href: unwrapDDGHref(attr(n, "href"))})
		}
		if n.Type == html.ElementNode && hasClass(n, "result__snippet") {
			snippets = append(snippets, nodeText(n))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	out := make([]actool.SearchResult, 0, min(limit, len(hits)))
	for i, h := range hits {
		if len(out) >= limit {
			break
		}
		if h.href == "" {
			continue
		}
		desc := ""
		if i < len(snippets) {
			desc = snippets[i]
		}
		out = append(out, actool.SearchResult{
			Title:       strings.TrimSpace(h.title),
			URL:         h.href,
			Description: strings.TrimSpace(desc),
			Position:    len(out) + 1,
		})
	}
	return out
}

func unwrapDDGHref(href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return ""
}

func braveAPISearch(ctx context.Context, apiKey, query string, limit int, proxyAddr string) ([]actool.SearchResult, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("web_search: brave-free 需要 Brave Search API Key")
	}
	client, err := searchHTTPClient(proxyAddr)
	if err != nil {
		return nil, err
	}
	count := min(limit, 20)
	q := url.Values{"q": {query}, "count": {fmt.Sprintf("%d", count)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveAPIEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := readLimited(resp.Body, webSearchMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPError("Brave Search", resp.StatusCode, raw)
	}
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("could not parse Brave Search response: %w", err)
	}
	out := make([]actool.SearchResult, 0, min(limit, len(payload.Web.Results)))
	for _, r := range payload.Web.Results {
		if len(out) >= limit {
			break
		}
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, actool.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Description,
			Position:    len(out) + 1,
		})
	}
	return out, nil
}

func tavilySearch(ctx context.Context, apiKey, query string, limit int, proxyAddr string) ([]actool.SearchResult, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("web_search: tavily 需要 Tavily API Key")
	}
	client, err := searchHTTPClient(proxyAddr)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"api_key":      key,
		"query":        query,
		"max_results":  min(limit, 20),
		"search_depth": "basic",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := readLimited(resp.Body, webSearchMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPError("Tavily Search", resp.StatusCode, raw)
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("could not parse Tavily Search response: %w", err)
	}
	out := make([]actool.SearchResult, 0, min(limit, len(payload.Results)))
	for _, r := range payload.Results {
		if len(out) >= limit {
			break
		}
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		out = append(out, actool.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Content,
			Position:    len(out) + 1,
		})
	}
	return out, nil
}

func commandCodeEndpoint(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/alpha/web-search"
}

func commandCodeSearch(ctx context.Context, baseURL, apiKey, query string, limit int, proxyAddr string) ([]actool.SearchResult, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("web_search: commandcode 需要 API Key")
	}
	ep := commandCodeEndpoint(baseURL)
	if ep == "" {
		return nil, fmt.Errorf("web_search: commandcode 需要有效的 BaseURL")
	}
	client, err := searchHTTPClient(proxyAddr)
	if err != nil {
		return nil, err
	}
	n := limit
	if n <= 0 {
		n = 5
	}
	if n > 10 {
		n = 10
	}
	payload, err := json.Marshal(map[string]any{"query": query, "numResults": n})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", DefaultOutboundUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := readLimited(resp.Body, webSearchMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPError("CommandCode Search", resp.StatusCode, raw)
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("could not parse CommandCode search response: %w", err)
	}
	results := make([]actool.SearchResult, 0, len(out.Results))
	seen := map[string]bool{}
	for _, r := range out.Results {
		u := strings.TrimSpace(r.URL)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		results = append(results, actool.SearchResult{
			Title:       r.Title,
			URL:         u,
			Description: r.Snippet,
			Position:    len(results) + 1,
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func isLocalSearxngHost(host string) bool {
	h := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if h == "" {
		return false
	}
	if h == "localhost" || h == "searxng" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func searxngSearch(ctx context.Context, baseURL, query string, limit int, proxyAddr string) ([]actool.SearchResult, error) {
	rawBase := strings.TrimSpace(baseURL)
	if rawBase == "" {
		return nil, fmt.Errorf("web_search: searxng 需要配置 SearXNG 实例地址")
	}
	parsed, err := url.Parse(rawBase)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("web_search: 无效的 SearXNG 实例地址 %q", rawBase)
	}
	// 本机回环或 Docker Compose 同网络伴生 searxng 服务必须直连，
	// 避免用户配置了独立搜索代理（用于 ddgs/brave）后把本机流量误转发到远端代理。
	effectiveProxy := proxyAddr
	if isLocalSearxngHost(parsed.Hostname()) {
		effectiveProxy = ""
	}
	client, err := searchHTTPClient(effectiveProxy)
	if err != nil {
		return nil, err
	}
	searchURL := strings.TrimRight(rawBase, "/") + "/search?" + url.Values{"q": {query}, "format": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", webSearchDefaultBrowserUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := readLimited(resp.Body, webSearchMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPError("SearXNG", resp.StatusCode, raw)
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("could not parse SearXNG JSON response: %w", err)
	}
	results := make([]actool.SearchResult, 0, min(limit, len(payload.Results)))
	seen := map[string]bool{}
	for _, r := range payload.Results {
		u := strings.TrimSpace(r.URL)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		desc := strings.TrimSpace(r.Content)
		if desc == "" {
			desc = strings.TrimSpace(r.Snippet)
		}
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = u
		}
		results = append(results, actool.SearchResult{
			Title:       title,
			URL:         u,
			Description: desc,
			Position:    len(results) + 1,
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func braveHTMLSearch(ctx context.Context, query string, limit int, proxyAddr string) ([]actool.SearchResult, error) {
	client, err := searchHTTPClient(proxyAddr)
	if err != nil {
		return nil, err
	}
	q := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveHTMLEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchDefaultBrowserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, readErr := readLimited(resp.Body, webSearchHTMLMaxBytes)
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPError("Brave HTML", resp.StatusCode, raw)
	}
	if readErr != nil && len(raw) == 0 {
		return nil, readErr
	}
	results, err := parseBraveHTML(raw, limit)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		if msg := isAntiBotChallenge(raw); msg != "" {
			return nil, errors.New(msg)
		}
	}
	return results, nil
}

func parseBraveHTML(raw []byte, limit int) ([]actool.SearchResult, error) {
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("could not parse Brave HTML: %w", err)
	}
	out := collectBraveNodes(doc, limit, true)
	if len(out) == 0 {
		// 兼容新版或无 data-type="web" 标注的 Brave 页面，但仍要求含非 Brave 自有域外链。
		out = collectBraveNodes(doc, limit, false)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func collectBraveNodes(doc *html.Node, limit int, strict bool) []actool.SearchResult {
	out := make([]actool.SearchResult, 0, limit)
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "snippet") {
			if strict && !strings.EqualFold(attr(n, "data-type"), "web") {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				return
			}
			link := firstExternalHTTPHref(n)
			title := firstClassText(n, "title")
			if title == "" {
				title = firstClassText(n, "snippet-title")
			}
			desc := firstClassText(n, "content")
			if desc == "" {
				desc = firstClassText(n, "snippet-description")
			}
			if link != "" && title != "" && !seen[link] {
				seen[link] = true
				out = append(out, actool.SearchResult{
					Title: title, URL: link, Description: desc, Position: len(out) + 1,
				})
				return // don't descend into nested result clusters
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func hasClass(n *html.Node, class string) bool {
	for _, part := range strings.Fields(attr(n, "class")) {
		if part == class {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func firstExternalHTTPHref(n *html.Node) string {
	var found string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if found != "" {
			return
		}
		if x.Type == html.ElementNode && x.Data == "a" {
			href := strings.TrimSpace(attr(x, "href"))
			if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
				if u, err := url.Parse(href); err == nil {
					host := strings.ToLower(u.Hostname())
					if host != "" && !strings.HasSuffix(host, "brave.com") {
						found = href
						return
					}
				}
			}
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

func firstClassText(n *html.Node, class string) string {
	var found string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if found != "" {
			return
		}
		if x.Type == html.ElementNode && hasClass(x, class) {
			found = nodeText(x)
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func searchHTTPClient(proxyAddr string) (*http.Client, error) {
	// 显式置 nil Proxy，杜绝未配搜索代理时隐式读取进程 HTTP_PROXY 环境变量越权出网。
	return newProxyAwareHTTPClient(proxyAddr, 30*time.Second)
}
