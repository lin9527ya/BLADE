export type MetricResource = { used_bytes: number; total_bytes: number; percent: number };
export type MetricLLM = {
  key: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  zero_cache_calls: number;
  p50_ms: number;
  p95_ms: number;
};
export type RuntimeMetrics = {
  now: string;
  uptime_seconds: number;
  llm_in_flight: number;
  system?: {
    supported: boolean;
    scope: string;
    cpu: MetricResource;
    memory: MetricResource;
    data_disk: MetricResource;
    cpu_limit_cores?: number;
  };
  runtime?: { goroutines: number; heap_alloc_bytes: number };
  db?: { open: number; in_use: number; idle: number; wait: number; max_open: number };
  dropped_activity?: { total: number; by_task: Record<string, number> };
  sse?: { overflow_events: number };
  http?: {
    total: number;
    slow_total: number;
    by_path: { path: string; count: number; errors_5xx: number; errors_4xx: number; p95_ms: number }[];
  };
  llm?: { window_hours: number; by_profile: MetricLLM[]; cold_start_sessions: Record<string, number> };
};
export type CacheTraceState = { enabled: boolean; environment: boolean; expires_at?: string; limit: number };
export type CacheStructure = {
  id: number;
  at: string;
  session: string;
  model: string;
  previous: boolean;
  system_same: boolean;
  tools_same: boolean;
  shared_messages: number;
  messages: number;
  previous_messages: number;
  system: { bytes: number; sha256: string };
  tools: { bytes: number; sha256: string };
  breakpoints: string[];
  frames: { event: string; values: Record<string, number> }[];
  observed_frames: number;
  status: number;
  complete: boolean;
  truncated: boolean;
};
export type UsageDiagnostic = {
  id: number;
  at: string;
  task_id: string;
  worker: string;
  session: string;
  model: string;
  profile: string;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  status: string;
  latency_ms: number;
  first_call: boolean;
};
export type CacheDiagnostics = {
  state: CacheTraceState;
  calls: UsageDiagnostic[];
  requests: CacheStructure[];
  next_before: number;
  window_hours: number;
};

export function diagnosticHitRate(input: number, read: number): string {
  if (!Number.isFinite(input) || !Number.isFinite(read) || input < 0 || read < 0 || read > input) return "数据异常";
  if (!input) return "—";
  return `${((read / input) * 100).toFixed(2)}%`;
}

export function cacheStructureExplanation(row: CacheStructure): string {
  if (!row.previous) return "首个结构样本，尚无可比较记录；不等于模型会话首次调用";
  if (!row.system_same || !row.tools_same) return "系统或工具结构变化，可能影响缓存复用；不是根因确认";
  if (row.shared_messages < Math.min(row.messages, row.previous_messages))
    return "历史消息前缀变化，可能影响复用；不是根因确认";
  return "未发现系统、工具或已有消息前缀变化；缓存仍受网关策略、时效等影响，原因不能仅凭结构确定";
}
