"use client";

import * as React from "react";

import {
  ActivityIcon,
  AlertTriangleIcon,
  BanIcon,
  BellRingIcon,
  BookOpenIcon,
  BugIcon,
  CheckIcon,
  ClockIcon,
  CoinsIcon,
  LightbulbIcon,
  ListChecksIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  TargetIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

import { nodeSummary } from "@/components/_parts/graph-helpers";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination, useClientPagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Progress } from "@/components/ui/progress";
import { api } from "@/lib/api";
import { taskIsTerminal } from "@/lib/status";
import { cacheHitRate, fmtCostUSD } from "@/lib/token-stats";
import type { TaskConstraint, TaskScopeRow } from "@/lib/types";

import { ArtifactPanel } from "./_components/artifact-panel";
import { CollapsibleOverviewCard } from "./_components/collapsible-overview-card";
import { ExplorationTrackingCard } from "./_components/exploration-tracking-card";
import {
  TaskAssetScopeCard,
  TaskCredentialBudgetCard,
  TaskInterceptRulesCard,
  TaskLootExecCard,
  TaskRuntimeCard,
} from "./_components/task-policy-cards";
import { TaskRelationsCard } from "./_components/task-relations-card";
import { useOverviewState } from "./_components/use-overview-state";

// 紧凑格式化 token 数（12345 → 12.3k，2000000 → 2M）。
// budgetBarTone：按预算用量占比取进度条颜色（与后端 70/85/95/100 档位一致）。
function budgetBarTone(pct: number): string {
  if (pct >= 100) return "bg-red-500";
  if (pct >= 95) return "bg-red-400";
  if (pct >= 85) return "bg-amber-500";
  if (pct >= 70) return "bg-yellow-500";
  return "bg-emerald-500";
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

// 测试范围一条的显示值：域名 / 网段 / 公司。
function scopeValue(row: TaskScopeRow): string {
  if (row.value) return row.value;
  if (row.domain) return row.domain;
  if (row.net) return row.net;
  if (row.company_id) return row.company_name?.trim() ? row.company_name : `企业 #${row.company_id}`;
  return "—";
}

const SCOPE_KIND_LABELS: Record<TaskScopeRow["kind"], string> = {
  company: "公司",
  root_domain: "根域名",
  subdomain: "子域名",
  ip: "IP",
  cidr: "网段",
  icp: "ICP",
  keyword: "关键词",
};

const SCOPE_SOURCE_LABELS: Record<TaskScopeRow["source"], string> = {
  auto: "自动",
  agent: "Agent",
  manual: "手动",
};

// 提示超过这个时长仍未采纳，就在卡片里标出来：planner 可能漏看了，而不是"它还在考虑"。
const HINT_STALE_MS = 24 * 60 * 60 * 1000;
function hintIsStale(h: { state: string; ts: string }): boolean {
  if (h.state !== "active") return false;
  const ts = new Date(h.ts).getTime();
  return Number.isFinite(ts) && Date.now() - ts > HINT_STALE_MS;
}

function StatCard({
  label,
  value,
  sub,
  icon: Icon,
}: {
  label: string;
  value: React.ReactNode;
  sub?: string;
  icon: React.ElementType;
}) {
  return (
    <Card className="gap-1.5">
      <CardHeader className="pb-0">
        <CardDescription className="flex items-center gap-1.5">
          <Icon className="size-3.5" /> {label}
        </CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {sub && <CardContent className="text-muted-foreground text-xs">{sub}</CardContent>}
    </Card>
  );
}

export function OverviewTab({ taskId, sseConnected = false }: { taskId: string; sseConnected?: boolean }) {
  const [showPlannerState, setShowPlannerState] = React.useState(false);
  const [showGoals, setShowGoals] = React.useState(true);
  const [showHints, setShowHints] = React.useState(true);
  const [showConstraints, setShowConstraints] = React.useState(true);
  const [showScope, setShowScope] = React.useState(true);
  const {
    task,
    stats,
    intents,
    plannerState,
    coverage,
    rerunning,
    resuming,
    scope,
    scopeKind,
    setScopeKind,
    scopeValueInput,
    setScopeValueInput,
    scopeBusy,
    scopeErr,
    modelTokens,
    tokenBudget,
    goals,
    goalText,
    setGoalText,
    goalVuln,
    setGoalVuln,
    goalBusy,
    goalErr,
    editingGoalId,
    editText,
    setEditText,
    editVuln,
    setEditVuln,
    hints,
    hintText,
    setHintText,
    hintBusy,
    hintErr,
    editingHintId,
    editHintText,
    setEditHintText,
    artifacts,
    artifactRiskAccepted,
    clearArtifact,
    acceptArtifactRisk,
    constraints,
    conText,
    setConText,
    conKind,
    setConKind,
    conBusy,
    conErr,
    editingConId,
    editConText,
    setEditConText,
    editConKind,
    setEditConKind,
    compileFor,
    setCompileFor,
    compileResult,
    setCompileResult,
    compileBusy,
    addGoal,
    startEditGoal,
    cancelEditGoal,
    saveEditGoal,
    removeGoal,
    abandonGoal,
    addHint,
    removeHint,
    startEditHint,
    cancelEditHint,
    saveEditHint,
    restoreHint,
    remindHint,
    addConstraint,
    startEditConstraint,
    cancelEditConstraint,
    saveEditConstraint,
    removeConstraint,
    openCompile,
    applyCompile,
    addScope,
    removeScope,
    rerunOne,
    rerunAll,
    resumeTerminal,
    running,
    open,
    blocked,
    taskFindings,
    goalsPct,
    tokenTotals,
    budgetUsed,
    budgetPct,
  } = useOverviewState(taskId, sseConnected);

  // 手动沉淀：自动触发依赖调度水位，历史任务/漏触发时用它补一次。
  const [distilling, setDistilling] = React.useState(false);
  const distillPlaybook = async () => {
    setDistilling(true);
    try {
      await api.distillTaskPlaybook(taskId);
      toast.success("已提交打法沉淀，稍后到「打法库」查看结果");
    } catch (e) {
      toast.error(`提交失败：${e instanceof Error ? e.message : "未知错误"}`);
    } finally {
      setDistilling(false);
    }
  };

  // 精确计数：列表接口有行数上限（意图 300 / 发现 200），len(列表) 是窗口长度不是总数。

  // 与「发现」页同口径：api.findings(taskId) 已过滤掉候选闭合，统计不再用节点总数。
  const totalFindings = taskFindings.length;
  // deleted 是规划者主动丢弃的方向，不计入「意图总数」；精确计数优先用
  // intent_states（列表接口有行数上限），无精确计数时回退到过滤后的窗口。
  const intentStates = stats?.intent_states;
  const totalIntents = intentStates
    ? Object.entries(intentStates).reduce((n, [state, count]) => (state === "deleted" ? n : n + count), 0)
    : intents.filter((i) => i.state !== "deleted").length;
  const totalOpen = stats?.claimable_intents ?? open.length;
  const totalRunning = stats?.intent_states?.running ?? running.length;
  const totalBlocked = stats?.intent_states?.blocked ?? blocked.length;
  const totalExhausted = stats?.intent_states?.exhausted ?? intents.filter((i) => i.state === "exhausted").length;
  const totalStopped = stats?.intent_states?.stopped ?? intents.filter((i) => i.state === "stopped").length;
  const totalNoOutput = stats?.intent_states?.no_output ?? intents.filter((i) => i.state === "no_output").length;
  // 依赖阻塞：open 但 strict 前置仍活跃；与 state=blocked（执行失败）分开说。
  const blockedByDeps = stats?.blocked_intents ?? [];
  const totalBlockedByDeps = stats?.blocked_open_intents ?? blockedByDeps.length;
  const sortedHints = [...hints].sort((a, b) => (a.state === b.state ? 0 : a.state === "active" ? -1 : 1));
  const goalsPager = useClientPagination(goals, 5, taskId);
  const hintsPager = useClientPagination(sortedHints, 5, taskId);
  const constraintsPager = useClientPagination(constraints, 5, taskId);
  const todoPager = useClientPagination(plannerState?.todos ?? [], 10, taskId);
  const scopePager = useClientPagination(scope, 10, taskId);
  const runningPager = useClientPagination(running, 6, taskId);
  const findingsPager = useClientPagination(taskFindings, 6, taskId);
  const blockedByDepsPager = useClientPagination(blockedByDeps, 5, taskId);
  const blockedPager = useClientPagination(blocked, 10, taskId);

  return (
    <div className="flex flex-col gap-4">
      <TaskRelationsCard taskId={taskId} />
      {/* 遗留物（P43）：我们留在目标上的东西。放在最前面——任务收尾前最该被看见的一条，
          没清掉的 webshell 就是留在客户系统上的后门。
          active 与 unknown 分开说：unknown 是"没去确认还在不在"，并进未清理会把
          "我们不知道"说成"我们确认它还留着"（同检测三态的纪律）。 */}
      {((stats?.artifacts_uncleaned ?? 0) > 0 || (stats?.artifacts_unknown ?? 0) > 0) && (
        <Card className="border-amber-500/40 bg-amber-500/5">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <AlertTriangleIcon className="size-4 text-amber-600 dark:text-amber-400" />
              遗留物未清
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-1.5 text-sm">
            {(stats?.artifacts_uncleaned ?? 0) > 0 && (
              <p>
                有 <strong className="tabular-nums">{stats?.artifacts_uncleaned}</strong> 件遗留物
                <strong>仍留在目标上未清理</strong>——没清掉的 webshell 就是留在客户系统上的后门。
              </p>
            )}
            {(stats?.artifacts_unknown ?? 0) > 0 && (
              <p className="text-muted-foreground">
                另有 <span className="tabular-nums">{stats?.artifacts_unknown}</span> 件<strong>未确认</strong>
                还在不在（不是「已清理」，只是没去核）。
              </p>
            )}
            <p className="text-muted-foreground text-xs">
              清单与清理方式见导出的报告「遗留物与清理」一节，或在播报板/图里按「遗留物」筛选。
            </p>
            <div className="border-t pt-2">
              <ArtifactPanel
                artifacts={artifacts}
                riskAccepted={artifactRiskAccepted}
                onClear={clearArtifact}
                onAcceptRisk={acceptArtifactRisk}
              />
            </div>
          </CardContent>
        </Card>
      )}
      {/* 原始任务描述与目标(创建时填写的),置顶便于随时回看。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <TargetIcon className="size-4 text-primary" /> 任务描述与目标
          </CardTitle>
          <CardAction>
            <Button
              size="sm"
              variant="outline"
              disabled={distilling}
              title="让「打法沉淀」agent 复盘本任务并写入打法库（自动触发漏掉时用）"
              onClick={() => {
                if (window.confirm("将用 LLM 复盘本任务并把可复用的方法写入打法库，继续？")) {
                  void distillPlaybook();
                }
              }}
            >
              <BookOpenIcon data-icon="inline-start" />
              {distilling ? "提交中…" : "沉淀本任务打法"}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <div className="font-medium text-muted-foreground text-xs">描述</div>
            <p className="whitespace-pre-wrap break-words text-sm">{task?.description?.trim() || "—"}</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <div className="font-medium text-muted-foreground text-xs">目标</div>
            <p className="whitespace-pre-wrap break-words text-sm">{task?.goal?.trim() || "—"}</p>
          </div>
        </CardContent>
      </Card>
      {/* 目标管理：查看/新增/修改/删除本任务的探索目标。新增与修改会通知规划者并复活任务，
          删除仅通知规划者（不复活）。目标 = 最终可交付/可核验的结果，不是攻击步骤或侦察动作。 */}
      <CollapsibleOverviewCard
        title="目标管理"
        icon={<ListChecksIcon className="size-4 text-primary" />}
        meta={<span className="font-normal text-muted-foreground text-xs">（共 {goals.length} 条）</span>}
        description={
          showGoals ? "最终可核验的目标；新增/修改会通知规划者并复活任务。" : "已收起，点击右侧「查看目标管理」展开。"
        }
        open={showGoals}
        onOpenChange={setShowGoals}
        openLabel="收起目标管理"
        closedLabel="查看目标管理"
      >
        {/* 新增表单 */}
        <div className="flex flex-wrap items-center gap-2">
          <Input
            className="h-7 min-w-56 flex-1 text-sm"
            placeholder="新增目标，如『拿到管理员账号的越权访问』"
            value={goalText}
            onChange={(e) => setGoalText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void addGoal();
            }}
            disabled={goalBusy}
          />
          <Input
            className="h-7 w-32 text-sm"
            placeholder="漏洞类(可选)"
            value={goalVuln}
            onChange={(e) => setGoalVuln(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void addGoal();
            }}
            disabled={goalBusy}
          />
          <Button size="sm" variant="outline" disabled={goalBusy || !goalText.trim()} onClick={() => void addGoal()}>
            <PlusIcon className="size-3.5" /> 添加
          </Button>
          {goalErr && <span className="text-red-500 text-xs">{goalErr}</span>}
        </div>
        {/* 目标列表 */}
        {goals.length > 0 ? (
          <div className="flex flex-col gap-1.5">
            {goalsPager.pageItems.map((g) =>
              editingGoalId === g.id ? (
                <div key={g.id} className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
                  <Input
                    className="h-7 min-w-56 flex-1 text-sm"
                    value={editText}
                    onChange={(e) => setEditText(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") void saveEditGoal(g);
                      if (e.key === "Escape") cancelEditGoal();
                    }}
                    disabled={goalBusy}
                    autoFocus
                  />
                  <Input
                    className="h-7 w-32 text-sm"
                    placeholder="漏洞类(可选)"
                    value={editVuln}
                    onChange={(e) => setEditVuln(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") void saveEditGoal(g);
                      if (e.key === "Escape") cancelEditGoal();
                    }}
                    disabled={goalBusy}
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={goalBusy || !editText.trim()}
                    onClick={() => void saveEditGoal(g)}
                  >
                    <CheckIcon className="size-3.5 text-emerald-500" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={goalBusy}
                    onClick={cancelEditGoal}
                  >
                    <XIcon className="size-3.5" />
                  </Button>
                </div>
              ) : (
                <div key={g.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                  <StatusBadge domain="goal" value={g.state} />
                  <span className="min-w-0 flex-1 break-words">{g.text}</span>
                  {g.vulnclass && (
                    <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-muted-foreground text-xs">
                      {g.vulnclass}
                    </span>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={goalBusy}
                    onClick={() => startEditGoal(g)}
                  >
                    <PencilIcon className="size-3.5" />
                  </Button>
                  {g.state === "open" && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 shrink-0 px-1.5"
                      disabled={goalBusy}
                      title="放弃目标（不可达/已下线/客户取消），需要写明理由"
                      onClick={() => {
                        const reason = window.prompt(
                          `放弃目标「${g.text}」的理由（必填）：不可达 / 已下线 / 客户取消 / 超出授权范围…\n放弃后目标保留在图上，但不再阻断收官，报告会区分「已放弃」与「已达成」。`,
                        );
                        if (reason?.trim()) void abandonGoal(g, reason.trim());
                      }}
                    >
                      <BanIcon className="size-3.5 text-amber-500" />
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={goalBusy}
                    onClick={() => {
                      if (
                        window.confirm(
                          `删除目标「${g.text}」？删除后规划者不再向它派发意图，也不会再判定它达成；此操作不可撤销。`,
                        )
                      ) {
                        void removeGoal(g);
                      }
                    }}
                  >
                    <Trash2Icon className="size-3.5 text-red-500" />
                  </Button>
                </div>
              ),
            )}
            <TablePagination {...goalsPager.paginationProps} />
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">暂无目标，添加后规划者会据此派发探索意图并判定达成。</p>
        )}
      </CollapsibleOverviewCard>
      {/* 战略提示：操作者在任务运行中直接给规划者一句话方向。新增立即唤醒 planner，
          采纳后由它 consume（状态变「已采纳」），不再每轮反复出现。不自动复活任务。 */}
      <CollapsibleOverviewCard
        title="战略提示 / 测试方向"
        icon={<LightbulbIcon className="size-4 text-amber-500" />}
        meta={<span className="font-normal text-muted-foreground text-xs">（共 {hints.length} 条）</span>}
        description={
          showHints
            ? "给规划者一句话方向；采纳后标记「已采纳」，不再反复出现。"
            : "已收起，点击右侧「查看战略提示」展开。"
        }
        open={showHints}
        onOpenChange={setShowHints}
        openLabel="收起战略提示"
        closedLabel="查看战略提示"
      >
        <div className="flex flex-wrap items-center gap-2">
          <Input
            className="h-7 min-w-56 flex-1 text-sm"
            placeholder="如『重点看认证后的越权接口』"
            value={hintText}
            onChange={(e) => setHintText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void addHint();
            }}
            disabled={hintBusy}
          />
          <Button size="sm" variant="outline" disabled={hintBusy || !hintText.trim()} onClick={() => void addHint()}>
            <PlusIcon className="size-3.5" /> 发送
          </Button>
          {hintErr && <span className="text-red-500 text-xs">{hintErr}</span>}
        </div>
        {task && task.status !== "running" && (
          <p className="text-muted-foreground text-xs">任务当前未运行，提示会在下次规划时生效（不会自动恢复任务）。</p>
        )}
        {sortedHints.length > 0 ? (
          <div className="flex flex-col gap-1.5">
            {hintsPager.pageItems.map((h) => (
              <div
                key={h.id}
                className={`rounded-md border px-2.5 py-1.5 text-sm ${
                  h.state === "consumed" ? "opacity-60" : ""
                } ${hintIsStale(h) ? "border-amber-500/50 bg-amber-500/5" : ""}`}
              >
                {editingHintId === h.id && h.state === "active" ? (
                  <div className="flex flex-wrap items-center gap-2">
                    <StatusBadge domain="hint" value={h.state} />
                    <Input
                      className="h-7 min-w-56 flex-1 text-sm"
                      value={editHintText}
                      onChange={(e) => setEditHintText(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") void saveEditHint(h);
                        if (e.key === "Escape") cancelEditHint();
                      }}
                      disabled={hintBusy}
                      autoFocus
                    />
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 shrink-0 px-1.5"
                      disabled={hintBusy || !editHintText.trim()}
                      onClick={() => void saveEditHint(h)}
                    >
                      <CheckIcon className="size-3.5 text-emerald-500" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 shrink-0 px-1.5"
                      disabled={hintBusy}
                      onClick={cancelEditHint}
                    >
                      <XIcon className="size-3.5" />
                    </Button>
                  </div>
                ) : (
                  <div className="flex items-start gap-2">
                    <StatusBadge domain="hint" value={h.state} />
                    <div className="min-w-0 flex-1">
                      <span className="block break-words">{h.text}</span>
                      {h.note && (
                        <span className="mt-0.5 block break-words text-muted-foreground text-xs">
                          采纳说明：{h.note}
                        </span>
                      )}
                      <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                        <span className="tabular-nums" title={h.ts}>
                          {new Date(h.ts).toLocaleString("zh-CN", { hour12: false })}
                        </span>
                        {(h.asset_ids ?? []).map((id) => (
                          <span key={id} className="rounded bg-muted px-1.5 py-0.5 text-muted-foreground">
                            资产 #{id}
                          </span>
                        ))}
                        {hintIsStale(h) && (
                          <span className="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400">
                            <ClockIcon className="size-3" />
                            超过 24 小时未采纳，规划者可能漏看
                          </span>
                        )}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      {h.state === "active" ? (
                        <>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-6 shrink-0 px-1.5"
                            title="编辑提示（会重新唤醒规划者）"
                            disabled={hintBusy}
                            onClick={() => startEditHint(h)}
                          >
                            <PencilIcon className="size-3.5" />
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-6 shrink-0 px-1.5"
                            title="再次提醒规划者"
                            disabled={hintBusy}
                            onClick={() => void remindHint(h)}
                          >
                            <BellRingIcon className="size-3.5" />
                          </Button>
                        </>
                      ) : (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 shrink-0 px-1.5"
                          title="撤销采纳（回到待采纳，规划者下一轮重新看到）"
                          disabled={hintBusy}
                          onClick={() => void restoreHint(h)}
                        >
                          <RotateCcwIcon className="size-3.5" />
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 shrink-0 px-1.5"
                        onClick={() => {
                          if (
                            window.confirm(
                              `删除提示「${h.text}」？删除后规划者下一轮不再看到它；已进入本轮触发的仍可能被看到一次。`,
                            )
                          ) {
                            void removeHint(h);
                          }
                        }}
                      >
                        <Trash2Icon className="size-3.5 text-red-500" />
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            ))}
            <TablePagination {...hintsPager.paginationProps} />
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">暂无提示</p>
        )}
        <p className="text-muted-foreground text-xs leading-relaxed">
          {task && task.status === "running" ? "发送后立即唤醒规划者。" : "任务运行后，规划者下一轮会读到。"}
          它把方向派成意图（或判断与现状重复/不适用）后会标记「已采纳」；
          未被采纳的提示会一直留在待采纳列表，可随时删除。提示只是测试方向，优先级低于操作约束与授权范围。
        </p>
      </CollapsibleOverviewCard>
      {/* 操作约束管理：allow=允许 / deny=禁止。约束会在下一轮规划时注入 planner/worker 的系统
          提示以框定探索边界（注入范围可在 系统设置 里按 planner/worker 开关）。改动不即时打断，
          下一轮规划自然读到。 */}
      <CollapsibleOverviewCard
        title="操作约束"
        icon={<ShieldAlertIcon className="size-4 text-amber-500" />}
        meta={<span className="font-normal text-muted-foreground text-xs">（共 {constraints.length} 条）</span>}
        description={
          showConstraints
            ? "默认只作提示词约束注入 planner/worker，改动下一轮规划生效；点盾牌图标可编译为任务级硬拦截规则。"
            : "已收起，点击右侧「查看操作约束」展开。"
        }
        open={showConstraints}
        onOpenChange={setShowConstraints}
        openLabel="收起操作约束"
        closedLabel="查看操作约束"
      >
        {/* 新增表单 */}
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect
            size="sm"
            value={conKind}
            onChange={(e) => setConKind(e.target.value as TaskConstraint["kind"])}
          >
            <NativeSelectOption value="deny">禁止</NativeSelectOption>
            <NativeSelectOption value="allow">允许</NativeSelectOption>
          </NativeSelect>
          <Input
            className="h-7 min-w-56 flex-1 text-sm"
            placeholder="一条操作约束，如『仅测当前端口，不扫其他端口』"
            value={conText}
            onChange={(e) => setConText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void addConstraint();
            }}
            disabled={conBusy}
          />
          <Button
            size="sm"
            variant="outline"
            disabled={conBusy || !conText.trim()}
            onClick={() => void addConstraint()}
          >
            <PlusIcon className="size-3.5" /> 添加
          </Button>
          {conErr && <span className="text-red-500 text-xs">{conErr}</span>}
        </div>
        {/* 约束列表 */}
        {constraints.length > 0 ? (
          <div className="flex flex-col gap-1.5">
            {constraintsPager.pageItems.map((c) =>
              editingConId === c.id ? (
                <div key={c.id} className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
                  <NativeSelect
                    size="sm"
                    value={editConKind}
                    onChange={(e) => setEditConKind(e.target.value as TaskConstraint["kind"])}
                  >
                    <NativeSelectOption value="deny">禁止</NativeSelectOption>
                    <NativeSelectOption value="allow">允许</NativeSelectOption>
                  </NativeSelect>
                  <Input
                    className="h-7 min-w-56 flex-1 text-sm"
                    value={editConText}
                    onChange={(e) => setEditConText(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") void saveEditConstraint(c);
                      if (e.key === "Escape") cancelEditConstraint();
                    }}
                    disabled={conBusy}
                    autoFocus
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={conBusy || !editConText.trim()}
                    onClick={() => void saveEditConstraint(c)}
                  >
                    <CheckIcon className="size-3.5 text-emerald-500" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={conBusy}
                    onClick={cancelEditConstraint}
                  >
                    <XIcon className="size-3.5" />
                  </Button>
                </div>
              ) : (
                <div key={c.id} className="flex flex-col gap-2">
                  <div className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                    <span
                      className={`shrink-0 rounded px-1.5 py-0.5 text-xs ${
                        c.kind === "allow"
                          ? "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
                          : "bg-red-500/15 text-red-600 dark:text-red-400"
                      }`}
                    >
                      {c.kind === "allow" ? "允许" : "禁止"}
                    </span>
                    <span className="min-w-0 flex-1 break-words">{c.text}</span>
                    {c.compiled_rules ? (
                      <span
                        className="shrink-0 rounded bg-sky-500/15 px-1.5 py-0.5 text-sky-600 text-xs dark:text-sky-400"
                        title="已编译为任务级拦截规则，只对本任务生效"
                      >
                        已生效 {c.compiled_rules} 条
                      </span>
                    ) : (
                      <span
                        className="shrink-0 text-muted-foreground text-xs"
                        title="当前只作为提示词约束注入 planner/worker（建议，不是硬拦截）"
                      >
                        仅提示词
                      </span>
                    )}
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 shrink-0 px-1.5"
                      disabled={conBusy || compileBusy}
                      title="编译为任务级拦截规则（先预览再应用）"
                      onClick={() => void openCompile(c)}
                    >
                      <ShieldCheckIcon className="size-3.5" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 shrink-0 px-1.5"
                      disabled={conBusy}
                      onClick={() => startEditConstraint(c)}
                    >
                      <PencilIcon className="size-3.5" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 shrink-0 px-1.5"
                      disabled={conBusy}
                      onClick={() => void removeConstraint(c)}
                    >
                      <Trash2Icon className="size-3.5 text-red-500" />
                    </Button>
                  </div>
                  {compileFor === c.id ? (
                    <div className="flex flex-col gap-2 rounded-md border border-dashed bg-muted/30 p-2.5 text-xs">
                      <div className="text-muted-foreground">
                        {compileBusy && !compileResult ? "编译中…" : (compileResult?.note ?? "")}
                      </div>
                      {compileResult?.rules?.length ? (
                        <div className="flex flex-col gap-1">
                          <span className="font-medium">
                            将生成 {compileResult.rules.length} 条拦截规则（只对本任务生效）：
                          </span>
                          {compileResult.rules.map((r) => {
                            const pv = compileResult.preview?.find((x) => x.pattern === r.pattern);
                            return (
                              <div
                                key={r.pattern}
                                className="flex flex-col gap-0.5 rounded border bg-background px-2 py-1"
                              >
                                <span className="break-all font-mono">
                                  {r.action} · {r.pattern}
                                </span>
                                {pv ? (
                                  <span className="text-muted-foreground">
                                    会命中最近 {pv.total} 次工具调用中的 {pv.matched} 次
                                    {pv.samples.length > 0 ? `（如：${pv.samples[0]}）` : ""}
                                  </span>
                                ) : (
                                  <span className="text-muted-foreground">本任务还没有可对照的历史调用</span>
                                )}
                              </div>
                            );
                          })}
                        </div>
                      ) : null}
                      {compileResult?.scope?.length ? (
                        <div className="flex flex-col gap-1">
                          <span className="font-medium">
                            将加入本任务授权范围（RoE 允许面）{compileResult.scope.length} 条：
                          </span>
                          {compileResult.scope.map((sc) => (
                            <span key={`${sc.kind}:${sc.value}`} className="break-all font-mono">
                              {sc.kind} · {sc.value}
                            </span>
                          ))}
                        </div>
                      ) : null}
                      <div className="flex gap-2">
                        <Button
                          size="sm"
                          className="h-6 px-2 text-xs"
                          disabled={
                            compileBusy || !((compileResult?.rules?.length ?? 0) + (compileResult?.scope?.length ?? 0))
                          }
                          onClick={() => void applyCompile(c)}
                        >
                          应用
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-6 px-2 text-xs"
                          disabled={compileBusy}
                          onClick={() => {
                            setCompileFor(null);
                            setCompileResult(null);
                          }}
                        >
                          取消
                        </Button>
                      </div>
                    </div>
                  ) : null}
                </div>
              ),
            )}
            <TablePagination {...constraintsPager.paginationProps} />
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">
            暂无操作约束。建任务时会自动从描述/目标抽取；也可在此手动增删改，用来框定「允许/禁止做哪些操作」。
          </p>
        )}
      </CollapsibleOverviewCard>
      <TaskAssetScopeCard taskId={taskId} policy={task?.asset_scope_policy} />
      <TaskLootExecCard taskId={taskId} enabled={task?.loot_exec_enabled} />
      <TaskCredentialBudgetCard taskId={taskId} budget={task?.credential_attempt_budget} />
      <TaskRuntimeCard task={task} />
      <Collapsible open={showPlannerState} onOpenChange={setShowPlannerState}>
        <Card>
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <CardTitle className="flex items-center gap-2 text-base">
                <ListChecksIcon className="size-4" /> Planner 规划备忘
                {plannerState?.found && (
                  <span className="font-normal text-muted-foreground text-xs">
                    （{plannerState.todos.length} 条 Todo）
                  </span>
                )}
              </CardTitle>
              <CollapsibleTrigger asChild>
                <Button size="sm" variant="outline">
                  {showPlannerState ? "收起规划备忘" : "查看规划备忘"}
                </Button>
              </CollapsibleTrigger>
            </div>
            <CardDescription>
              {showPlannerState
                ? "规划者跨轮次维护的 TodoWrite 清单与上轮结论摘要。"
                : "默认收起，按需查看规划者 TodoWrite 进度与上轮结论。"}
            </CardDescription>
          </CardHeader>
          <CollapsibleContent>
            <CardContent className="space-y-3 text-sm">
              {!plannerState?.found ? (
                <p className="text-muted-foreground">规划者尚未写入备忘状态。</p>
              ) : (
                <>
                  {plannerState.updated_at && (
                    <p className="text-muted-foreground text-xs">
                      最近更新：{new Date(plannerState.updated_at).toLocaleString("zh-CN")}
                    </p>
                  )}
                  {plannerState.last_round_note && (
                    <div className="rounded-md border bg-muted/30 p-3">
                      <div className="mb-1 font-medium text-muted-foreground text-xs">上轮结论</div>
                      <p className="whitespace-pre-wrap break-words text-xs leading-relaxed">
                        {plannerState.last_round_note}
                      </p>
                    </div>
                  )}
                  {plannerState.todos.length > 0 ? (
                    <div className="space-y-2">
                      <ul className="space-y-1.5">
                        {todoPager.pageItems.map((todo, idx) => (
                          <li
                            key={`${todoPager.page}-${idx}-${todo.status}-${todo.content.slice(0, 24)}`}
                            className="flex items-start gap-2 rounded-md border px-2.5 py-1.5 text-xs"
                          >
                            <Badge variant="outline" className="shrink-0">
                              {todo.status === "completed"
                                ? "已完成"
                                : todo.status === "in_progress"
                                  ? "进行中"
                                  : todo.status === "pending"
                                    ? "待办"
                                    : todo.status}
                            </Badge>
                            <span className="min-w-0 flex-1 break-words">{todo.content}</span>
                          </li>
                        ))}
                      </ul>
                      <TablePagination {...todoPager.paginationProps} />
                    </div>
                  ) : (
                    <p className="text-muted-foreground text-xs">当前无 Todo 条目。</p>
                  )}
                </>
              )}
            </CardContent>
          </CollapsibleContent>
        </Card>
      </Collapsible>
      <ExplorationTrackingCard key={taskId} taskId={taskId} />
      <TaskInterceptRulesCard taskId={taskId} />
      {coverage?.enabled && coverage.scope_rows > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <TargetIcon className="size-4 text-emerald-500" /> 资产测试覆盖度
              <span className="font-normal text-muted-foreground text-xs">（粗估，仅供参考）</span>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-baseline gap-3">
              <span className="font-semibold text-2xl tabular-nums">
                {coverage.pct != null ? `${Math.round(coverage.pct * 100)}%` : "—"}
              </span>
              <span className="text-muted-foreground text-sm">
                已测 {coverage.tested} / 范围内 {coverage.denominator}
              </span>
            </div>
            {coverage.pct != null && <Progress value={Math.round(coverage.pct * 100)} />}
            {coverage.by_type.length > 0 && (
              <div className="flex flex-wrap gap-1.5 text-xs">
                {coverage.by_type.map((b) => (
                  <span key={b.type} className="rounded bg-muted px-1.5 py-0.5">
                    <span className="text-muted-foreground">{b.type}</span>{" "}
                    <span className="font-medium tabular-nums">
                      {b.tested}/{b.total}
                    </span>
                  </span>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}
      {/* LLM Token 用量：按模型分组，数据来自 llm_records（需开启 LLM 录制）。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <CoinsIcon className="size-4 text-amber-500" /> LLM Token 用量
            <span className="font-normal text-muted-foreground text-xs">
              （按模型统计{tokenTotals.calls > 0 ? `，共 ${tokenTotals.calls} 次调用` : ""}）
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {tokenBudget > 0 && (
            <div className="flex flex-col gap-1.5">
              <div className="flex items-baseline justify-between text-xs">
                <span className="text-muted-foreground">任务 Token 预算（全局设置）</span>
                <span className="tabular-nums">
                  {fmtTokens(budgetUsed)} / {fmtTokens(tokenBudget)}（{Math.round(budgetPct)}%）
                </span>
              </div>
              <div className="relative h-2 overflow-hidden rounded-full bg-muted">
                <div
                  className={`h-full rounded-full ${budgetBarTone(budgetPct)}`}
                  style={{ width: `${Math.min(100, budgetPct)}%` }}
                />
                {[70, 85, 95].map((p) => (
                  <span key={p} className="absolute top-0 h-full w-px bg-background/80" style={{ left: `${p}%` }} />
                ))}
              </div>
              <span className="text-[10px] text-muted-foreground">
                到 70% 提示收敛 / 85% 只保高价值方向 / 95% 告急 / 100% 自动进入收尾（各档在会话时间线留痕）。
              </span>
            </div>
          )}
          {tokenBudget <= 0 && (
            <span className="text-[10px] text-muted-foreground">
              未设任务 Token 预算（系统设置页可开启）：开启后到 70/85/95% 向规划者投递收敛提示，100% 自动收尾。
            </span>
          )}
          {modelTokens.length > 0 ? (
            <>
              {/* 合计总览 */}
              <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-sm">
                <span className="tabular-nums">
                  <span className="text-muted-foreground">输入 </span>
                  <span className="font-semibold">{fmtTokens(tokenTotals.input)}</span>
                </span>
                <span className="tabular-nums">
                  <span className="text-muted-foreground">输出 </span>
                  <span className="font-semibold">{fmtTokens(tokenTotals.output)}</span>
                </span>
                <span className="tabular-nums">
                  <span className="text-muted-foreground">缓存读 </span>
                  <span className="font-semibold">{fmtTokens(tokenTotals.cacheRead)}</span>
                </span>
                <span className="tabular-nums">
                  <span className="text-muted-foreground">未命中输入 </span>
                  <span className="font-semibold">
                    {fmtTokens(Math.max(0, tokenTotals.input - tokenTotals.cacheRead))}
                  </span>
                </span>
                <span className="tabular-nums">
                  <span className="text-muted-foreground">缓存命中率 </span>
                  <span className="font-semibold text-emerald-500">
                    {cacheHitRate(tokenTotals.cacheRead, tokenTotals.input)}
                  </span>
                </span>
                <span className="tabular-nums">
                  <span className="text-muted-foreground">LLM 成本 </span>
                  <span
                    className="font-semibold"
                    title={
                      tokenTotals.unpricedCount > 0
                        ? "存在未配置单价的模型，可在「系统 → LLM 配置」中补充费用单价"
                        : "按模型配置单价（含峰/谷时段）估算"
                    }
                  >
                    {tokenTotals.pricedCount === 0
                      ? "未定价"
                      : tokenTotals.unpricedCount > 0
                        ? `${fmtCostUSD(tokenTotals.costUsd, true)}（含未定价）`
                        : fmtCostUSD(tokenTotals.costUsd, true)}
                  </span>
                </span>
              </div>
              <p className="text-[10px] text-muted-foreground">
                「输入」已包含缓存读；DeepSeek 为自动前缀缓存，没有缓存写入计费（cache write 恒为 0）。命中率 = 缓存读 /
                输入；成本按「系统 → LLM 配置」模型单价（含峰/谷时段）估算。
              </p>
              {/* 按模型明细表 */}
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b text-left text-muted-foreground text-xs">
                      <th className="py-1.5 pr-3 font-medium">模型</th>
                      <th className="py-1.5 pr-3 text-right font-medium">调用</th>
                      <th className="py-1.5 pr-3 text-right font-medium">输入</th>
                      <th className="py-1.5 pr-3 text-right font-medium">输出</th>
                      <th className="py-1.5 pr-3 text-right font-medium">缓存读</th>
                      <th className="py-1.5 pr-3 text-right font-medium">未命中</th>
                      <th className="py-1.5 pr-3 text-right font-medium">命中率</th>
                      <th className="py-1.5 text-right font-medium">成本</th>
                    </tr>
                  </thead>
                  <tbody>
                    {modelTokens.map((m) => (
                      <tr key={m.model} className="border-b last:border-0">
                        <td className="max-w-[16rem] truncate py-1.5 pr-3 font-mono text-xs" title={m.model}>
                          {m.model}
                        </td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{m.calls}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtTokens(m.input_tokens)}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtTokens(m.output_tokens)}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtTokens(m.cache_read_tokens)}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">
                          {fmtTokens(Math.max(0, m.input_tokens - m.cache_read_tokens))}
                        </td>
                        <td className="py-1.5 pr-3 text-right text-emerald-500 tabular-nums">
                          {cacheHitRate(m.cache_read_tokens, m.input_tokens)}
                        </td>
                        <td className="py-1.5 text-right font-medium tabular-nums">
                          {m.priced ? (
                            fmtCostUSD(m.cost_usd ?? 0, true)
                          ) : (
                            <span className="font-normal text-muted-foreground" title="未配置模型单价">
                              未定价
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          ) : (
            <p className="text-muted-foreground text-sm">暂无 LLM 用量（任务尚未产生调用，或记录仍在写入）。</p>
          )}
        </CardContent>
      </Card>
      {/* 测试范围：覆盖度分母 + 授权边界，可手动增删。 */}
      <CollapsibleOverviewCard
        title="测试范围"
        icon={<ShieldCheckIcon className="size-4 text-emerald-500" />}
        meta={<span className="font-normal text-muted-foreground text-xs">（共 {scope.length} 条）</span>}
        description={showScope ? "覆盖度分母与授权边界，可手动增删。" : "已收起，点击右侧「查看测试范围」展开。"}
        open={showScope}
        onOpenChange={setShowScope}
        openLabel="收起测试范围"
        closedLabel="查看测试范围"
      >
        {/* 新增表单 */}
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect
            size="sm"
            value={scopeKind}
            onChange={(e) => setScopeKind(e.target.value as TaskScopeRow["kind"])}
          >
            <NativeSelectOption value="root_domain">根域名</NativeSelectOption>
            <NativeSelectOption value="subdomain">子域名</NativeSelectOption>
            <NativeSelectOption value="ip">IP</NativeSelectOption>
            <NativeSelectOption value="cidr">网段</NativeSelectOption>
            <NativeSelectOption value="icp">ICP</NativeSelectOption>
            <NativeSelectOption value="keyword">关键词</NativeSelectOption>
            <NativeSelectOption value="company">公司</NativeSelectOption>
          </NativeSelect>
          <Input
            className="h-7 w-56 text-sm"
            placeholder={
              scopeKind === "company"
                ? "公司名或 id"
                : scopeKind === "ip" || scopeKind === "cidr"
                  ? "IP：10.0.0.1；网段：10.0.0.0/24"
                  : scopeKind === "icp"
                    ? "如 京ICP备12345678号-1"
                    : scopeKind === "keyword"
                      ? "如 企业名称关键词"
                      : "如 example.com"
            }
            value={scopeValueInput}
            onChange={(e) => setScopeValueInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void addScope();
            }}
            disabled={scopeBusy}
          />
          <Button
            size="sm"
            variant="outline"
            disabled={scopeBusy || !scopeValueInput.trim()}
            onClick={() => void addScope()}
          >
            <PlusIcon className="size-3.5" /> 添加
          </Button>
          {scopeErr && <span className="text-red-500 text-xs">{scopeErr}</span>}
        </div>
        {(scopeKind === "ip" || scopeKind === "cidr") && (
          <p className="text-muted-foreground text-xs">
            IP 保存为单主机 /32；网段按网络地址保存（192.168.1.1/24 → 192.168.1.0/24）。
          </p>
        )}
        {/* 范围列表 */}
        {scope.length > 0 ? (
          <div className="flex flex-col gap-1.5">
            {scopePager.pageItems.map((row) => (
              <div key={row.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-muted-foreground text-xs">
                  {SCOPE_KIND_LABELS[row.kind]}
                </span>
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{scopeValue(row)}</span>
                <span className="shrink-0 text-muted-foreground text-xs">{SCOPE_SOURCE_LABELS[row.source]}</span>
                {row.task_id.toString() === taskId ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    onClick={() => void removeScope(row)}
                  >
                    <Trash2Icon className="size-3.5 text-red-500" />
                  </Button>
                ) : (
                  <span className="shrink-0 text-muted-foreground text-xs">继承</span>
                )}
              </div>
            ))}
            <TablePagination {...scopePager.paginationProps} />
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">暂无测试范围，添加后可作为资产覆盖度的分母。</p>
        )}
      </CollapsibleOverviewCard>
      {/* Heartbeat */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ActivityIcon className="size-4 text-blue-500" /> 心跳
          </CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <div>
            <div className="text-muted-foreground text-xs">引擎态</div>
            <StatusBadge
              domain="engine"
              value={stats?.engine_mode ?? task?.engine_mode ?? "idle"}
              dot
              className="mt-1"
            />
          </div>
          <div>
            <div className="text-muted-foreground text-xs">运行中 Worker</div>
            <div className="mt-1 font-semibold text-lg tabular-nums">{running.length}</div>
          </div>
          <div>
            <div className="text-muted-foreground text-xs">最近活动</div>
            <div className="mt-1 inline-flex items-center gap-1 text-sm">
              <ClockIcon className="size-3.5" />
              {task?.last_activity ? new Date(task.last_activity).toLocaleTimeString("zh-CN") : "—"}
            </div>
          </div>
          <div>
            <div className="text-muted-foreground text-xs">
              目标 {task?.goals_met ?? 0}/{task?.goals_total ?? 0}
            </div>
            <Progress value={goalsPct} className="mt-2" />
          </div>
          {task?.completed_unix && task.completed_unix > 0 ? (
            <div>
              <div className="text-muted-foreground text-xs">完成时间</div>
              <div className="mt-1 inline-flex items-center gap-1 text-sm">
                <ClockIcon className="size-3.5" />
                {new Date(task.completed_unix * 1000).toLocaleString("zh-CN")}
              </div>
            </div>
          ) : null}
        </CardContent>
      </Card>

      {/* Work set */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <TargetIcon className="size-4" /> 进行中意图
              {running.length > 0 && (
                <span className="font-normal text-muted-foreground text-xs">（{running.length}）</span>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {runningPager.pageItems.map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="shrink-0 font-mono text-muted-foreground text-xs">#{i.id}</span>
                <span className="min-w-0 flex-1 truncate">{nodeSummary(i) || "(无摘要)"}</span>
              </div>
            ))}
            {running.length === 0 && <p className="text-muted-foreground text-sm">暂无进行中意图</p>}
            <TablePagination
              {...runningPager.paginationProps}
              compactNumbers
              summaryLabel={`共 ${running.length} 条`}
            />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-amber-500" /> 需要关注
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <div className="grid grid-cols-2 gap-3">
              <div>
                <div className="font-semibold text-2xl text-red-600 tabular-nums">{totalFindings}</div>
                <div className="text-muted-foreground text-xs">确认漏洞</div>
              </div>
              <div>
                <div className="font-semibold text-2xl text-blue-600 tabular-nums">{totalRunning}</div>
                <div className="text-muted-foreground text-xs">执行中</div>
              </div>
              <div>
                <div className="font-semibold text-2xl tabular-nums">{totalOpen}</div>
                <div className="text-muted-foreground text-xs">frontier 待领</div>
              </div>
              <div>
                <div className="font-semibold text-2xl text-red-600 tabular-nums">
                  {totalBlocked + totalExhausted + totalStopped + totalNoOutput}
                </div>
                <div className="text-muted-foreground text-xs">异常/中断意图</div>
              </div>
            </div>
            {(totalBlocked > 0 || totalExhausted > 0 || totalStopped > 0 || totalNoOutput > 0) && (
              <div className="border-t pt-2 text-muted-foreground text-xs">
                出错 {totalBlocked} · 耗尽 {totalExhausted} · 停止 {totalStopped} · 无产出 {totalNoOutput}
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <BugIcon className="size-4 text-red-500" /> 最近发现
              {totalFindings > 0 && (
                <span className="font-normal text-muted-foreground text-xs">（{totalFindings}）</span>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {findingsPager.pageItems.map((f) => (
              <div key={f.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="severity" value={f.severity} dot />
                <span className="min-w-0 flex-1 truncate">{f.summary}</span>
              </div>
            ))}
            {totalFindings === 0 && <p className="text-muted-foreground text-sm">暂无发现</p>}
            <TablePagination
              {...findingsPager.paginationProps}
              compactNumbers
              summaryLabel={`共 ${totalFindings} 条`}
            />
          </CardContent>
        </Card>
      </div>

      {/* 依赖阻塞：open 但 strict 前置仍活跃。它不是执行失败，不能和下面的
          state=blocked（出错/被拦）混为一谈；planner 会被看门狗唤醒解阻。 */}
      {blockedByDeps.length > 0 && (
        <Card className="border-amber-500/30">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <ClockIcon className="size-4 text-amber-500" /> 依赖阻塞意图
              <span className="font-normal text-muted-foreground text-xs">
                （共 {totalBlockedByDeps} 条，等待前置产出）
              </span>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {blockedByDepsPager.pageItems.map((i) => (
              <div key={i.intent_id} className="rounded-md border border-amber-500/20 bg-amber-500/5 px-3 py-2">
                <div className="flex items-center gap-2">
                  <span className="shrink-0 font-mono text-xs text-muted-foreground">#{i.intent_id}</span>
                  <span className="min-w-0 flex-1 truncate text-sm">{i.summary || "(无摘要)"}</span>
                </div>
                <div className="mt-1 text-muted-foreground text-xs">
                  前置：{i.blockers?.length ? i.blockers.map((b) => `#${b.id} ${b.state}`).join("、") : "未知"}
                  {i.depends_on?.length ? `（共 ${i.depends_on.length} 条依赖）` : ""}
                </div>
              </div>
            ))}
            <TablePagination {...blockedByDepsPager.paginationProps} />
            {totalBlockedByDeps > blockedByDeps.length && (
              <p className="text-muted-foreground text-xs">
                统计接口仅返回前 {blockedByDeps.length} 条依赖阻塞样本；其余等 planner 处理后刷新。
              </p>
            )}
          </CardContent>
        </Card>
      )}

      {/* Blocked intents — 出错/被拦(如 LLM 网络问题)的意图，可一键重跑：置回 open，
          worker 会重新认领、从头再跑（已写回图谱的数据保留）；任务若已终态/暂停会自动复活。 */}
      {blocked.length > 0 && (
        <Card className="border-red-500/30">
          <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-red-500" /> 被拦/出错意图
              <span className="font-normal text-muted-foreground text-xs">（共 {blocked.length} 条，可重跑）</span>
            </CardTitle>
            <Button size="sm" variant="outline" disabled={rerunning.has("__all__")} onClick={() => void rerunAll()}>
              <RefreshCwIcon className={`size-3.5 ${rerunning.has("__all__") ? "animate-spin" : ""}`} />
              全部重跑
            </Button>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {blockedPager.pageItems.map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="shrink-0 font-mono text-muted-foreground text-xs">#{i.id}</span>
                <span className="min-w-0 flex-1 truncate">{nodeSummary(i) || "(无摘要)"}</span>
                {i.blocked_reason && (
                  <Badge variant="outline" className="shrink-0 text-[10px]">
                    {i.blocked_reason}
                  </Badge>
                )}
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 shrink-0 px-2 text-xs"
                  disabled={rerunning.has(i.id)}
                  onClick={() => void rerunOne(i.id)}
                >
                  <RefreshCwIcon className={`size-3 ${rerunning.has(i.id) ? "animate-spin" : ""}`} />
                  重跑
                </Button>
              </div>
            ))}
            <TablePagination {...blockedPager.paginationProps} />
          </CardContent>
        </Card>
      )}

      {/* 终态但仍有未执行意图：任务因目标达成/收束而结束，frontier 里却留着从未跑过的
          方向（规划者已停跑，没人再领）。明确解释状态 + 一键继续运行。 */}
      {taskIsTerminal(task?.status ?? "") && open.length > 0 && (
        <Card className="border-amber-500/40">
          <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-amber-500" />
              任务已结束{task?.status === "done" ? "（目标已达成）" : ""}，但仍有 {open.length} 条意图从未执行
            </CardTitle>
            <Button size="sm" disabled={resuming} onClick={() => void resumeTerminal()}>
              <PlayIcon className={`size-3.5 ${resuming ? "animate-pulse" : ""}`} />
              继续运行
            </Button>
          </CardHeader>
          <CardContent>
            <p className="text-muted-foreground text-xs">
              这些方向还在 frontier 里：点「继续运行」让 worker
              重新领取执行（已写回图谱的事实与漏洞保留）；跑完、frontier 抽干后任务会再次收束。
            </p>
          </CardContent>
        </Card>
      )}

      {/* Stat cards */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">
        <StatCard label="待领意图" value={totalOpen} icon={ShieldCheckIcon} sub="可领（前置已满足）" />
        <StatCard label="确认发现" value={totalFindings} icon={BugIcon} sub="本任务" />
        <StatCard label="意图总数" value={totalIntents} icon={AlertTriangleIcon} sub="不含已丢弃" />
      </div>
    </div>
  );
}
