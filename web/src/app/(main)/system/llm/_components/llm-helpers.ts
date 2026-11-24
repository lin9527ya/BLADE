// LLM 页的共享小工具：thinking 字段的「不发送」哨兵转换，以及熔断冷却的可读文本。
//
// 配置档弹窗、轮询抽屉与页面本身都要用，所以独立成模块。

export const NONE = "none";
export const fromStore = (v?: string) => (v ? v : NONE);
export const toStore = (v: string) => (v === NONE ? "" : v);

export function cooldownText(secs: number) {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

export const THINKING_TYPES: { value: string; label: string }[] = [
  { value: NONE, label: "不发送（默认）" },
  { value: "disabled", label: "关闭" },
  { value: "enabled", label: "开启" },
];

export const MAX_TOKENS_FIELDS: { value: string; label: string }[] = [
  { value: NONE, label: "max_tokens（默认）" },
  { value: "max_completion_tokens", label: "max_completion_tokens" },
];

export const MAX_TOKENS_FIELD_HINTS: Record<string, string> = {
  openai:
    "上限发哪个键。max_tokens 是默认，绝大多数兼容网关只认它；OpenAI 官方推理模型（o 系列 / GPT-5）反过来只认 max_completion_tokens，收到 max_tokens 会直接报 unsupported_parameter。",
  anthropic: "仅 openai 格式可选。Anthropic 的字段名固定为 max_tokens。",
  "openai-responses": "仅 openai 格式可选。Responses API 的字段名固定为 max_output_tokens。",
};

export const EFFORT_LEVELS: { value: string; label: string }[] = [
  { value: NONE, label: "不发送（默认）" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

export const CACHE_BREAKPOINT_MODES: { value: string; label: string }[] = [
  { value: "rolling", label: "滚动多断点（工具+历史，默认）" },
  { value: "system", label: "仅系统前缀" },
];

export const CACHE_TTLS: { value: string; label: string }[] = [
  { value: NONE, label: "5m（默认）" },
  { value: "5m", label: "5m（显式）" },
  { value: "1h", label: "1h（仅静态前缀）" },
];

export const PROMPT_CACHE_RETENTIONS: { value: string; label: string }[] = [
  { value: NONE, label: "默认（不发送）" },
  { value: "in_memory", label: "in_memory" },
  { value: "24h", label: "24h（延伸保留）" },
];
