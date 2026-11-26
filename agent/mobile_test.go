package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

type stubMobileBridge struct {
	auditKinds  []string
	proxyAction string
	signCalls   int
}

func (s *stubMobileBridge) DeviceStatus(context.Context, int64) (string, error) { return "ok", nil }
func (s *stubMobileBridge) AppControl(context.Context, int64, string, string, string) (string, error) {
	return "app_ctrl_ok", nil
}
func (s *stubMobileBridge) ProxyControl(_ context.Context, _ int64, action, _ string) (string, error) {
	s.proxyAction = action
	return "proxy:" + action, nil
}
func (s *stubMobileBridge) UIExplore(context.Context, int64, json.RawMessage) (string, error) {
	return "ui_ok", nil
}
func (s *stubMobileBridge) FridaHook(context.Context, int64, string, string, string, string, []string, string) (string, error) {
	return "frida_ok", nil
}
func (s *stubMobileBridge) RPCSign(_ context.Context, _ int64, _ string, method string, args []any) (string, error) {
	s.signCalls++
	out, _ := json.Marshal(map[string]any{
		"headers": map[string]string{"X-RPC-Sig": method + "-signed"},
		"body":    `{"signed":true}`,
	})
	return string(out), nil
}
func (s *stubMobileBridge) IPCProbe(context.Context, int64, string, string, string, string, string, map[string]string) (string, error) {
	return "ipc_ok", nil
}
func (s *stubMobileBridge) Audit(_ context.Context, _ int64, kind string, _ json.RawMessage) (string, error) {
	s.auditKinds = append(s.auditKinds, kind)
	return "audit:" + kind, nil
}

func TestMobileAuditToolsAndHTTPRequestRPCSign(t *testing.T) {
	stub := &stubMobileBridge{}
	WithOverrides(Overrides{Mobile: stub}, func() {
		ts := NewToolSet(nil, t.TempDir())
		ctx := WithRunInfo(context.Background(), RunInfo{TaskID: 99})

		// 1. Audit dispatch across mobile_app_ctrl, mobile_ui_explore, mobile_ipc_probe, mobile_proxy_ctrl
		byName := map[string]actool.CoreTool{}
		for _, tl := range ts.WorkerTools() {
			byName[tl.Name()] = tl
		}
		if _, err := byName["mobile_app_ctrl"].Call(ctx, json.RawMessage(`{"action":"sandbox_audit"}`), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := byName["mobile_app_ctrl"].Call(ctx, json.RawMessage(`{"action":"logcat_audit"}`), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := byName["mobile_ui_explore"].Call(ctx, json.RawMessage(`{"action":"auto_crawl","max_steps":5}`), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := byName["mobile_ipc_probe"].Call(ctx, json.RawMessage(`{"kind":"auto_fuzz"}`), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := byName["mobile_proxy_ctrl"].Call(ctx, json.RawMessage(`{"action":"enable_transparent"}`), nil); err != nil {
			t.Fatal(err)
		}
		if len(stub.auditKinds) != 4 || stub.proxyAction != "enable_transparent" {
			t.Fatalf("unexpected audit/proxy dispatch: audits=%v proxy=%q", stub.auditKinds, stub.proxyAction)
		}

		// 2. http_request with mobile_sign_method signs headers & body cleanly before sending
		var gotSig, gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotSig = r.Header.Get("X-RPC-Sig")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()

		res, err := byName["http_request"].Call(ctx, json.RawMessage(`{"url":"`+srv.URL+`/api/pay","method":"POST","body_json":"{\"raw\":1}","mobile_sign_method":"calcSign"}`), nil)
		if err != nil {
			t.Fatalf("http_request call error: %v", err)
		}
		if res.IsError || stub.signCalls != 1 || gotSig != "calcSign-signed" || !strings.Contains(gotBody, `"signed":true`) {
			t.Fatalf("http_request RPC sign mismatch: resErr=%v signCalls=%d sig=%q body=%q", res.IsError, stub.signCalls, gotSig, gotBody)
		}
	})
}
