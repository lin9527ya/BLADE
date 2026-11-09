package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"golang.org/x/net/html"
	"golang.org/x/net/proxy"
)

// braveHTMLEndpoint is a variable so tests can point it at an httptest server.
var braveHTMLEndpoint = "https://search.brave.com/search"

// WebSearchProbe runs a search through the configured primary backend and, when
// it is rate-limited or unavailable, falls back to Brave's public HTML results
// (no key) and then DuckDuckGo. It returns the actual backend and the original
// backend when a fallback ran.
func WebSearchProbe(o WebSearchOpts, query string, limit int) ([]actool.SearchResult, string, string, error) {
	return WebSearchProbeContext(context.Background(), o, query, limit)
}

// WebSearchProbeContext is the request-scoped variant used by the settings
// "test search" handler so a slow proxy cannot outlive the HTTP request.
func WebSearchProbeContext(ctx context.Context, o WebSearchOpts, query string, limit int) ([]actool.SearchResult, string, string, error) {
	return o.search(ctx, query, limit)
}

func (o WebSearchOpts) search(ctx context.Context, query string, limit int) ([]actool.SearchResult, string, string, error) {
	primary := strings.ToLower(strings.TrimSpace(o.Backend))
	if primary == "" {
		primary = "ddgs"
	}
	if primary == "opencode" {
		primary = "deepseek"
	}
	var primaryErr error
	if primary != "brave-html" {
		cfg := actool.WebSearchConfig{
			Backend: primary, BraveAPIKey: o.BraveKey, TavilyAPIKey: o.TavilyKey,
			DeepSeekBaseURL: o.DeepSeekBaseURL, DeepSeekAPIKey: o.DeepSeekAPIKey,
			DeepSeekModel: o.DeepSeekModel, DeepSeekHeaders: o.Headers,
			CommandCodeBaseURL: o.CommandCodeBaseURL, CommandCodeAPIKey: o.CommandCodeAPIKey,
			Proxy: o.Proxy,
		}
		res, err := actool.WebSearchProbe(ctx, cfg, query, limit)
		if err == nil && len(res) > 0 {
			return res, primary, "", nil
		}
		if err == nil {
			err = fmt.Errorf("%s 返回 0 条结果", primary)
		}
		primaryErr = err
	}

	brave, braveErr := braveHTMLSearch(ctx, query, limit, o.Proxy)
	if braveErr == nil && len(brave) > 0 {
		return brave, "brave-html", primary, nil
	}
	if braveErr == nil {
		braveErr = fmt.Errorf("brave-html 返回 0 条结果")
	}

	// Last resort. DDG itself is subject to 202; the caller gets every error
	// when no source can serve the query.
	if primary != "ddgs" {
		cfg := actool.WebSearchConfig{Backend: "ddgs", Proxy: o.Proxy}
		res, err := actool.WebSearchProbe(ctx, cfg, query, limit)
		if err == nil && len(res) > 0 {
			return res, "ddgs", primary, nil
		}
		if err == nil {
			err = fmt.Errorf("ddgs 返回 0 条结果")
		}
		if primaryErr != nil {
			return nil, "", "", fmt.Errorf("primary: %v; brave-html: %v; ddgs: %v", primaryErr, braveErr, err)
		}
		return nil, "", "", fmt.Errorf("brave-html: %v; ddgs: %v", braveErr, err)
	}
	if primaryErr != nil {
		return nil, "", "", fmt.Errorf("primary: %v; brave-html: %v", primaryErr, braveErr)
	}
	return nil, "", "", braveErr
}

// NewWebSearchTool builds BLADE's own web_search tool. It deliberately wraps
// Norma's probe instead of replacing its providers, so the existing ddgs /
// brave-free / tavily / deepseek / commandcode implementations stay in one
// place; the added value is the no-key Brave HTML fallback for DDG's frequent
// 202s.
func NewWebSearchTool(o WebSearchOpts) actool.CoreTool {
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
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			results, actual, fallbackFrom, err := o.search(cctx, query, limit)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("Error searching web: %s", err.Error())), nil
			}
			out := map[string]any{"backend": actual, "query": query, "results": results}
			if fallbackFrom != "" && fallbackFrom != actual {
				out["fallback_from"] = fallbackFrom
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return actool.Text(string(b)), nil
		},
	})
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
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Brave HTML returned HTTP %d", resp.StatusCode)
	}
	raw := make([]byte, 0, 1<<20)
	buf := make([]byte, 64<<10)
	for len(raw) < 4<<20 {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return parseBraveHTML(raw, limit)
}

func parseBraveHTML(raw []byte, limit int) ([]actool.SearchResult, error) {
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("could not parse Brave HTML: %w", err)
	}
	out := make([]actool.SearchResult, 0, limit)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "snippet") && strings.EqualFold(attr(n, "data-type"), "web") {
			link := firstHTTPHref(n)
			title := firstClassText(n, "title")
			desc := firstClassText(n, "content")
			if link != "" && title != "" {
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
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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

func firstHTTPHref(n *html.Node) string {
	var found string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if found != "" {
			return
		}
		if x.Type == html.ElementNode && x.Data == "a" {
			href := strings.TrimSpace(attr(x, "href"))
			if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
				found = href
				return
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
	proxyAddr = strings.TrimSpace(proxyAddr)
	tr := &http.Transport{}
	if proxyAddr != "" {
		u, err := url.Parse(proxyAddr)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid search proxy %q", proxyAddr)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			tr.Proxy = http.ProxyURL(u)
		case "socks5", "socks5h":
			d, err := proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct)
			if err != nil {
				return nil, err
			}
			cd, ok := d.(proxy.ContextDialer)
			if !ok {
				return nil, fmt.Errorf("socks5 proxy does not support context")
			}
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return cd.DialContext(ctx, network, addr)
			}
		default:
			return nil, fmt.Errorf("unsupported search proxy scheme %q", u.Scheme)
		}
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}, nil
}
