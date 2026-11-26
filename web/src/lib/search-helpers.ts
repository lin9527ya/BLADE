import type { WebSearchProxyMode, WebSearchStats, WebSearchTestResult } from "./types";

export interface WebSearchBackendOption {
  value: string;
  label: string;
  shortLabel: string;
}

export interface WebSearchChannelInfo {
  title: string;
  detail: string;
  warning?: string;
}

export interface WebSearchProxyInfo {
  mode: WebSearchProxyMode | "direct";
  badge: string;
  tone: "ok" | "warn" | "info";
  detail: string;
}

export const WEB_SEARCH_BACKEND_OPTIONS: readonly WebSearchBackendOption[] = [
  {
    value: "ddgs",
    label: "DuckDuckGo 网页（免费、无需 Key）",
    shortLabel: "DuckDuckGo (ddgs)",
  },
  {
    value: "brave-html",
    label: "Brave 网页抓取（免费、无需 Key）",
    shortLabel: "Brave HTML",
  },
  {
    value: "searxng",
    label: "自建 SearXNG 实例（免费、需 URL）",
    shortLabel: "SearXNG",
  },
  {
    value: "brave-free",
    label: "Brave Search API（免费额度 2,000 次/月，需 Key）",
    shortLabel: "Brave API",
  },
  {
    value: "tavily",
    label: "Tavily Search API（付费/额度制，需 Key）",
    shortLabel: "Tavily",
  },
  {
    value: "deepseek",
    label: "DeepSeek 原生联网（复用平台 Anthropic 配置，需支持联网的模型）",
    shortLabel: "DeepSeek",
  },
  {
    value: "opencode",
    label: "OpenCode Zen 原生联网（复用当前/opencode 平台 Anthropic 配置）",
    shortLabel: "OpenCode",
  },
  {
    value: "commandcode",
    label: "Command Code 原生联网（/alpha/web-search 独立通道，不占模型调用）",
    shortLabel: "Command Code",
  },
];

const BACKEND_LABEL_MAP = new Map(WEB_SEARCH_BACKEND_OPTIONS.map((o) => [o.value, o.shortLabel]));

/** Returns a concise human-readable label for a web search backend identifier. */
export function backendLabel(backend: string | undefined): string {
  const key = (backend ?? "").trim();
  if (!key) return "DuckDuckGo (ddgs)";
  return BACKEND_LABEL_MAP.get(key) ?? key;
}

const CHANNEL_BACKEND_INFO = new Map<string, WebSearchChannelInfo>([
  [
    "commandcode",
    {
      title: "使用平台「commandcode」配置（/alpha/web-search 独立通道）",
      detail:
        "系统会自动从当前激活或同名「commandcode」平台配置读取 Base URL 与 API Key，直接调用 /alpha/web-search 搜索接口，不触发额外 LLM 推理、不额外消耗对话 token，返回标题、链接与摘要。",
    },
  ],
  [
    "deepseek",
    {
      title: "使用平台「deepseek」配置（Anthropic 协议服务端联网）",
      detail:
        "需使用 Anthropic 协议（format = anthropic）及支持 web_search_20250305 工具的端点与模型（例如官方 /anthropic），按普通对话计入一次模型调用与 token，由 DeepSeek 服务端执行搜索；返回结果主要包含标题和链接。",
      warning:
        "若 DeepSeek 平台配置为 OpenAI / Responses 协议，测试将直接报错；Agent 运行时会自动兜底到 Brave/DDG 以避免搜索中断。",
    },
  ],
  [
    "opencode",
    {
      title: "使用平台「opencode」配置（Anthropic 兼容服务端联网）",
      detail:
        "系统会自动从当前激活或同名「opencode」平台配置读取端点与 Key。若为自建 Anthropic 兼容后端将直连执行服务端联网；若指向 opencode.ai 公共网关（暂不支持 web_search_20250305），会自动回落到 DuckDuckGo（并通过平台出口代理访问）。",
    },
  ],
]);

/** Returns channel-specific guidance for LLM/platform-integrated search backends. */
export function describeChannelBackend(backend: string): WebSearchChannelInfo | null {
  return CHANNEL_BACKEND_INFO.get(backend.trim()) ?? null;
}

