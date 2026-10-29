// 缓存命中率 = 缓存读 / 输入（InputTokens 已含 cache_read 子集，故比值在 0–100%）。
// 一位小数：整数会把 93.7% 和 94.4% 都显示成 94%，看不出优化前后的差距。
export function cacheHitRate(cacheRead: number, input: number): string {
  if (input <= 0) return "—";
  return ((cacheRead / input) * 100).toFixed(1) + "%";
}
