export interface ScheduleComponents {
  priority: number;
  dependency: number;
  restart: number;
  route: number;
  aging: number;
  retries: number;
}
export interface ScheduleExplanation {
  intent_id: number;
  summary: string;
  state: string;
  evaluated_at: string;
  evaluated: boolean;
  claimable: boolean;
  rank?: number;
  score: number;
  components: ScheduleComponents;
  route: string;
  route_count: number;
  largest_route_count: number;
  waiting_seconds: number;
  fair_wait: boolean;
  selection_reason: string;
  wait_reason?: string;
  wait_reasons?: string[];
}
export interface SchedulingReport {
  evaluated_at: string;
  open_total: number;
  claimable_total: number;
  candidate_count: number;
  rows: ScheduleExplanation[];
  last_dispatch?: ScheduleExplanation;
  dispatch_mode: "regular" | "goal_drain" | "stopped";
  gate_reason?: string;
  worker_slots: number;
  active_works: number;
  continuation_ids?: number[];
}
export type LeadTraceState = "pending" | "queued" | "running" | "paused" | "completed" | "needs_follow_up" | "dismissed" | "source_changed";
export interface LeadIntentTrace {
  intent_id: number;
  summary: string;
  state: string;
  intent_created_at: string;
  linked_at?: string;
  first_claimed_at?: string;
  first_started_at?: string;
  completed_at?: string;
  match: "explicit" | "exact" | "legacy" | string;
  source_changed: boolean;
  text_snapshot?: string;
  last_dispatch?: ScheduleExplanation;
}
export interface LeadTrace {
  id: string;
  kind: "lead" | "proposal";
  fact_id: number;
  text: string;
  state: LeadTraceState;
  fact_created_at: string;
  asset_ids?: number[];
  source_retired: boolean;
  intents: LeadIntentTrace[];
  intent_total: number;
  history_truncated: boolean;
  adopted_at?: string;
  first_started_at?: string;
}
export interface LeadTraceReport {
  rows: LeadTrace[];
  stats: {
    total: number;
    pending: number;
    adopted: number;
    queued: number;
    running: number;
    paused: number;
    completed: number;
    needs_follow_up: number;
    dismissed: number;
    source_changed: number;
    adoption_samples: number;
    execution_samples: number;
    average_adoption_seconds?: number;
    average_execution_seconds?: number;
  };
  total: number;
  limit: number;
  offset: number;
  has_more: boolean;
}
export const LEAD_TRACE_LABELS: Record<LeadTraceState, string> = {
  pending: "待接手",
  queued: "已接手待执行",
  running: "执行中",
  paused: "已暂停",
  completed: "关联执行结束",
  needs_follow_up: "需继续跟进",
  dismissed: "已忽略/来源废弃",
  source_changed: "来源变更需复核",
};
const WAIT_LABELS: Record<string, string> = {
  strict_dependency: "等待 strict 活跃前置",
  advisory_dependency: "advisory 前置带来轻度排序扣分",
  earlier_candidates: "前面仍有更靠前的候选",
  outside_candidate_window: "本次候选窗口之外，未计算得分",
  task_paused: "任务已暂停",
  task_queued: "任务等待运行名额",
  task_terminal: "任务已结束",
  task_deleting: "任务正在删除",
  task_settling: "任务收尾中，不领新意图",
  llm_unavailable: "模型当前不可用",
  engine_not_started: "任务引擎尚未启动",
  worker_slots_busy: "工作槽位正在使用，等待释放",
  not_drain_continuation: "达标收尾只允许原在跑方向续读输出",
  not_open: "当前不是待领意图，不参与实时排序",
  read_unknown: "读取失败，暂不能判断",
};
export function scheduleWaitLabel(reason: string): string {
  return WAIT_LABELS[reason] ?? reason;
}
export function trackingDuration(seconds?: number): string {
  if (seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return "未知";
  if (seconds < 60) return `${seconds.toFixed(1)} 秒`;
  if (seconds < 3600) return `${(seconds / 60).toFixed(1)} 分钟`;
  return `${(seconds / 3600).toFixed(1)} 小时`;
}
export function schedulingScore(row: ScheduleExplanation): string {
  return row.evaluated && Number.isFinite(row.score) ? row.score.toFixed(1) : "未评分";
}
