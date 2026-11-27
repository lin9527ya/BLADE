package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func fakeOASTToken(sub string) string {
	payload, _ := json.Marshal(map[string]any{"subdomain": sub, "exp": 9999999999})
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

func withOAST(t *testing.T, token, domain string, on bool) {
	t.Helper()
	old := OASTConfig
	OASTConfig = func() (string, string, bool) { return token, domain, on }
	t.Cleanup(func() { OASTConfig = old })
}

// 回调地址使用不透明随机令牌：外发 URL 绝不暴露任务编号或漏洞标签，同时支持本地标签对照。
func TestOASTMintBuildsMarkedURL(t *testing.T) {
	withOAST(t, fakeOASTToken("ok9iwmcq"), "requestrepo.com", true)
	tools, _, _ := newToolWorker(t)
	tools.SetTaskRoot(t.TempDir())
	out := callTool(t, tools.WorkerTools(), "oast_mint", `{"label":"ssrf_admin"}`)
	if !strings.Contains(out, "http://ok9iwmcq.requestrepo.com/") {
		t.Fatalf("回调地址域名部分异常：%s", out)
	}
	if !strings.Contains(out, "oast_poll") {
		t.Fatalf("回执应告诉模型命中后怎么取：%s", out)
	}
	// 提取 URL 部分断言其中没有任何可识别特征。
	fields := strings.Fields(out)
	var mintedURL string
	for _, f := range fields {
		if idx := strings.Index(f, "http://ok9iwmcq.requestrepo.com/"); idx >= 0 {
			mintedURL = f[idx:]
			if cut := strings.IndexAny(mintedURL, "（("); cut > 0 {
				mintedURL = mintedURL[:cut]
			}
			break
		}
	}
	u, err := url.Parse(mintedURL)
	if err != nil || len(strings.TrimPrefix(u.Path, "/")) < 8 {
		t.Fatalf("minted callback URL 应为不透明路径 token，got=%q err=%v (%s)", mintedURL, err, out)
	}
	if strings.Contains(u.Path, fmt.Sprintf("cb%d", tools.taskID)) ||
		strings.Contains(strings.ToLower(u.Path), "ssrf") ||
		strings.Contains(strings.ToLower(u.Path), "admin") {
		t.Fatalf("外发 OAST URL 泄露了任务 ID 或标签词：%s", mintedURL)
	}
}

// 轮询只登记**新命中**：不透明令牌能命中并还原原 label；旧 cb<taskID> 亦兼容；控制端发正常 UA。
func TestOASTPollRecordsNewHitsOnce(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	tools.SetTaskRoot(t.TempDir())
	withOAST(t, fakeOASTToken("ok9iwmcq"), "requestrepo.com", true)

	mintOut := callTool(t, tools.WorkerTools(), "oast_mint", `{"label":"xml_cosmicsting"}`)
	var tok string
	for _, f := range strings.Fields(mintOut) {
		if idx := strings.Index(f, "http://ok9iwmcq.requestrepo.com/"); idx >= 0 {
			raw := f[idx:]
			if cut := strings.IndexAny(raw, "（("); cut > 0 {
				raw = raw[:cut]
			}
			u, _ := url.Parse(raw)
			if u != nil {
				tok = strings.TrimPrefix(u.Path, "/")
			}
		}
	}
	if tok == "" {
		t.Fatalf("未能提取 minted opaque token：%s", mintOut)
	}

	legacyMarker := tools.oastMarker()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+fakeOASTToken("ok9iwmcq") {
			t.Errorf("应带 Bearer token：%q", got)
		}
		if ua := r.Header.Get("User-Agent"); ua == "" || strings.Contains(ua, "Go-http-client") {
			t.Errorf("OAST 控制面请求暴露 Go 默认 UA：%q", ua)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": []map[string]any{
			{"_id": "hit-opaque", "type": "http", "method": "GET", "path": "/" + tok, "ip": "10.0.0.9", "country": "CN", "date": 1792815288},
			{"_id": "hit-legacy", "type": "http", "method": "GET", "path": "/" + legacyMarker + "-ssrf", "ip": "10.0.0.9", "country": "CN", "date": 1792815289},
			{"_id": "other-1", "type": "http", "method": "GET", "path": "/cb999-other", "ip": "10.0.0.10", "country": "CN"},
			{"_id": "dns-1", "type": "dns", "domain": tok + ".ok9iwmcq.requestrepo.com", "ip": "10.0.0.11", "country": "CN"},
		}})
	}))
	t.Cleanup(srv.Close)
	oldBase := OASTBaseURL
	OASTBaseURL = srv.URL
	t.Cleanup(func() { OASTBaseURL = oldBase })

	out := callTool(t, tools.WorkerTools(), "oast_poll", `{}`)
	if !strings.Contains(out, "callbacks: 3") {
		t.Fatalf("应记录 3 条本任务命中（opaque HTTP + legacy + opaque DNS），别的任务不算：%s", out)
	}
	if !strings.Contains(out, "xml-cosmicsting") {
		t.Fatalf("命中反馈应还原原本地标签 xml-cosmicsting：%s", out)
	}
	oastFacts := func() []map[string]any {
		facts, err := ts.ListByKind(db.KindFact, 50)
		if err != nil {
			t.Fatalf("列事实：%v", err)
		}
		var out []map[string]any
		for _, n := range facts {
			var p map[string]any
			if json.Unmarshal(n.Payload, &p) == nil && payloadString(p, "oast_request_id") != "" {
				out = append(out, p)
			}
		}
		return out
	}
	got := oastFacts()
	if len(got) != 3 {
		t.Fatalf("应落 3 条 OAST 事实：%d", len(got))
	}
	if got[0]["confidence"] != "observed" {
		t.Fatalf("事实应带 observed 置信度：%+v", got[0])
	}
	// 再轮询一次：同样的记录不该重复登记。
	if again := callTool(t, tools.WorkerTools(), "oast_poll", `{}`); !strings.Contains(again, "no new callbacks") {
		t.Fatalf("重复轮询应提示无新命中：%s", again)
	}
	if again := oastFacts(); len(again) != 3 {
		t.Fatalf("不该重复记事实：%d", len(again))
	}
}

