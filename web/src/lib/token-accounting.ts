import type { TokenTotal, TokenStatsSnapshot } from "./types.ts";

export interface TokenAccountingEvent extends Partial<TokenTotal> {
  seq: number;
  session: string;
  kind: "usage" | "result";
}

export interface TokenAccountingState {
  snapshot: TokenStatsSnapshot | null;
  pending: TokenAccountingEvent[];
}

export const emptyTokenAccounting = (): TokenAccountingState => ({ snapshot: null, pending: [] });
const fields = ["input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"] as const;
const zero = (): TokenTotal => ({ input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 });
const counts = (v: Partial<TokenTotal>): TokenTotal => Object.fromEntries(fields.map((f) => [f, v[f] ?? 0])) as unknown as TokenTotal;
function add(a: TokenTotal, b: Partial<TokenTotal>): TokenTotal {
  return Object.fromEntries(fields.map((f) => [f, a[f] + (b[f] ?? 0)])) as unknown as TokenTotal;
}

// 基线含 cursor 以内的 result 和每会话最新未结算 usage；SSE 仅计基线之外的事件。
// 重复/乱序事件按 seq 去重并排序；旧轮询响应不能把计量水位倒退。
export function acceptTokenSnapshot(state: TokenAccountingState, snapshot: TokenStatsSnapshot): TokenAccountingState {
  if (state.snapshot && snapshot.snapshot_cursor < state.snapshot.snapshot_cursor) return state;
  return { snapshot, pending: state.pending.filter((e) => e.seq > snapshot.snapshot_cursor) };
}

export function acceptTokenEvent(state: TokenAccountingState, event: TokenAccountingEvent): TokenAccountingState {
  if (event.seq <= (state.snapshot?.snapshot_cursor ?? 0) || state.pending.some((e) => e.seq === event.seq)) return state;
  return { ...state, pending: [...state.pending, event].sort((a, b) => a.seq - b.seq) };
}

export function projectTokenAccounting(state: TokenAccountingState): {
  total: TokenTotal | null;
  sessions: Record<string, TokenTotal>;
} {
  const completed: Record<string, TokenTotal> = {};
  const live: Record<string, TokenTotal> = {};
  for (const s of state.snapshot?.sessions ?? []) {
    completed[s.session] = counts(s);
    if (s.live) live[s.session] = counts(s.live);
  }
  let total = counts(state.snapshot?.total ?? zero());
  for (const e of state.pending) {
    if (e.kind === "result") {
      completed[e.session] = add(completed[e.session] ?? zero(), e);
      total = add(total, e);
      delete live[e.session];
    } else {
      live[e.session] = counts(e); // usage 为 run 累计值，替换而不是累加
    }
  }
  const sessions: Record<string, TokenTotal> = { ...completed };
  for (const [session, usage] of Object.entries(live)) {
    sessions[session] = add(sessions[session] ?? zero(), usage);
    total = add(total, usage);
  }
  return { total: state.snapshot ? total : null, sessions };
}
