package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSettingsProxyPoolRoundTrip 钉住设置接口的代理池读写：多行候选持久化、
// socks5h 原样保留、调度策略与多健康检查 URL 回显，以及热更新与清空回退直连。
func TestSettingsProxyPoolRoundTrip(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	w := request("PUT", "/api/settings", `{
		"global_proxy_pool":"http://127.0.0.1:7890\nsocks5h://user:secret@127.0.0.1:7891",
		"proxy_health_url":"https://health.example/204\nhttps://backup.example/204",
		"proxy_health_timeout_seconds":2,
		"proxy_fail_threshold":3,
		"proxy_strategy":"fastest"
	}`)
	if w.Code != 200 {
		t.Fatalf("保存代理池失败：%d %s", w.Code, w.Body)
	}
	var saved map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	pool, _ := saved["global_proxy_pool"].(string)
	if !strings.Contains(pool, "http://127.0.0.1:7890") || !strings.Contains(pool, "socks5h://user:secret@127.0.0.1:7891") {
		t.Fatalf("代理池没有按行保留 socks5h：%q", pool)
	}
	if saved["proxy_health_timeout_seconds"] != float64(2) || saved["proxy_fail_threshold"] != float64(3) {
		t.Fatalf("健康参数没有保存：timeout=%v threshold=%v",
			saved["proxy_health_timeout_seconds"], saved["proxy_fail_threshold"])
	}
	if saved["proxy_strategy"] != "fastest" {
		t.Fatalf("策略没有保存：%v", saved["proxy_strategy"])
	}
	if saved["global_proxy"] != "http://127.0.0.1:7890" {
		t.Fatalf("兼容字段 global_proxy 应等于第一项：%v", saved["global_proxy"])
	}
	if active, _ := saved["proxy_active"].(string); strings.Contains(active, "secret") {
		t.Fatalf("proxy_active 不应泄露凭据：%q", active)
	}

	// GET 也必须回显同一份配置（页面刷新后不会丢）。
	w = request("GET", "/api/settings", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "socks5h://") || !strings.Contains(w.Body.String(), "backup.example") {
		t.Fatalf("GET 回显代理池失败：%d %s", w.Code, w.Body)
	}

	// 清空 = 显式直连；新的 pool 不应再挂着旧候选。
	w = request("PUT", "/api/settings", `{"global_proxy_pool":""}`)
	if w.Code != 200 || strings.TrimSpace(s.m.GlobalProxy()) != "" {
		t.Fatalf("清空代理池失败：%d %s", w.Code, w.Body)
	}
}

func TestSettingsProxyProbePinAndEgressRedaction(t *testing.T) {
	p1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer p1.Close()
	p2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer p2.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer target.Close()

	// Insert credentials in p2 to verify redaction across probe / pin / egress status.
	p2WithCred := strings.Replace(p2.URL, "http://", "http://alice:s3cr3t@", 1)

	s, _, request := trafficEvidenceServer(t)
	w := request("PUT", "/api/settings", `{
		"global_proxy_pool":"`+p1.URL+`\n`+p2WithCred+`",
		"proxy_health_url":"`+target.URL+`",
		"proxy_strategy":"ordered"
	}`)
	if w.Code != 200 {
		t.Fatalf("save proxy pool failed: %d %s", w.Code, w.Body)
	}
	defer request("PUT", "/api/settings", `{"global_proxy_pool":""}`)

	// Probe endpoint triggers concurrent check and returns redacted status.
	pw := request("POST", "/api/settings/proxy/probe", `{}`)
	if pw.Code != 200 {
		t.Fatalf("proxy/probe failed: %d %s", pw.Code, pw.Body)
	}
	if strings.Contains(pw.Body.String(), "s3cr3t") {
		t.Fatalf("proxy/probe leaked credentials: %s", pw.Body.String())
	}

	// Pin p2 cleanly (accepting redacted URL too).
	pinW := request("POST", "/api/settings/proxy/pin", `{"proxy":"`+p2.URL+`"}`)
	if pinW.Code != 200 {
		t.Fatalf("proxy/pin p2 failed: %d %s", pinW.Code, pinW.Body)
	}
	if !strings.Contains(pinW.Body.String(), `"pinned":"`+p2.URL+`"`) {
		t.Fatalf("expected pinned=%q in response: %s", p2.URL, pinW.Body.String())
	}

	// Unreachable pin is rejected cleanly.
	badPin := request("POST", "/api/settings/proxy/pin", `{"proxy":"http://127.0.0.1:9"}`)
	if badPin.Code != 400 {
		t.Fatalf("expected 400 pinning unknown/bad proxy, got %d %s", badPin.Code, badPin.Body)
	}

	// Unpin restores normal status.
	unpinW := request("POST", "/api/settings/proxy/pin", `{"proxy":""}`)
	if unpinW.Code != 200 || strings.Contains(unpinW.Body.String(), `"pinned"`) {
		t.Fatalf("expected unpin success, got %d %s", unpinW.Code, unpinW.Body)
	}

	// GET /api/egress/status reports strategy and no secret leak.
	egW := request("GET", "/api/egress/status", "")
	if egW.Code != 200 || strings.Contains(egW.Body.String(), "s3cr3t") || !strings.Contains(egW.Body.String(), `"strategy":"ordered"`) {
		t.Fatalf("egress/status check failed: %d %s", egW.Code, egW.Body)
	}
	_ = s
}