// 没配置时两个工具都要明确拒绝（server 侧还会把它们从工具面移除）。
func TestOASTToolsRequireConfig(t *testing.T) {
	withOAST(t, "", "", false)
	tools, _, _ := newToolWorker(t)
	if out := callTool(t, tools.WorkerTools(), "oast_mint", `{}`); !strings.Contains(out, "未配置 OAST") {
		t.Fatalf("mint 应拒绝：%s", out)
	}
	if out := callTool(t, tools.WorkerTools(), "oast_poll", `{}`); !strings.Contains(out, "未配置 OAST") {
		t.Fatalf("poll 应拒绝：%s", out)
	}
}

// token 只用来拼子域，绝不回显。
func TestOASTDoesNotLeakToken(t *testing.T) {
	token := fakeOASTToken("ok9iwmcq")
	withOAST(t, token, "requestrepo.com", true)
	// 用本地空响应避免真的打外部服务。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"requests":[]}`))
	}))
	t.Cleanup(srv.Close)
	oldBase := OASTBaseURL
	OASTBaseURL = srv.URL
	t.Cleanup(func() { OASTBaseURL = oldBase })
	tools, _, _ := newToolWorker(t)
	for _, tool := range []string{"oast_mint", "oast_poll"} {
		out := callTool(t, tools.WorkerTools(), tool, `{}`)
		if strings.Contains(out, token) {
			t.Fatalf("%s 的回执泄露了 token：%s", tool, out)
		}
	}
}

