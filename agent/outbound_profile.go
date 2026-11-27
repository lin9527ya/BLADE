package agent

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	actool "github.com/lin9527ya/Norma/tool"
	"golang.org/x/net/proxy"
)

// DialTLSContextFunc allows direct-egress HTTP transports to plug in a custom
// TLS dialer (for future uTLS ClientHello alignment without disturbing pooled proxies).
type DialTLSContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// OutboundProfile describes a coherent set of browser identity headers so every
// target-facing tool speaks with one internally consistent persona per task.
type OutboundProfile struct {
	ID              string
	Family          string // "chromium" | "firefox"
	UserAgent       string
	AcceptNav       string
	AcceptAPI       string
	AcceptLanguage  string
	AcceptEncoding  string
	SecCHUA         string // empty for Firefox profiles
	SecCHUAMobile   string
	SecCHUAPlatform string
}

var outboundProfiles = []OutboundProfile{
	{
		ID:              "chrome157-win",
		Family:          "chromium",
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36",
		AcceptNav:       "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		AcceptAPI:       "application/json, text/plain, */*",
		AcceptLanguage:  "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7",
		AcceptEncoding:  "gzip, deflate, br",
		SecCHUA:         `"Google Chrome";v="157", "Chromium";v="157", "Not-A.Brand";v="99"`,
		SecCHUAMobile:   "?0",
		SecCHUAPlatform: `"Windows"`,
	},
	{
		ID:              "chrome156-win",
		Family:          "chromium",
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36",
		AcceptNav:       "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		AcceptAPI:       "application/json, text/plain, */*",
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
		SecCHUA:         `"Google Chrome";v="156", "Chromium";v="156", "Not)A;Brand";v="8"`,
		SecCHUAMobile:   "?0",
		SecCHUAPlatform: `"Windows"`,
	},
	{
		ID:              "chrome157-mac",
		Family:          "chromium",
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36",
		AcceptNav:       "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		AcceptAPI:       "application/json, text/plain, */*",
		AcceptLanguage:  "en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7",
		AcceptEncoding:  "gzip, deflate, br",
		SecCHUA:         `"Google Chrome";v="157", "Chromium";v="157", "Not-A.Brand";v="99"`,
		SecCHUAMobile:   "?0",
		SecCHUAPlatform: `"macOS"`,
	},
	{
		ID:              "edge156-win",
		Family:          "chromium",
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0",
		AcceptNav:       "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		AcceptAPI:       "application/json, text/plain, */*",
		AcceptLanguage:  "zh-CN,zh;q=0.9,en;q=0.8",
		AcceptEncoding:  "gzip, deflate, br",
		SecCHUA:         `"Microsoft Edge";v="156", "Chromium";v="156", "Not)A;Brand";v="8"`,
		SecCHUAMobile:   "?0",
		SecCHUAPlatform: `"Windows"`,
	},
	{
		ID:              "chrome157-linux",
		Family:          "chromium",
		UserAgent:       "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36",
		AcceptNav:       "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		AcceptAPI:       "application/json, text/plain, */*",
		AcceptLanguage:  "en-US,en;q=0.9",
		AcceptEncoding:  "gzip, deflate, br",
		SecCHUA:         `"Google Chrome";v="157", "Chromium";v="157", "Not-A.Brand";v="99"`,
		SecCHUAMobile:   "?0",
		SecCHUAPlatform: `"Linux"`,
	},
	{
		ID:             "firefox152-linux",
		Family:         "firefox",
		UserAgent:      "Mozilla/5.0 (X11; Linux x86_64; rv:152.0) Gecko/20100101 Firefox/152.0",
		AcceptNav:      "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		AcceptAPI:      "application/json, text/plain, */*",
		AcceptLanguage: "en-US,en;q=0.5",
		AcceptEncoding: "gzip, deflate, br",
	},
	{
		ID:             "firefox151-win",
		Family:         "firefox",
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:151.0) Gecko/20100101 Firefox/151.0",
		AcceptNav:      "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		AcceptAPI:      "application/json, text/plain, */*",
		AcceptLanguage: "zh-CN,zh;q=0.8,en-US;q=0.5,en;q=0.3",
		AcceptEncoding: "gzip, deflate, br",
	},
}

// DefaultOutboundUserAgent returns the canonical Chromium User-Agent when no
// task context or environment override applies.
func DefaultOutboundUserAgent() string {
	return outboundProfiles[0].UserAgent
}

// OutboundProfileForTask picks a deterministic, task-consistent browser persona.
// Non-positive taskID resolves to the primary Windows Chrome profile.
func OutboundProfileForTask(taskID int64) OutboundProfile {
	if custom := strings.TrimSpace(os.Getenv("BLADE_OUTBOUND_UA")); custom != "" {
		p := outboundProfiles[0]
		p.ID = "env-override"
		p.Family = ""
		p.UserAgent = custom
		p.SecCHUA = ""
		p.SecCHUAMobile = ""
		p.SecCHUAPlatform = ""
		return p
	}
	if taskID <= 0 {
		return outboundProfiles[0]
	}
	idx := int(uint64(taskID)*0x9e3779b97f4a7c15%uint64(len(outboundProfiles)))
	return outboundProfiles[idx]
}

// ChromiumProfileForTask picks a Chromium-family persona suitable for headless
// Chromium / Playwright MCP flags.
func ChromiumProfileForTask(taskID int64) OutboundProfile {
	p := OutboundProfileForTask(taskID)
	if p.Family == "chromium" {
		return p
	}
	var chromes []OutboundProfile
	for _, item := range outboundProfiles {
		if item.Family == "chromium" {
			chromes = append(chromes, item)
		}
	}
	if len(chromes) == 0 {
		return outboundProfiles[0]
	}
	if taskID <= 0 {
		return chromes[0]
	}
	return chromes[int(uint64(taskID)%uint64(len(chromes)))]
}

// ApplyRequestProfile fills coherent browser defaults for headers NOT already
// overridden in customHeaders. When User-Agent is overridden explicitly by the
// caller, Sec-CH-UA* headers are skipped so the wire headers stay consistent.
func ApplyRequestProfile(req *http.Request, prof OutboundProfile, apiStyle bool, customHeaders map[string]bool) {
	if req == nil {
		return
	}
	has := func(key string) bool {
		return customHeaders[strings.ToLower(key)]
	}
	if !has("user-agent") && prof.UserAgent != "" {
		req.Header.Set("User-Agent", prof.UserAgent)
	}
	if !has("accept") {
		acc := prof.AcceptNav
		if apiStyle && prof.AcceptAPI != "" {
			acc = prof.AcceptAPI
		}
		if acc != "" {
			req.Header.Set("Accept", acc)
		}
	}
	if !has("accept-language") && prof.AcceptLanguage != "" {
		req.Header.Set("Accept-Language", prof.AcceptLanguage)
	}
	if !has("accept-encoding") && prof.AcceptEncoding != "" {
		req.Header.Set("Accept-Encoding", prof.AcceptEncoding)
	}
	if prof.Family == "chromium" && !has("user-agent") {
		if !has("sec-ch-ua") && prof.SecCHUA != "" {
			req.Header.Set("Sec-CH-UA", prof.SecCHUA)
		}
		if !has("sec-ch-ua-mobile") && prof.SecCHUAMobile != "" {
			req.Header.Set("Sec-CH-UA-Mobile", prof.SecCHUAMobile)
		}
		if !has("sec-ch-ua-platform") && prof.SecCHUAPlatform != "" {
			req.Header.Set("Sec-CH-UA-Platform", prof.SecCHUAPlatform)
		}
	}
}

func newBLADEWebFetch(proxyFn func() (string, string), caCert string, taskID int64) actool.CoreTool {
	return actool.NewWebFetch(actool.WebFetchConfig{
		ProxyFunc:             proxyFn,
		CACert:                caCert,
		DisableDirectFallback: true,
		UserAgent:             OutboundProfileForTask(taskID).UserAgent,
	})
}

// wrapDecompressedBody decodes gzip, deflate, or brotli HTTP responses when
// explicit Accept-Encoding bypassed net/http's automatic gzip reader. Malformed
// compressed headers fall back to the raw stream without losing initial bytes.
func wrapDecompressedBody(resp *http.Response) (io.Reader, func()) {
	if resp == nil || resp.Body == nil || resp.Uncompressed {
		if resp == nil {
			return nil, func() {}
		}
		return resp.Body, func() {}
	}
	ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if ce == "" || ce == "identity" {
		return resp.Body, func() {}
	}
	// Strip outer hop/multiple directives to handle simple encodings cleanly.
	if idx := strings.LastIndex(ce, ","); idx >= 0 {
		ce = strings.TrimSpace(ce[idx+1:])
	}
	switch ce {
	case "gzip", "x-gzip":
		br := bufio.NewReaderSize(resp.Body, 4096)
		magic, err := br.Peek(2)
		if err != nil || len(magic) < 2 || magic[0] != 0x1f || magic[1] != 0x8b {
			return br, func() {}
		}
		gr, err := gzip.NewReader(br)
		if err != nil {
			return br, func() {}
		}
		return gr, func() { _ = gr.Close() }
	case "deflate":
		br := bufio.NewReaderSize(resp.Body, 4096)
		peek, _ := br.Peek(2)
		if len(peek) == 0 {
			return br, func() {}
		}
		var probed bytes.Buffer
		if zr, err := zlib.NewReader(io.TeeReader(br, &probed)); err == nil {
			return zr, func() { _ = zr.Close() }
		}
		fr := flate.NewReader(io.MultiReader(&probed, br))
		return fr, func() { _ = fr.Close() }
	case "br":
		return brotli.NewReader(resp.Body), func() {}
	default:
		return resp.Body, func() {}
	}
}

// newProxyAwareHTTPClient creates an HTTP client that honors an explicit
// http(s)/socks5(h) proxy without falling back to ambient process proxy env vars.
func newProxyAwareHTTPClient(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	proxyAddr = strings.TrimSpace(proxyAddr)
	tr := &http.Transport{Proxy: nil}
	if proxyAddr != "" {
		u, err := url.Parse(proxyAddr)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid proxy %q", proxyAddr)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			tr.Proxy = http.ProxyURL(u)
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if u.User != nil {
				pw, _ := u.User.Password()
				auth = &proxy.Auth{User: u.User.Username(), Password: pw}
			}
			d, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
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
			return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
		}
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// sanitizeCurlRCValue strips quotes and newlines so .curlrc remains well-formed.
func sanitizeCurlRCValue(s string) string {
	r := strings.NewReplacer("\r", "", "\n", "", `"`, "")
	return strings.TrimSpace(r.Replace(s))
}

// TaskSubprocessEnv returns subprocess defaults (TARGET_USER_AGENT,
// TARGET_ACCEPT_LANGUAGE, and an isolated CURL_HOME/.curlrc when taskRoot is
// provided) so ad-hoc curl/python/scanner commands do not fall back to bare
// curl/<ver> User-Agents while still allowing explicit -A / -H overrides.
func TaskSubprocessEnv(taskRoot string, taskID int64) []string {
	prof := OutboundProfileForTask(taskID)
	env := []string{
		"TARGET_USER_AGENT=" + prof.UserAgent,
		"TARGET_ACCEPT_LANGUAGE=" + prof.AcceptLanguage,
	}
	if root := strings.TrimSpace(taskRoot); root != "" {
		curlDir := filepath.Join(root, ".curl_env")
		if err := os.MkdirAll(curlDir, 0o755); err == nil {
			_ = os.Chmod(curlDir, 0o755)
			rcPath := filepath.Join(curlDir, ".curlrc")
			content := fmt.Sprintf("user-agent = %q\n", sanitizeCurlRCValue(prof.UserAgent))
			if cur, err := os.ReadFile(rcPath); err != nil || string(cur) != content {
				if os.WriteFile(rcPath, []byte(content), 0o644) == nil {
					_ = os.Chmod(rcPath, 0o644)
				}
			}
			env = append(env, "CURL_HOME="+curlDir)
		}
	}
	return env
}
