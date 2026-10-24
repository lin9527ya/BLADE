"use client";

import * as React from "react";
import { TaskInterceptRulesCard, TaskExplorationPolicyCard } from "./_components/task-policy-cards";

import { ActivityIcon, AlertTriangleIcon, BugIcon, CheckIcon, ClockIcon, CoinsIcon, ListChecksIcon, PencilIcon, PlayIcon, PlusIcon, RefreshCwIcon, ShieldAlertIcon, ShieldCheckIcon, TargetIcon, Trash2Icon, XIcon } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Progress } from "@/components/ui/progress";

import { taskIsTerminal } from "@/lib/status";
import { TaskConstraint, TaskScopeRow } from "@/lib/types";

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
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + "k";
  return String(n);
}

// 缓存命中率 = 缓存读 / 输入（InputTokens 已含 cache_read 子集，故比值在 0–100%）。
function cacheHitRate(cacheRead: number, input: number): string {
  if (input <= 0) return "—";
  return Math.round((cacheRead / input) * 100) + "%";
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
      {sub && <CardContent className="text-xs text-muted-foreground">{sub}</CardContent>}
    </Card>
  );
}

export function OverviewTab({ taskId }: { taskId: string }) {
  const {
    task,
    setTask,
    stats,
    setStats,
    intents,
    setIntents,
    findings,
    setFindings,
    coverage,
    setCoverage,
    rerunning,
    setRerunning,
    resuming,
    setResuming,
    scope,
    setScope,
    scopeKind,
    setScopeKind,
    scopeValueInput,
    setScopeValueInput,
    scopeBusy,
    setScopeBusy,
    scopeErr,
    setScopeErr,
    modelTokens,
    setModelTokens,
    tokenBudget,
    setTokenBudget,
    goals,
    setGoals,
    goalText,
    setGoalText,
    goalVuln,
    setGoalVuln,
    goalBusy,
    setGoalBusy,
    goalErr,
    setGoalErr,
    editingGoalId,
    setEditingGoalId,
    editText,
    setEditText,
    editVuln,
    setEditVuln,
    constraints,
    setConstraints,
    conText,
    setConText,
    conKind,
    setConKind,
    conBusy,
    setConBusy,
    conErr,
    setConErr,
    editingConId,
    setEditingConId,
    editConText,
    setEditConText,
    editConKind,
    setEditConKind,
    compileFor,
    setCompileFor,
    compileResult,
    setCompileResult,
    compileBusy,
    setCompileBusy,
    loadTokens,
    loadScope,
    loadGoals,
    addGoal,
    startEditGoal,
    cancelEditGoal,
    saveEditGoal,
    removeGoal,
    loadConstraints,
    addConstraint,
    startEditConstraint,
    cancelEditConstraint,
    saveEditConstraint,
    removeConstraint,
    openCompile,
    applyCompile,
    addScope,
    removeScope,
    markRerun,
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
  } = useOverviewState(taskId);

  // 精确计数：列表接口有行数上限（意图 300 / 发现 200），len(列表) 是窗口长度不是总数。
  const totalFindings = stats?.exploration?.finding ?? taskFindings.length;
  const totalIntents = stats?.exploration?.intent ?? intents.length;
  const totalOpen = stats?.claimable_intents ?? open.length;
  const totalRunning = stats?.intent_states?.running ?? running.length;
  const totalBlocked = stats?.intent_states?.blocked ?? blocked.length;

  return (
    <div className="flex flex-col gap-4">
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
          </CardContent>
        </Card>
      )}
      {/* 原始任务描述与目标(创建时填写的),置顶便于随时回看。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <TargetIcon className="size-4 text-primary" /> 任务描述与目标
          </CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <div className="text-xs font-medium text-muted-foreground">描述</div>
            <p className="text-sm whitespace-pre-wrap break-words">{task?.description?.trim() || "—"}</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <div className="text-xs font-medium text-muted-foreground">目标</div>
            <p className="text-sm whitespace-pre-wrap break-words">{task?.goal?.trim() || "—"}</p>
          </div>
        </CardContent>
      </Card>
      {/* 目标管理：查看/新增/修改/删除本任务的探索目标。新增与修改会通知规划者并复活任务，
          删除仅通知规划者（不复活）。目标 = 最终可交付/可核验的结果，不是攻击步骤或侦察动作。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ListChecksIcon className="size-4 text-primary" /> 目标管理
            <span className="text-muted-foreground text-xs font-normal">
              （最终可核验的目标，共 {goals.length} 条；新增/修改会通知规划者并复活任务）
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
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
            {goalErr && <span className="text-xs text-red-500">{goalErr}</span>}
          </div>
          {/* 目标列表 */}
          {goals.length > 0 ? (
            <div className="flex flex-col gap-1.5">
              {goals.map((g) =>
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
                      <span className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-0.5 text-xs">
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
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">暂无目标，添加后规划者会据此派发探索意图并判定达成。</p>
          )}
        </CardContent>
      </Card>
      {/* 操作约束管理：allow=允许 / deny=禁止。约束会在下一轮规划时注入 planner/worker 的系统
          提示以框定探索边界（注入范围可在 系统设置 里按 planner/worker 开关）。改动不即时打断，
          下一轮规划自然读到。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ShieldAlertIcon className="size-4 text-amber-500" /> 操作约束
            <span className="text-muted-foreground text-xs font-normal">
              （共 {constraints.length} 条；默认只作提示词约束注入 planner/worker，改动下一轮规划生效。
              点盾牌图标可把它编译成任务级拦截规则 —— 抽得到目标/工具名的约束会变成硬拦截，只对本任务生效）
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
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
            {conErr && <span className="text-xs text-red-500">{conErr}</span>}
          </div>
          {/* 约束列表 */}
          {constraints.length > 0 ? (
            <div className="flex flex-col gap-1.5">
              {constraints.map((c) =>
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
                              compileBusy ||
                              !((compileResult?.rules?.length ?? 0) + (compileResult?.scope?.length ?? 0))
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
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">
              暂无操作约束。建任务时会自动从描述/目标抽取；也可在此手动增删改，用来框定「允许/禁止做哪些操作」。
            </p>
          )}
        </CardContent>
      </Card>
      <TaskExplorationPolicyCard taskId={taskId} />
      <TaskInterceptRulesCard taskId={taskId} />
      {coverage && coverage.enabled && coverage.scope_rows > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <TargetIcon className="size-4 text-emerald-500" /> 资产测试覆盖度
              <span className="text-muted-foreground text-xs font-normal">（粗估，仅供参考）</span>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-baseline gap-3">
              <span className="text-2xl font-semibold tabular-nums">
                {coverage.pct != null ? Math.round(coverage.pct * 100) + "%" : "—"}
              </span>
              <span className="text-muted-foreground text-sm">
                已测 {coverage.tested} / 范围内 {coverage.denominator}
              </span>
            </div>
            {coverage.pct != null && <Progress value={Math.round(coverage.pct * 100)} />}
            {coverage.by_type.length > 0 && (
              <div className="flex flex-wrap gap-1.5 text-xs">
                {coverage.by_type.map((b) => (
                  <span key={b.type} className="bg-muted rounded px-1.5 py-0.5">
                    <span className="text-muted-foreground">{b.type}</span>{" "}
                    <span className="tabular-nums font-medium">
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
            <span className="text-muted-foreground text-xs font-normal">
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
              <div className="bg-muted relative h-2 overflow-hidden rounded-full">
                <div
                  className={`h-full rounded-full ${budgetBarTone(budgetPct)}`}
                  style={{ width: `${Math.min(100, budgetPct)}%` }}
                />
                {[70, 85, 95].map((p) => (
                  <span key={p} className="bg-background/80 absolute top-0 h-full w-px" style={{ left: `${p}%` }} />
                ))}
              </div>
              <span className="text-muted-foreground text-[10px]">
                到 70% 提示收敛 / 85% 只保高价值方向 / 95% 告急 / 100% 自动进入收尾（各档在会话时间线留痕）。
              </span>
            </div>
          )}
          {tokenBudget <= 0 && (
            <span className="text-muted-foreground text-[10px]">
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
                  <span className="text-muted-foreground">缓存命中率 </span>
                  <span className="font-semibold text-emerald-500">
                    {cacheHitRate(tokenTotals.cacheRead, tokenTotals.input)}
                  </span>
                </span>
              </div>
              {/* 按模型明细表 */}
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="text-muted-foreground border-b text-left text-xs">
                      <th className="py-1.5 pr-3 font-medium">模型</th>
                      <th className="py-1.5 pr-3 text-right font-medium">调用</th>
                      <th className="py-1.5 pr-3 text-right font-medium">输入</th>
                      <th className="py-1.5 pr-3 text-right font-medium">输出</th>
                      <th className="py-1.5 pr-3 text-right font-medium">缓存读</th>
                      <th className="py-1.5 text-right font-medium">命中率</th>
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
                        <td className="py-1.5 text-right tabular-nums text-emerald-500">
                          {cacheHitRate(m.cache_read_tokens, m.input_tokens)}
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
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ShieldCheckIcon className="size-4 text-emerald-500" /> 测试范围
            <span className="text-muted-foreground text-xs font-normal">
              （覆盖度分母 + 授权边界，共 {scope.length} 条）
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
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
                    ? "如 10.0.0.1 或 10.0.0.0/24"
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
            {scopeErr && <span className="text-xs text-red-500">{scopeErr}</span>}
          </div>
          {/* 范围列表 */}
          {scope.length > 0 ? (
            <div className="flex flex-col gap-1.5">
              {scope.map((row) => (
                <div key={row.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                  <span className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-0.5 text-xs">
                    {SCOPE_KIND_LABELS[row.kind]}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">{scopeValue(row)}</span>
                  <span className="text-muted-foreground shrink-0 text-xs">{SCOPE_SOURCE_LABELS[row.source]}</span>
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
                    <span className="text-muted-foreground shrink-0 text-xs">继承</span>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">暂无测试范围，添加后可作为资产覆盖度的分母。</p>
          )}
        </CardContent>
      </Card>
      {/* Heartbeat */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ActivityIcon className="size-4 text-blue-500" /> 心跳
          </CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <div>
            <div className="text-xs text-muted-foreground">引擎态</div>
            <StatusBadge
              domain="engine"
              value={stats?.engine_mode ?? task?.engine_mode ?? "idle"}
              dot
              className="mt-1"
            />
          </div>
          <div>
            <div className="text-xs text-muted-foreground">运行中 Worker</div>
            <div className="mt-1 text-lg font-semibold tabular-nums">{running.length}</div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">最近活动</div>
            <div className="mt-1 inline-flex items-center gap-1 text-sm">
              <ClockIcon className="size-3.5" />
              {task?.last_activity ? new Date(task.last_activity).toLocaleTimeString("zh-CN") : "—"}
            </div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">
              目标 {task?.goals_met ?? 0}/{task?.goals_total ?? 0}
            </div>
            <Progress value={goalsPct} className="mt-2" />
          </div>
          {task?.completed_unix && task.completed_unix > 0 ? (
            <div>
              <div className="text-xs text-muted-foreground">完成时间</div>
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
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {running.slice(0, 6).map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="min-w-0 flex-1 truncate">{i.payload}</span>
              </div>
            ))}
            {running.length === 0 && <p className="text-sm text-muted-foreground">暂无进行中意图</p>}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-amber-500" /> 需要关注
            </CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-2 gap-3 text-sm">
            <div>
              <div className="text-2xl font-semibold tabular-nums text-red-600">{totalFindings}</div>
              <div className="text-xs text-muted-foreground">确认漏洞</div>
            </div>
            <div>
              <div className="text-2xl font-semibold tabular-nums text-blue-600">{totalRunning}</div>
              <div className="text-xs text-muted-foreground">执行中</div>
            </div>
            <div>
              <div className="text-2xl font-semibold tabular-nums">{totalOpen}</div>
              <div className="text-xs text-muted-foreground">frontier 待领</div>
            </div>
            <div>
              <div className="text-2xl font-semibold tabular-nums text-red-600">{totalBlocked}</div>
              <div className="text-xs text-muted-foreground">被拦意图</div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <BugIcon className="size-4 text-red-500" /> 最近发现
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {taskFindings.slice(0, 6).map((f) => (
              <div key={f.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="severity" value={f.severity} dot />
                <span className="min-w-0 flex-1 truncate">{f.summary}</span>
              </div>
            ))}
            {totalFindings === 0 && <p className="text-sm text-muted-foreground">暂无发现</p>}
          </CardContent>
        </Card>
      </div>

      {/* Blocked intents — 出错/被拦(如 LLM 网络问题)的意图，可一键重跑：置回 open，
          worker 会重新认领、从头再跑（已写回图谱的数据保留）；任务若已终态/暂停会自动复活。 */}
      {blocked.length > 0 && (
        <Card className="border-red-500/30">
          <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-red-500" /> 被拦/出错意图
              <span className="text-xs font-normal text-muted-foreground">（共 {blocked.length} 条，可重跑）</span>
            </CardTitle>
            <Button size="sm" variant="outline" disabled={rerunning.has("__all__")} onClick={() => void rerunAll()}>
              <RefreshCwIcon className={`size-3.5 ${rerunning.has("__all__") ? "animate-spin" : ""}`} />
              全部重跑
            </Button>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {blocked.slice(0, 20).map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="min-w-0 flex-1 truncate">{i.payload}</span>
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
            {blocked.length > 20 && (
              <p className="text-xs text-muted-foreground">
                仅显示前 20 条，点「全部重跑」处理剩余 {blocked.length - 20} 条。
              </p>
            )}
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
        <StatCard label="意图总数" value={totalIntents} icon={AlertTriangleIcon} sub="本任务全部意图" />
      </div>
    </div>
  );
}