/** Maps runtime web_search_proxy_mode into a UI badge label, tone, and explanation. */
export function describeWebSearchProxyMode(
  mode: string | undefined,
  opts?: { activeProxy?: string },
): WebSearchProxyInfo {
  const active = opts?.activeProxy?.trim();
  switch (mode) {
    case "custom":
      return {
        mode: "custom",
        badge: "独立搜索代理",
        tone: "info",
        detail: "所有搜索请求（含直连型后端与失败兜底抓取）统一经由上方配置的独立搜索代理转发。",
      };
    case "egress_pool":
      return {
        mode: "egress_pool",
        badge: "跟随平台出口代理池",
        tone: "ok",
        detail: active
          ? `ddgs / brave-html 及失败兜底抓取自动通过当前活跃出口代理 ${active} 访问。`
          : "ddgs / brave-html 及失败兜底抓取自动跟随平台全局出口代理池访问。",
      };
    case "proxy_blocked":
      return {
        mode: "proxy_blocked",
        badge: "出口代理池已阻断",
        tone: "warn",
        detail:
          "独立搜索代理为空且全局出口代理池当前全部不可用，ddgs / brave-html 不会静默直连。请恢复代理池、填写独立搜索代理，或改用 Tavily / Brave API / SearXNG / Command Code。",
      };
    default:
      return {
        mode: "direct",
        badge: "本机直连",
        tone: "info",
        detail:
          "独立搜索代理与平台出口代理池均未启用，搜索请求将由本机直接发出（若本机直连 DDG/Brave 受阻，可填写独立搜索代理、启用全局出口代理或接入自建 SearXNG）。",
      };
  }
}

/** Formats test probe feedback into a compact single-line toast summary. */
export function formatWebSearchTestToast(res: WebSearchTestResult): string {
  const backend = backendLabel(res.backend);
  const proxyInfo = res.proxy_mode ? describeWebSearchProxyMode(res.proxy_mode).badge : "";
  const latency = typeof res.elapsed_ms === "number" ? `${res.elapsed_ms}ms` : "";
  const fallback = res.fallback_from
    ? `从 ${backendLabel(res.fallback_from)} 兜底${res.fallback_reason ? `（${res.fallback_reason}）` : ""}`
    : "";

  const meta = [proxyInfo, latency, fallback].filter(Boolean).join(" · ");

  if (res.ok) {
    const base = `搜索测试成功：${backend} 返回 ${res.count ?? 0} 条`;
    return meta ? `${base}（${meta}）` : base;
  }
  const errText = res.error || "未知错误";
  const prefix = `搜索测试失败（${backend}）`;
  return meta ? `${prefix}：${errText} [${meta}]` : `${prefix}：${errText}`;
}

export interface FormattedWebSearchStats {
  requests: number;
  cacheHits: number;
  primaryOk: number;
  fallbackOk: number;
  failures: number;
  summary: string;
  lastLine: string | null;
}

/** Formats runtime WebSearchStatsSnapshot for display in the settings panel. */
export function formatWebSearchStats(stats: WebSearchStats | null | undefined): FormattedWebSearchStats | null {
  if (!stats) return null;
  const requests = stats.requests ?? 0;
  const cacheHits = stats.cache_hits ?? 0;
  const primaryOk = stats.primary_ok ?? 0;
  const fallbackOk = stats.fallback_ok ?? 0;
  const failures = stats.failures ?? 0;
  const summary = `累计请求 ${requests} 次 · 主通道 ${primaryOk} · 兜底 ${fallbackOk} · 缓存命中 ${cacheHits} · 失败 ${failures}`;

  const parts: string[] = [];
  if (stats.last_backend) {
    parts.push(`最近通道: ${backendLabel(stats.last_backend)}`);
  }
  if (stats.last_fallback_from) {
    parts.push(`触发兜底自: ${backendLabel(stats.last_fallback_from)}`);
  }
  if (stats.last_error) {
    parts.push(`最近错误: ${stats.last_error}`);
  }
  if (stats.last_at) {
    parts.push(stats.last_at);
  }
  return {
    requests,
    cacheHits,
    primaryOk,
    fallbackOk,
    failures,
    summary,
    lastLine: parts.length > 0 ? parts.join(" · ") : null,
  };
}
