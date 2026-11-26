import {
  backendLabel,
  describeChannelBackend,
  describeSearxngState,
  describeWebSearchProxyMode,
  formatWebSearchStats,
  formatWebSearchTestToast,
  SEARXNG_ENGINE_PRESETS,
  SEARXNG_PYPI_PRESETS,
  validateSearxngForm,
  WEB_SEARCH_BACKEND_OPTIONS,
} from "./search-helpers.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("WEB_SEARCH_BACKEND_OPTIONS exposes all 8 backends including searxng", () => {
  const values = WEB_SEARCH_BACKEND_OPTIONS.map((o) => o.value);
  assert.deepEqual(values, [
    "ddgs",
    "brave-html",
    "searxng",
    "brave-free",
    "tavily",
    "deepseek",
    "opencode",
    "commandcode",
  ]);
  assert.equal(backendLabel("searxng"), "SearXNG");
  assert.equal(backendLabel(""), "DuckDuckGo (ddgs)");
});

test("describeChannelBackend distinguishes commandcode from deepseek and opencode", () => {
  const cc = describeChannelBackend("commandcode");
  assert.ok(cc);
  assert.equal(cc.title.includes("/alpha/web-search"), true);
  assert.equal(cc.detail.includes("/alpha/web-search"), true);
  assert.equal(cc.detail.includes("DeepSeek 服务端"), false);
  assert.equal(cc.detail.includes("只有标题"), false);

  const ds = describeChannelBackend("deepseek");
  assert.ok(ds);
  assert.equal(ds.detail.includes("format = anthropic"), true);
  assert.ok(ds.warning?.includes("OpenAI"));

  const oc = describeChannelBackend("opencode");
  assert.ok(oc);
  assert.equal(oc.detail.includes("opencode.ai"), true);

  assert.equal(describeChannelBackend("searxng"), null);
});

test("describeWebSearchProxyMode covers custom, egress_pool, proxy_blocked, and direct", () => {
  const egress = describeWebSearchProxyMode("egress_pool", {
    activeProxy: "http://127.0.0.1:7890",
  });
  assert.equal(egress.badge, "跟随平台出口代理池");
  assert.equal(egress.tone, "ok");
  assert.equal(egress.detail.includes("http://127.0.0.1:7890"), true);

  const blocked = describeWebSearchProxyMode("proxy_blocked");
  assert.equal(blocked.tone, "warn");
  assert.equal(blocked.detail.includes("不会静默直连"), true);

  const custom = describeWebSearchProxyMode("custom");
  assert.equal(custom.badge, "独立搜索代理");

  const direct = describeWebSearchProxyMode("direct");
  assert.equal(direct.badge, "本机直连");
});

test("formatWebSearchTestToast includes fallback_from, fallback_reason, proxy_mode, and elapsed_ms", () => {
  const okToast = formatWebSearchTestToast({
    ok: true,
    count: 4,
    backend: "brave-html",
    fallback_from: "deepseek",
    fallback_reason: "Anthropic web_search returned empty",
    proxy_mode: "egress_pool",
    elapsed_ms: 812,
  });
  assert.equal(okToast.includes("Brave HTML 返回 4 条"), true);
  assert.equal(okToast.includes("跟随平台出口代理池"), true);
  assert.equal(okToast.includes("812ms"), true);
  assert.equal(okToast.includes("从 DeepSeek 兜底（Anthropic web_search returned empty）"), true);

  const errToast = formatWebSearchTestToast({
    ok: false,
    backend: "deepseek",
    error: "DeepSeek 联网需要 format = anthropic",
    proxy_mode: "direct",
    elapsed_ms: 5,
  });
  assert.equal(errToast.includes("搜索测试失败（DeepSeek）：DeepSeek 联网需要 format = anthropic"), true);
  assert.equal(errToast.includes("本机直连"), true);
});

test("formatWebSearchStats handles empty and populated snapshots", () => {
  assert.equal(formatWebSearchStats(null), null);
  const formatted = formatWebSearchStats({
    requests: 15,
    cache_hits: 4,
    primary_ok: 12,
    fallback_ok: 2,
    failures: 1,
    last_backend: "brave-html",
    last_fallback_from: "ddgs",
    last_error: "HTTP 429",
    last_at: "2026-11-26T05:20:00Z",
  });
  assert.ok(formatted);
  assert.equal(formatted.summary, "累计请求 15 次 · 主通道 12 · 兜底 2 · 缓存命中 4 · 失败 1");
  assert.equal(formatted.lastLine?.includes("最近通道: Brave HTML"), true);
  assert.equal(formatted.lastLine?.includes("触发兜底自: DuckDuckGo (ddgs)"), true);
  assert.equal(formatted.lastLine?.includes("HTTP 429"), true);
});

test("describeSearxngState and validateSearxngForm handle lifecycle and validation cleanly", () => {
  assert.equal(SEARXNG_PYPI_PRESETS.length >= 3, true);
  assert.equal(SEARXNG_ENGINE_PRESETS.length >= 3, true);

  const running = describeSearxngState({
    mode: "host_venv",
    state: "running",
    installed: true,
    running: true,
    healthy: true,
    pid: 1234,
    url: "http://127.0.0.1:8888",
    active_in_search: true,
    config: {
      enabled: true,
      port: 8888,
      proxy_mode: "inherit",
      custom_proxy: "",
      engines_preset: "security",
      repo_url: "https://github.com/searxng/searxng.git",
      pypi_index: "",
      install_proxy: "",
      auto_switch: true,
    },
  });
  assert.equal(running.tone, "ok");
  assert.equal(running.badge.includes("8888"), true);

  const composeBad = describeSearxngState({
    mode: "compose",
    state: "error",
    installed: true,
    running: true,
    healthy: false,
    url: "http://searxng:8080",
    active_in_search: false,
    last_error: "HTTP 403",
    config: {
      enabled: false,
      port: 8888,
      proxy_mode: "inherit",
      custom_proxy: "",
      engines_preset: "security",
      repo_url: "",
      pypi_index: "",
      install_proxy: "",
      auto_switch: true,
    },
  });
  assert.equal(composeBad.tone, "warn");

  const baseCfg = {
    enabled: false,
    port: 8888,
    proxy_mode: "custom",
    custom_proxy: "",
    engines_preset: "security",
    repo_url: "https://github.com/searxng/searxng.git",
    pypi_index: "https://pypi.tuna.tsinghua.edu.cn/simple",
    install_proxy: "",
    auto_switch: true,
  };
  assert.ok(validateSearxngForm("80", baseCfg)?.includes("1024"));
  assert.ok(validateSearxngForm("8888", baseCfg)?.includes("自定义代理"));
  assert.equal(validateSearxngForm("8888", { ...baseCfg, custom_proxy: "socks5h://127.0.0.1:7897" }), null);
});
