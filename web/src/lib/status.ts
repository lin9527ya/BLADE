// Centralised status → color/label semantics, reused across the whole app.
// Spec §8.3: 意图 / 覆盖 / 任务 / 严重度 each have a consistent color set.

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "rose" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  // rose 用作「严重」——实心高强调,视觉上明显高于「高危」的软红描边。
  rose: "bg-rose-600 text-white border-rose-600 dark:bg-rose-600 dark:text-white",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  rose: "bg-white",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  label: string;
  tone: Tone;
}

const intent: Record<string, StatusMeta> = {
  open: { label: "待领", tone: "slate" },
  running: { label: "执行中", tone: "blue" },
  paused: { label: "已暂停", tone: "amber" },
  done: { label: "已完成", tone: "green" },
  // negative = 探针确实发出且有回包、回包明确否定该假设（worker 经 conclude_intent 声明）。
  negative: { label: "已测(未发现)", tone: "green" },
  // out-of-scope = 该方向超出任务授权范围（worker 经 conclude_intent 声明）。
  "out-of-scope": { label: "范围外", tone: "slate" },
  // blocked = 模型/API/网络故障重试用尽，这条意图基本没真正探成（非目标拦截）。
  blocked: { label: "执行出错", tone: "red" },
  // exhausted = 达到步数/时间预算被中途掐断、只写回部分结果（非方向已探尽）。
  exhausted: { label: "预算耗尽", tone: "violet" },
  // no_output = 正常结束但一条事实/发现/战果都没写回：什么都没测到，可重派（不是已覆盖）。
  no_output: { label: "无产出", tone: "amber" },
  // stopped = 历史软删除状态（保留,历史数据）。
  stopped: { label: "已停止", tone: "slate" },
  // deleted = 用户假删除了该意图（保留节点与血缘，删除原因见 delete_reason 字段）。
  deleted: { label: "已删除", tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { label: "已创建", tone: "slate" },
  queued: { label: "排队中", tone: "amber" },
  running: { label: "运行中", tone: "blue" },
  paused: { label: "已暂停", tone: "amber" },
  done: { label: "已完成", tone: "green" },
  failed: { label: "失败", tone: "red" },
  timeout: { label: "已超时", tone: "amber" },
  policy_blocked: { label: "策略封死", tone: "slate" }, // P20：探索策略内无可行方向而收尾（改策略/加目标可复活）
};

const severity: Record<string, StatusMeta> = {
  critical: { label: "严重", tone: "rose" },
  high: { label: "高危", tone: "red" },
  medium: { label: "中危", tone: "amber" },
  low: { label: "低危", tone: "slate" },
};

const finding: Record<string, StatusMeta> = {
  pending: { label: "待处理", tone: "amber" },
  in_progress: { label: "处理中", tone: "blue" },
  confirmed: { label: "已确认", tone: "red" },
  resolved: { label: "已处理", tone: "green" },
  fixed: { label: "已修复", tone: "green" },
  false_positive: { label: "误报", tone: "slate" },
  ignored: { label: "忽略", tone: "neutral" },
  duplicate: { label: "重复", tone: "neutral" },
  risk_accepted: { label: "风险接受", tone: "violet" },
  // retracted = 复测推翻后撤回；墓碑保留、不进报告正文，原因见 retract_reason。
  retracted: { label: "已撤回", tone: "slate" },
  // ruled_out = 反证闭合（counterevidence 点名控制点）；候选不进报告正文，保留在案。
  ruled_out: { label: "已排除", tone: "slate" },
  // open_proof_gap = 存疑待补证（proof_gap 写清缺什么）；不得视为安全。
  open_proof_gap: { label: "存疑待补证", tone: "amber" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { label: "探索中", tone: "blue" },
  paused: { label: "已暂停", tone: "amber" },
  stalled: { label: "停滞", tone: "red" },
  idle: { label: "空闲", tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { label: "进行中", tone: "blue" },
  met: { label: "已达成", tone: "green" },
  abandoned: { label: "已放弃", tone: "slate" },
};

// 战略提示：active=待规划者采纳，consumed=已采纳（规划者领用后失效）。
const hint: Record<string, StatusMeta> = {
  active: { label: "待采纳", tone: "amber" },
  consumed: { label: "已采纳", tone: "green" },
};

// CVE 库条目状态。
const cve: Record<string, StatusMeta> = {
  active: { label: "启用", tone: "green" },
  deprecated: { label: "已废弃", tone: "slate" },
};

// 打法库卡片状态：与 CVE 同语义（active 参与指纹匹配，deprecated 不再匹配）。
const playbook: Record<string, StatusMeta> = {
  active: { label: "启用", tone: "green" },
  deprecated: { label: "已废弃", tone: "slate" },
};

// 目标侧痕迹：cleaned 与 accepted 必须分开（清掉了 / 决定不清理）。
const footprint: Record<string, StatusMeta> = {
  open: { label: "未处置", tone: "amber" },
  unknown: { label: "未确认", tone: "slate" },
  accepted: { label: "接受风险", tone: "violet" },
  cleaned: { label: "已清理", tone: "green" },
  not_found: { label: "未发现", tone: "neutral" },
};

// 遗留物：active=仍在目标上，removed=已清理，unknown=没去确认（不是"已清理"）。
const artifact: Record<string, StatusMeta> = {
  active: { label: "未清理", tone: "amber" },
  removed: { label: "已清理", tone: "green" },
  unknown: { label: "未确认", tone: "slate" },
};

// 长任务执行台账状态（时间线 execution 详情用）。
const execution: Record<string, StatusMeta> = {
  queued: { label: "排队中", tone: "neutral" },
  running: { label: "进行中", tone: "blue" },
  completed: { label: "完成", tone: "green" },
  failed: { label: "失败", tone: "red" },
  cancelled: { label: "已取消", tone: "slate" },
  hard_timeout: { label: "硬超时", tone: "red" },
  orphaned: { label: "无人接管", tone: "amber" },
};

const audit: Record<string, StatusMeta> = {
  allow: { label: "放行", tone: "green" },
  block: { label: "拦截", tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { label: "观测", tone: "slate" },
  confirmed: { label: "确认", tone: "green" },
  tombstoned: { label: "废弃", tone: "neutral" },
};

// 推送投递状态。sending 用 blue 而不是 amber：它不是「有问题」，
// 而是「已被领取、正在发」，与 pending 的等待语义要能区分开。
const delivery: Record<string, StatusMeta> = {
  pending: { label: "待发送", tone: "amber" },
  sending: { label: "发送中", tone: "blue" },
  sent: { label: "已送达", tone: "green" },
  failed: { label: "失败", tone: "red" },
  skipped: { label: "已跳过", tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  hint,
  cve,
  playbook,
  footprint,
  artifact,
  execution,
  audit,
  node,
  delivery,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { label: key, tone: "neutral" };
}

// 任务终态：后端对这四个状态不再接受「暂停」，只接受「继续运行」（有未执行意图时）。
// 详情页、列表页的暂停按钮与归档资格都从这里取，避免各处硬编码漏掉 policy_blocked。
export const TASK_TERMINAL_STATUSES = new Set<string>(["done", "failed", "timeout", "policy_blocked"]);

export function taskIsTerminal(status: string): boolean {
  return TASK_TERMINAL_STATUSES.has(status);
}
