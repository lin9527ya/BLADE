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
