package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// TestIntelSearchReportsLedgerWriteFailure 钉住「配额台账写失败不能静默」。
//
// 台账（intel_queries）同时是**配额计数器**——intelUsageToday 读它算今日用量。所以写失败
// 不只是少一条审计记录：这次调用没被计数 → 下一次读到的 used 偏低 → 平台侧每日上限可以被
// 悄悄超出。此前两处调用都是 `_ = RecordIntelQuery(...)`，失败连日志都没有。
//
// 用例走的是真实工具路径（假 FOFA 上游，与 accP21 同一套做法），只在最后一步把台账表改名
// 逼出写失败——比"关掉连接池"精准：关了池子连设置都读不出来，工具会在「没配 key」那步就返回，
// 根本走不到台账。
func TestIntelSearchReportsLedgerWriteFailure(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search/all" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"error":false,"size":1,"page":1,"results":[
			["http://a.example.com","1.2.3.4","80","http","example.com","",""]
		]}`))
	}))
	defer srv.Close()

	// 设置是全局的：改前记下、跑完还原（共享测试库不留残留）。defer 顺序：这里注册的
	// 还原函数晚于 defer m.Close()，因而先执行——连接池还活着（同 auth_test.go 的说明）。
	prevKey, hadKey, _ := m.pg.GetSetting("fofa_key")
	prevURL, hadURL, _ := m.pg.GetSetting("fofa_base_url")
	defer func() {
		if hadKey {
			_ = m.pg.SetSetting("fofa_key", prevKey)
		} else {
			_ = m.pg.SetSetting("fofa_key", "")
		}
		if hadURL {
			_ = m.pg.SetSetting("fofa_base_url", prevURL)
		} else {
			_ = m.pg.SetSetting("fofa_base_url", "")
		}
	}()
	if err := m.pg.SetSetting("fofa_key", "test-key"); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting("fofa_base_url", srv.URL); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	var tool actool.CoreTool
	for _, tl := range s.intelTools() {
		if tl.Name() == "intel_search" {
			tool = tl
		}
	}
	if tool == nil {
		t.Fatal("intel_search 工具未注册")
	}

	if _, err := m.pg.Exec(`ALTER TABLE intel_queries RENAME TO intel_queries_bak`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.pg.Exec(`ALTER TABLE intel_queries_bak RENAME TO intel_queries`) }()

	res, err := tool.Call(context.Background(), json.RawMessage(`{"expr":"domain=\"example.com\""}`), nil)
	if err != nil {
		t.Fatalf("工具调用出错: %v", err)
	}
	text := res.Flatten()
	if !strings.Contains(text, "未能记入台账") {
		t.Fatalf("台账写失败必须出现在回执里——静默失效的配额闸等于没有闸：\n回执: %s", text)
	}
}
