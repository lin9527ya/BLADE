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
  { value: "adaptive", label: "自适应（adaptive）" },
];

export const THINKING_TYPE_HINTS: Record<string, string> = {
  "openai-responses":
    "Responses API 不发 thinking 字段，仅靠下方 reasoning_effort 激活；设为「关闭」会一并抑制强度配置，否则不限制。",
  anthropic:
    "控制是否发送 thinking（不发送/disabled/enabled/adaptive）。Claude 4.6 推荐选 adaptive 并配合下方强度；disabled 会同时抑制强度。",
  openai: "控制是否发送 thinking.type（不发送=兼容不支持该键的模型；关闭=发 disabled 并抑制强度；开启=发 enabled）。",
};

export const REASONING_EFFORT_HINTS: Record<string, string> = {
  anthropic: "映射为 output_config.effort（low/medium/high/max）。当上方设为「关闭」或后台禁思考调用时会自动抑制。",
  "openai-responses":
    "映射为 reasoning.effort（none/minimal/low/medium/high/xhigh/max；DeepSeek 等模型支持 max，OpenAI 官方最高 xhigh）。Responses API 主要靠本项控制推理开关与深度。",
  openai:
    "映射为 reasoning_effort（none/minimal/low/medium/high/xhigh/max；DeepSeek 等支持 max，OpenAI 官方最高 xhigh）。当上方设为「关闭」时会自动抑制不发。",
};

export function thinkingTypesForFormat(format: string): { value: string; label: string }[] {
  if (format === "anthropic") return THINKING_TYPES;
  return THINKING_TYPES.filter((o) => o.value !== "adaptive");
}

export const EFFORT_LEVELS: { value: string; label: string }[] = [
  { value: NONE, label: "不发送（默认）" },
  { value: "none", label: "none" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

export function effortLevelsForFormat(format: string, current?: string): { value: string; label: string }[] {
  const allowed =
    format === "anthropic"
      ? new Set([NONE, "low", "medium", "high", "max"])
      : new Set([NONE, "none", "minimal", "low", "medium", "high", "xhigh", "max"]);
  const base = EFFORT_LEVELS.filter((o) => allowed.has(o.value));
  if (current && current !== NONE && !allowed.has(current)) {
    return [...base, { value: current, label: `${current}（旧值）` }];
  }
  return base;
}

export function formatThinkingSummary(p: {
  thinking_type?: string;
  thinking_budget_tokens?: number;
  reasoning_effort?: string;
}): string {
  const tt = p.thinking_type?.trim() ?? "";
  const eff = p.reasoning_effort?.trim() ?? "";
  if (tt === "disabled" || eff === "off") return "思考 关";
  if (tt === "adaptive") return eff ? `思考 自适应 · ${eff}` : "思考 自适应";
  if (tt === "enabled") {
    const extras: string[] = [];
    if (eff) extras.push(eff);
    if ((p.thinking_budget_tokens ?? 0) > 0) extras.push(`预算 ${p.thinking_budget_tokens}`);
    return extras.length > 0 ? `思考 开 · ${extras.join(" · ")}` : "思考 开";
  }
  if (eff) return `思考强度 ${eff}`;
  return "";
}

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
