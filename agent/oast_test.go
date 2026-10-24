package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// 回调地址要带任务标记与 label：同一子域下多个任务共用一个仓库，只有标记分得清。
func TestOASTMintBuildsMarkedURL(t *testing.T) {
	withOAST(t, fakeOASTToken("ok9iwmcq"), "requestrepo.com", true)
	tools, _, _ := newToolWorker(t)
	out := callTool(t, tools.PlannerTools(), "oast_mint", `{"label":"ssrf_admin"}`)
	want := fmt.Sprintf("http://ok9iwmcq.requestrepo.com/blade%d-ssrf-admin", tools.taskID)
	if !strings.Contains(out, want) {
		t.Fatalf("回调地址应带任务标记与归一化后的 label：want %q got %s", want, out)
	}
	if !strings.Contains(out, "oast_poll") {
		t.Fatalf("回执应告诉模型命中后怎么取：%s", out)
	}
	if out := callTool(t, tools.PlannerTools(), "oast_mint", `{}`); !strings.Contains(out, fmt.Sprintf("/blade%d", tools.taskID)) {
		t.Fatalf("不带 label 时也要带任务标记：%s", out)
	}
}

// 轮询只登记**新命中**：同一次回调不重复记事实，别的任务的回调不混进来。
func TestOASTPollRecordsNewHitsOnce(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	marker := tools.oastMarker()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+fakeOASTToken("ok9iwmcq") {
			t.Errorf("应带 Bearer token：%q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": []map[string]any{
			{"_id": "hit-1", "type": "http", "method": "GET", "path": "/" + marker + "-ssrf", "ip": "10.0.0.9", "country": "CN", "date": 1792815288},
			{"_id": "other-1", "type": "http", "method": "GET", "path": "/blade999-other", "ip": "10.0.0.10", "country": "CN"},
			{"_id": "dns-1", "type": "dns", "domain": marker + ".ok9iwmcq.requestrepo.com", "ip": "10.0.0.11", "country": "CN"},
		}})
	}))
	t.Cleanup(srv.Close)
	oldBase := OASTBaseURL
	OASTBaseURL = srv.URL
	t.Cleanup(func() { OASTBaseURL = oldBase })
	withOAST(t, fakeOASTToken("ok9iwmcq"), "requestrepo.com", true)

	out := callTool(t, tools.PlannerTools(), "oast_poll", `{}`)
	if !strings.Contains(out, "callbacks: 2") {
		t.Fatalf("应记录 2 条本任务命中（HTTP+DNS），别的任务不算：%s", out)
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
	if len(got) != 2 {
		t.Fatalf("应落 2 条 OAST 事实：%d", len(got))
	}
	if got[0]["confidence"] != "observed" {
		t.Fatalf("事实应带 observed 置信度：%+v", got[0])
	}
	// 再轮询一次：同样的记录不该重复登记。
	if again := callTool(t, tools.PlannerTools(), "oast_poll", `{}`); !strings.Contains(again, "no new callbacks") {
		t.Fatalf("重复轮询应提示无新命中：%s", again)
	}
	if again := oastFacts(); len(again) != 2 {
		t.Fatalf("不该重复记事实：%d", len(again))
	}
}

// 没配置时两个工具都要明确拒绝（server 侧还会把它们从工具面移除）。
func TestOASTToolsRequireConfig(t *testing.T) {
	withOAST(t, "", "", false)
	tools, _, _ := newToolWorker(t)
	if out := callTool(t, tools.PlannerTools(), "oast_mint", `{}`); !strings.Contains(out, "未配置 OAST") {
		t.Fatalf("mint 应拒绝：%s", out)
	}
	if out := callTool(t, tools.PlannerTools(), "oast_poll", `{}`); !strings.Contains(out, "未配置 OAST") {
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
		out := callTool(t, tools.PlannerTools(), tool, `{}`)
		if strings.Contains(out, token) {
			t.Fatalf("%s 的回执泄露了 token：%s", tool, out)
		}
	}
}
