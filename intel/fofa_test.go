package intel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeFOFA 起一个假 FOFA：记录收到的查询参数，按脚本应答。
func fakeFOFA(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, calls *int)) (*httptest.Server, *int) {
	t.Helper()
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		handler(w, r, calls)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func fofaWith(baseURL, key string) Provider {
	p, _ := New("fofa", Config{BaseURL: baseURL, Key: key, Timeout: 2e9})
	return p
}

func TestFOFASearchParsesRowsAndEncodesQuery(t *testing.T) {
	const expr = `domain="example.com" && protocol="http"`
	var gotQuery string
	srv, _ := fakeFOFA(t, func(w http.ResponseWriter, r *http.Request, _ *int) {
		if r.URL.Path != "/api/v1/search/all" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "k1" {
			t.Errorf("key 没带上: %s", r.URL.RawQuery)
		}
		// qbase64 必须是标准 base64，且能解回原表达式（FOFA 的硬要求）。
		raw, err := base64.StdEncoding.DecodeString(r.URL.Query().Get("qbase64"))
		if err != nil {
			t.Fatalf("qbase64 不是标准 base64: %v (raw=%q)", err, r.URL.Query().Get("qbase64"))
		}
		gotQuery = string(raw)
		if f := r.URL.Query().Get("fields"); !strings.Contains(f, "host") || !strings.Contains(f, "icp") {
			t.Errorf("fields 不对: %q", f)
		}
		if s := r.URL.Query().Get("size"); s != "2" {
			t.Errorf("size 应为 2，收到 %q", s)
		}
		// 列序必须与 fields 声明序一致：host,ip,port,domain,icp
		_, _ = w.Write([]byte(`{"error":false,"size":37,"page":1,"results":[
			["https://a.example.com:8443","1.2.3.4","8443","example.com","京ICP备1号"],
			["http://b.example.com","5.6.7.8","80","",""]
		]}`))
	})
	p := fofaWith(srv.URL, "k1")
	res, err := p.Search(context.Background(), Query{Expr: expr, Limit: 2,
		Fields: []string{"host", "ip", "port", "domain", "icp"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != expr {
		t.Fatalf("表达式不是原样送达: %q", gotQuery)
	}
	if res.Total != 37 || len(res.Assets) != 2 {
		t.Fatalf("结果不对: total=%d assets=%d", res.Total, len(res.Assets))
	}
	a := res.Assets[0]
	if a.Host != "https://a.example.com:8443" || a.IP != "1.2.3.4" || a.Port != 8443 ||
		a.Domain != "example.com" || a.ICP != "京ICP备1号" {
		t.Fatalf("首行映射不对: %+v", a)
	}
	if res.Assets[1].Domain != "" || res.Assets[1].ICP != "" {
		t.Fatalf("空字段不该被写入: %+v", res.Assets[1])
	}
	if res.Provider != "fofa" || res.Expr != expr || res.QueriedAt.IsZero() {
		t.Fatalf("结果元信息不全: %+v", res)
	}
}

func TestFOFASearchErrorsAreActionable(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       string
	}{
		{"key 无效", `{"error":true,"errmsg":"Invalid key"}`, 200, "API Key"},
		{"额度不足", `{"error":true,"errmsg":"The remaining fpoint is not enough"}`, 200, "额度不足"},
		{"语法错误", `{"error":true,"errmsg":"syntax error near ..."}`, 200, "语法"},
		{"其他业务错", `{"error":true,"errmsg":"something else"}`, 200, "FOFA 查询失败"},
		{"4xx 带业务正文", `{"error":true,"errmsg":"Invalid key"}`, 401, "API Key"},
		{"坏 JSON", `not-json`, 200, "不是合法 JSON"},
		{"5xx", `boom`, 500, "HTTP 500"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := fakeFOFA(t, func(w http.ResponseWriter, _ *http.Request, _ *int) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			_, err := fofaWith(srv.URL, "k").Search(context.Background(), Query{Expr: `domain="x.com"`})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误话术应含 %q，实际: %v", c.want, err)
			}
		})
	}
}

func TestFOFASearchEmptyAndLimits(t *testing.T) {
	var lastSize, lastFields string
	srv, _ := fakeFOFA(t, func(w http.ResponseWriter, r *http.Request, _ *int) {
		lastSize = r.URL.Query().Get("size")
		lastFields = r.URL.Query().Get("fields")
		_, _ = w.Write([]byte(`{"error":false,"size":0,"results":null}`))
	})
	p := fofaWith(srv.URL, "k")
	res, err := p.Search(context.Background(), Query{Expr: `domain="none.example"`})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 || len(res.Assets) != 0 {
		t.Fatalf("空结果应干净: %+v", res)
	}
	if lastSize != "20" { // 未给 limit → 默认 20
		t.Fatalf("默认 limit 应为 20，收到 %s", lastSize)
	}
	if !strings.Contains(lastFields, "title") { // 未给 fields → 默认字段集
		t.Fatalf("默认 fields 不对: %s", lastFields)
	}
	if _, err := p.Search(context.Background(), Query{Expr: "x", Limit: 9999}); err != nil {
		t.Fatal(err)
	}
	if lastSize != "100" { // 超上限 → 夹到 100
		t.Fatalf("limit 应夹到 100，收到 %s", lastSize)
	}
}

func TestFOFASearchRequiresKeyAndExpr(t *testing.T) {
	srv, calls := fakeFOFA(t, func(w http.ResponseWriter, _ *http.Request, _ *int) {
		t.Error("未配 key 时不该发起外呼")
		_, _ = w.Write([]byte(`{"error":false,"size":0}`))
	})
	if _, err := fofaWith(srv.URL, "").Search(context.Background(), Query{Expr: "x"}); err == nil ||
		!strings.Contains(err.Error(), "未配置 FOFA API Key") {
		t.Fatalf("缺 key 应给可读错误: %v", err)
	}
	if _, err := fofaWith(srv.URL, "k").Search(context.Background(), Query{Expr: "   "}); err == nil ||
		!strings.Contains(err.Error(), "表达式不能为空") {
		t.Fatalf("空表达式应给可读错误: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("本地校验失败时不该外呼，实际 %d 次", *calls)
	}
}

func TestFOFAInfo(t *testing.T) {
	srv, _ := fakeFOFA(t, func(w http.ResponseWriter, r *http.Request, _ *int) {
		if r.URL.Path != "/api/v1/info/my" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"error":false,"email":"a@b.c","remain_free_point":1234,"isvip":true}`))
	})
	info, err := fofaWith(srv.URL, "k").Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info["email"] != "a@b.c" || info["remain_free_point"].(float64) != 1234 {
		t.Fatalf("info 解析不对: %+v", info)
	}
	srvErr, _ := fakeFOFA(t, func(w http.ResponseWriter, _ *http.Request, _ *int) {
		_, _ = w.Write([]byte(`{"error":true,"errmsg":"Invalid key"}`))
	})
	if _, err := fofaWith(srvErr.URL, "k").Info(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "API Key") {
		t.Fatalf("info 业务错误应分类: %v", err)
	}
}

func TestRegistryAndUnknownProvider(t *testing.T) {
	if names := Names(); len(names) != 1 || names[0] != "fofa" {
		t.Fatalf("注册表不对: %v", names)
	}
	if _, err := New("quake", Config{}); err == nil || !strings.Contains(err.Error(), "未知情报源") {
		t.Fatalf("未知 provider 应给可读错误: %v", err)
	}
	if _, err := New("FOFA", Config{}); err != nil {
		t.Fatalf("provider 名应大小写不敏感: %v", err)
	}
}

// 代理必须显式生效（空 = 直连，不回退 HTTP_PROXY 环境变量）——与 LLM/搜索同一条纪律。
func TestFOFAProxyHandling(t *testing.T) {
	f := &fofa{cfg: Config{BaseURL: "https://fofa.info"}}
	if _, err := f.client(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&fofa{cfg: Config{Proxy: "socks5://127.0.0.1:1080"}}).client(); err != nil {
		t.Fatalf("socks5 代理应可用: %v", err)
	}
	if _, err := (&fofa{cfg: Config{Proxy: "127.0.0.1:1080"}}).client(); err == nil {
		t.Fatal("缺 scheme 的代理必须报错（不能悄悄直连）")
	}
	if _, err := (&fofa{cfg: Config{Proxy: "ftp://x"}}).client(); err == nil {
		t.Fatal("不支持的代理协议必须报错")
	}
}

// 结果里出现未知字段时进 Extra 原样保留（provider 加字段不该让平台丢数据）。
func TestAssetExtraFields(t *testing.T) {
	a := assetFromRow([]string{"host", "cert", "port"}, []string{"h", "CN=example", "not-a-port"})
	if a.Extra["cert"] != "CN=example" || a.Extra["port"] != "not-a-port" {
		t.Fatalf("未知字段应进 Extra: %+v", a)
	}
	if a.Port != 0 {
		t.Fatalf("非法端口不该被当成数字: %+v", a)
	}
	if a.Display() != "h" {
		t.Fatalf("Display 优先 host: %s", a.Display())
	}
	if (Asset{IP: "1.2.3.4", Port: 80}).Display() != "1.2.3.4:80" {
		t.Fatal("无 host 时 Display 应回退 ip:port")
	}
	// jsonResult 里 Extra 要能正常序列化
	if _, err := json.Marshal(a); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizeFofaRespTolerant 钉住「第三方中转站/自建」的常见结构差异：
// 官方形态（顶层 results + 数组行 + size）必须原样可用，其余变体收敛到同一份归一结果。
// 解析不出来要**报错**，不是返回空结果——把"结构不认识"当成"没查到"，人会以为目标不存在。
func TestNormalizeFofaRespTolerant(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantErr    string
		wantSize   int
		wantFields []string
		wantRows   [][]string
	}{
		{
			name:       "官方：顶层 results + 数组行 + size",
			body:       `{"error":false,"size":42,"page":1,"fields":["host","ip","port"],"results":[["a.com","1.2.3.4","443"]]}`,
			wantSize:   42,
			wantFields: []string{"host", "ip", "port"},
			wantRows:   [][]string{{"a.com", "1.2.3.4", "443"}},
		},
		{
			name:       "中转站：结果在 data.results、计数叫 total",
			body:       `{"code":0,"data":{"total":7,"results":[["b.com","5.6.7.8","80"]]}}`,
			wantSize:   7,
			wantFields: nil, // 应答没给 fields：调用方回落到自己请求的列序
			wantRows:   [][]string{{"b.com", "5.6.7.8", "80"}},
		},
		{
			name:       "中转站：结果在 result.results、行是对象（键序稳定）",
			body:       `{"success":true,"result":{"results":[{"port":8080,"host":"c.com","ip":"9.9.9.9"}],"size":1}}`,
			wantSize:   1,
			wantFields: []string{"host", "ip", "port"},
			wantRows:   [][]string{{"c.com", "9.9.9.9", "8080"}},
		},
		{
			name:       "计数在根上、数字是字符串",
			body:       `{"total":"12","results":[["d.com"]]}`,
			wantSize:   12,
			wantRows:   [][]string{{"d.com"}},
			wantFields: nil,
		},
		{
			name:    "官方式错误：error=true + errmsg",
			body:    `{"error":true,"errmsg":"[-700] 账号无效"}`,
			wantErr: "账号无效",
		},
		{
			name:    "中转站式错误：code!=0 + message",
			body:    `{"code":401,"message":"key 已过期"}`,
			wantErr: "key 已过期",
		},
		{
			name:    "结构不认识：不是 results 数组 → 报错（不是空结果）",
			body:    `{"hits":{"list":[]}}`,
			wantErr: "找不到 results 数组",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeFofaResp([]byte(c.body))
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("应当报错")
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("错误应含 %q，实际 %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错：%v", err)
			}
			if got.Size != c.wantSize {
				t.Fatalf("size 应为 %d，实际 %d", c.wantSize, got.Size)
			}
			if len(c.wantFields) > 0 {
				if strings.Join(got.Fields, ",") != strings.Join(c.wantFields, ",") {
					t.Fatalf("fields 应为 %v，实际 %v", c.wantFields, got.Fields)
				}
			} else if len(got.Fields) != 0 {
				t.Fatalf("应答没给 fields 时不该凭空造列：%v", got.Fields)
			}
			if len(got.Rows) != len(c.wantRows) {
				t.Fatalf("行数应为 %d，实际 %d", len(c.wantRows), len(got.Rows))
			}
			for i := range c.wantRows {
				if strings.Join(got.Rows[i], ",") != strings.Join(c.wantRows[i], ",") {
					t.Fatalf("第 %d 行应为 %v，实际 %v", i, c.wantRows[i], got.Rows[i])
				}
			}
		})
	}
}

// TestFOFASearchUsesResponseFieldOrder 钉住列名以**应答给的为准**：
// 中转站少给/多给列时，用请求的列序硬套会把值错位到别的列上。
func TestFOFASearchUsesResponseFieldOrder(t *testing.T) {
	srv, _ := fakeFOFA(t, func(w http.ResponseWriter, r *http.Request, _ *int) {
		// 应答给的列序与请求不同（中转站常见）
		_, _ = w.Write([]byte(`{"error":false,"size":1,"fields":["ip","host"],"results":[["1.2.3.4","a.com"]]}`))
	})
	res, err := fofaWith(srv.URL, "k").Search(context.Background(), Query{Expr: "domain=\"a.com\"", Fields: []string{"host", "ip"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Fields, ",") != "ip,host" {
		t.Fatalf("列名应以应答为准：%v", res.Fields)
	}
	if len(res.Assets) != 1 || res.Assets[0].IP != "1.2.3.4" || !strings.Contains(res.Assets[0].Host, "a.com") {
		t.Fatalf("值应按应答列序对齐：%+v", res.Assets)
	}
}