// 任务标记必须以「非数字」为边界：任务 9 不能把任务 99 的回调算成自己的。
func TestOASTMatchesTaskMarkerBoundary(t *testing.T) {
	if oastMatches(oastRequest{Path: "/cb999-other"}, "cb9") {
		t.Fatal("cb9 不应匹配 cb999")
	}
	if !oastMatches(oastRequest{Path: "/cb9-ssrf"}, "cb9") {
		t.Fatal("cb9 应匹配 cb9-ssrf")
	}
	if !oastMatches(oastRequest{Domain: "cb9.ok9iwmcq.requestrepo.com"}, "cb9") {
		t.Fatal("cb9 应匹配 DNS 域名")
	}
	if oastMatches(oastRequest{Path: "/cb12-ssrf"}, "cb1") {
		t.Fatal("cb1 不应匹配 cb12")
	}
}

// 纯单测（不依赖 PG 数据库）：验证不透明 OAST token 的生成/跨实例落盘恢复/精确边界匹配/原 label 还原，以及控制端 UA。
func TestOASTOpaqueTokenPersistenceAndControlHeaders(t *testing.T) {
	dir := t.TempDir()
	ts1 := NewToolSet(nil, "work#1")
	ts1.SetTaskID(77)
	ts1.SetTaskRoot(dir)

	tok := ts1.mintOASTToken("ssrf-admin")
	if len(tok) != 10 {
		t.Fatalf("unexpected token length %q", tok)
	}
	if strings.Contains(tok, "77") || strings.Contains(tok, "ssrf") || strings.Contains(tok, "cb") {
		t.Fatalf("opaque token contains forbidden marker: %q", tok)
	}

	// Simulate another worker / restart loading only from shared taskRoot disk state.
	oastTokenMu.Lock()
	clear(oastTaskTokens)
	oastTokenMu.Unlock()

	ts2 := NewToolSet(nil, "work#2")
	ts2.SetTaskID(77)
	ts2.SetTaskRoot(dir)
	tokens := ts2.lookupOASTTokens()
	if tokens[tok] != "ssrf-admin" {
		t.Fatalf("expected persisted token->label 'ssrf-admin', got %+v", tokens)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared", "oast_tokens.json")); err != nil {
		t.Fatalf("missing oast_tokens.json: %v", err)
	}

	// Matching boundaries for opaque tokens + legacy cb fallback.
	if ok, lbl := oastMatchesRequest(oastRequest{Path: "/" + tok}, "cb77", tokens); !ok || lbl != "ssrf-admin" {
		t.Fatalf("expected opaque path hit with label 'ssrf-admin', got ok=%v lbl=%q", ok, lbl)
	}
	if ok, _ := oastMatchesRequest(oastRequest{Path: "/x" + tok}, "cb77", tokens); ok {
		t.Fatal("opaque token must require non-alphanumeric boundary")
	}
	if ok, lbl := oastMatchesRequest(oastRequest{Domain: tok + ".sub.requestrepo.com"}, "cb77", tokens); !ok || lbl != "ssrf-admin" {
		t.Fatalf("expected opaque DNS subdomain hit, got ok=%v lbl=%q", ok, lbl)
	}
	if ok, _ := oastMatchesRequest(oastRequest{Path: "/cb77-xxe"}, "cb77", tokens); !ok {
		t.Fatal("expected legacy cb<taskID> marker fallback match")
	}

	// Control plane HTTP request must use coherent browser UA (not Go-http-client).
	var gotUA, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"requests":[]}`))
	}))
	defer srv.Close()
	oldBase := OASTBaseURL
	OASTBaseURL = srv.URL
	t.Cleanup(func() { OASTBaseURL = oldBase })

	if _, err := fetchOASTRequests(context.Background(), "tok-secret"); err != nil {
		t.Fatalf("fetchOASTRequests: %v", err)
	}
	if gotUA == "" || strings.Contains(gotUA, "Go-http-client") || strings.Contains(strings.ToLower(gotUA), "blade") {
		t.Fatalf("control plane UA exposed Go default or platform tag: %q", gotUA)
	}
	if gotAuth != "Bearer tok-secret" {
		t.Fatalf("missing auth header: %q", gotAuth)
	}
}
