package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSettingsProxyPoolRoundTrip 钉住设置接口的代理池读写：多行候选持久化、
// socks5h 归一成 socks5、健康参数回显，以及"保存即生效"（pool 被重建）。
func TestSettingsProxyPoolRoundTrip(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	w := request("PUT", "/api/settings", `{
		"global_proxy_pool":"http://127.0.0.1:7890\nsocks5h://127.0.0.1:7891",
		"proxy_health_url":"https://health.example/204",
		"proxy_health_timeout_seconds":2,
		"proxy_fail_threshold":3
	}`)
	if w.Code != 200 {
		t.Fatalf("保存代理池失败：%d %s", w.Code, w.Body)
	}
	var saved map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	pool, _ := saved["global_proxy_pool"].(string)
	if !strings.Contains(pool, "http://127.0.0.1:7890") || !strings.Contains(pool, "socks5://127.0.0.1:7891") {
		t.Fatalf("代理池没有按行保存/归一：%q", pool)
	}
	if saved["proxy_health_timeout_seconds"] != float64(2) || saved["proxy_fail_threshold"] != float64(3) {
		t.Fatalf("健康参数没有保存：timeout=%v threshold=%v",
			saved["proxy_health_timeout_seconds"], saved["proxy_fail_threshold"])
	}
	if saved["global_proxy"] != "http://127.0.0.1:7890" {
		t.Fatalf("兼容字段 global_proxy 应等于第一项：%v", saved["global_proxy"])
	}

	// GET 也必须回显同一份配置（页面刷新后不会丢）。
	w = request("GET", "/api/settings", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "socks5://127.0.0.1:7891") {
		t.Fatalf("GET 回显代理池失败：%d %s", w.Code, w.Body)
	}

	// 清空 = 显式直连；新的 pool 不应再挂着旧候选。
	w = request("PUT", "/api/settings", `{"global_proxy_pool":""}`)
	if w.Code != 200 || strings.TrimSpace(s.m.GlobalProxy()) != "" {
		t.Fatalf("清空代理池失败：%d %s", w.Code, w.Body)
	}
}
