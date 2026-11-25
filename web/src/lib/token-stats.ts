import type { ModelTokenStat } from "./types.ts";

// 缓存命中率 = 缓存读 / 输入（InputTokens 已含 cache_read 子集，故比值在 0–100%）。
// 一位小数：整数会把 93.7% 和 94.4% 都显示成 94%，看不出优化前后的差距。
export function cacheHitRate(cacheRead: number, input: number): string {
  if (!Number.isFinite(input) || !Number.isFinite(cacheRead) || input <= 0 || cacheRead < 0) return "—";
  if (cacheRead > input) return "数据异常";
  return `${((cacheRead / input) * 100).toFixed(1)}%`;
}

// 费用格式化：未定价显式标"未定价"而不伪造 $0.00；单任务金额往往小于 $0.01，保留 4 位小数
// 避免把真实消耗舍入成 $0.00，小于 $0.0001 的非零开销显式标 "<$0.0001"。
export function fmtCostUSD(cost: number, priced = true): string {
  if (!priced) return "未定价";
  if (!Number.isFinite(cost) || cost < 0) return "—";
  if (cost === 0) return "$0.00";
  if (cost < 0.0001) return "<$0.0001";
  if (cost < 0.01) return `$${cost.toFixed(4)}`;
  if (cost < 100) return `$${cost.toFixed(2)}`;
  return `$${cost.toFixed(0)}`;
}

export interface ModelTokenSummary {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  calls: number;
  costUsd: number;
  pricedCount: number;
  unpricedCount: number;
}

export function summarizeModelTokens(models: readonly ModelTokenStat[]): ModelTokenSummary {
  return models.reduce<ModelTokenSummary>(
    (acc, m) => {
      acc.input += m.input_tokens;
      acc.output += m.output_tokens;
      acc.cacheRead += m.cache_read_tokens;
      acc.cacheWrite += m.cache_write_tokens;
      acc.calls += m.calls;
      if (m.priced) {
        acc.costUsd += m.cost_usd ?? 0;
        acc.pricedCount += 1;
      } else {
        acc.unpricedCount += 1;
      }
      return acc;
    },
    { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, calls: 0, costUsd: 0, pricedCount: 0, unpricedCount: 0 },
  );
}

export function formatTaskCostSummary(
  summary: Pick<ModelTokenSummary, "costUsd" | "pricedCount" | "unpricedCount">,
  options?: { compact?: boolean },
): { text: string; title: string } {
  const totalModels = summary.pricedCount + summary.unpricedCount;
  if (totalModels === 0) {
    return {
      text: "—",
      title: "尚无模型用量计费样本（按「系统 → LLM 配置」模型单价估算）",
    };
  }
  if (summary.pricedCount === 0) {
    return {
      text: "未定价",
      title: "当前模型未配置单价，可在「系统 → LLM 配置」中补充费用单价",
    };
  }
  const formatted = fmtCostUSD(summary.costUsd, true);
  if (summary.unpricedCount > 0) {
    return {
      text: options?.compact ? `${formatted}+` : `${formatted}（含未定价）`,
      title: `${formatted}（另有未配置单价的模型，可在「系统 → LLM 配置」中补充费用单价）`,
    };
  }
  return {
    text: formatted,
    title: "按模型配置单价（含峰/谷时段）估算的本任务总 LLM 成本",
  };
}
