import type {
  SearxngEnginesPreset,
  SearxngManagedConfig,
  SearxngStatus,
  WebSearchProxyMode,
  WebSearchStats,
  WebSearchTestResult,
} from "./types";

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

export const SEARXNG_PYPI_PRESETS: readonly { label: string; url: string }[] = [
  { label: "官方 PyPI", url: "" },
  { label: "清华 TUNA", url: "https://pypi.tuna.tsinghua.edu.cn/simple" },
  { label: "阿里云镜像", url: "https://mirrors.aliyun.com/pypi/simple/" },
];

export const SEARXNG_ENGINE_PRESETS: readonly {
  value: SearxngEnginesPreset;
  label: string;
  desc: string;
}[] = [
  {
    value: "security",
    label: "安全与技术推荐（Google / Bing / DDG / Brave / GitHub / SO / Wiki）",
    desc: "适合漏洞研究、组件检索、公开情报与代码排查；海外引擎建议配合出口代理使用。",
  },
  {
    value: "default",
    label: "上游默认精简（Google / Bing / DuckDuckGo / Brave）",
    desc: "响应快、请求扇出适中，兼顾通用检索准确率与延迟。",
  },
  {
    value: "cn_friendly",
    label: "国内直连友好（Bing / 百度 / DuckDuckGo / GitHub / Wikipedia）",
    desc: "无海外代理环境优先依赖 Bing 与百度，减少海外引擎连续超时。",
  },
];

export interface SearxngStateBadge {
  badge: string;
  tone: "ok" | "warn" | "info" | "error";
  summary: string;
}

/** Formats bundled SearXNG status into a concise UI badge and status line. */
export function describeSearxngState(st: SearxngStatus | null | undefined): SearxngStateBadge {
  if (!st) {
    return { badge: "未加载", tone: "info", summary: "正在读取本地 SearXNG 状态…" };
  }
  if (st.mode === "compose") {
    if (st.healthy) {
      return {
        badge: "Compose 运行正常",
        tone: "ok",
        summary: `Docker Compose 伴生容器已就绪（${st.url}，JSON 接口正常）。`,
      };
    }
    if (st.running) {
      return {
        badge: "Compose 异常",
        tone: "warn",
        summary: st.last_error || `伴生容器可达但 JSON 探测未通过（${st.url}）。`,
      };
    }
    return {
      badge: "Compose 等待就绪",
      tone: "info",
      summary: st.last_error || `等待伴生容器启动或检查 docker compose ps searxng（${st.url}）。`,
    };
  }
  switch (st.state) {
    case "installing":
      return {
        badge: "安装部署中…",
        tone: "info",
        summary: st.install_step || "正在克隆源码并准备 Python venv 依赖…",
      };
    case "starting":
      return {
        badge: "启动中…",
        tone: "info",
        summary: st.install_step || `正在启动 Granian 服务并探测 ${st.url} JSON 健康检查…`,
      };
    case "running":
      return {
        badge: `运行中 · 127.0.0.1:${st.config.port}`,
        tone: "ok",
        summary: `宿主 Python venv sidecar 常驻运行中（PID ${st.pid ?? "-"}，JSON 接口正常，不随 ./blade 重启中断）。`,
      };
    case "error":
      return {
        badge: "异常",
        tone: "error",
        summary: st.last_error || "安装或启动失败，请查看下方日志并检查代理/镜像配置。",
      };
    case "stopped":
      return {
        badge: "已停止",
        tone: "info",
        summary: `已完成 Python venv 部署（${st.python_bin || "python3"}），点击「启动」即可拉起。`,
      };
    default:
      return {
        badge: "未安装 · 宿主 venv",
        tone: "info",
        summary: "BLADE 将在本机 data/searxng 目录拉取上游源码并构建独立 Python 虚拟环境（无需 Docker）。",
      };
  }
}

/** Validates bundled SearXNG form fields before saving configuration. */
export function validateSearxngForm(portText: string, cfg: SearxngManagedConfig): string | null {
  const port = Number.parseInt(portText.trim(), 10);
  if (!Number.isFinite(port) || port < 1024 || port > 65535) {
    return "监听端口需在 1024 到 65535 之间（仅绑定 127.0.0.1）";
  }
  if (cfg.proxy_mode === "custom" && !cfg.custom_proxy.trim()) {
    return "选择「自定义代理」时请填写 SearXNG 出海代理地址";
  }
  const repo = cfg.repo_url.trim();
  if (repo && !/^https?:\/\/\S+/i.test(repo)) {
    return "Git 仓库地址需以 http:// 或 https:// 开头";
  }
  const pypi = cfg.pypi_index.trim();
  if (pypi && !/^https?:\/\/\S+/i.test(pypi)) {
    return "PyPI 镜像源需以 http:// 或 https:// 开头";
  }
  return null;
}
