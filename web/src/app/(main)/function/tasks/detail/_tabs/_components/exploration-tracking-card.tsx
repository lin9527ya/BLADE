"use client";

import * as React from "react";
import { ClockIcon, GitBranchIcon, ListOrderedIcon, RefreshCwIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import {
  LEAD_TRACE_LABELS,
  LEAD_TRACE_PAGE_SIZE,
  schedulingScore,
  scheduleWaitLabel,
  trackingDuration,
  type LeadTraceReport,
  type LeadTraceState,
  type ScheduleExplanation,
  type SchedulingReport,
} from "@/lib/exploration-tracking";

function traceTime(value?: string): string {
  if (!value || !Number.isFinite(new Date(value).getTime())) return "未知";
  return new Date(value).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai", hour12: false });
}
function ScoreDetails({ row }: { row: ScheduleExplanation }) {
  const names = {
    priority: "优先级 ×100",
    dependency: "advisory 依赖",
    restart: "重启续跑",
    route: "路线均衡",
    aging: "等待加分",
    retries: "重试扣分",
  };
  return (
    <div className="space-y-1 text-xs">
      <p>评分时间：{traceTime(row.evaluated_at)}（北京时间）</p>
      {row.evaluated ? (
        <>
          <div className="flex flex-wrap gap-x-3 gap-y-1">
            {Object.entries(names).map(([key, label]) => (
              <span key={key}>{label}：<b className="tabular-nums">{row.components[key as keyof typeof names].toFixed(1)}</b></span>
            ))}
          </div>
          <p>路线：{row.route || "—"}；本次候选中 {row.route_count}/{row.largest_route_count}（该路线数/最多路线数）</p>
          <p>{row.fair_wait ? "等待已超过 15 分钟：先按最老方向排序，再比较分值。" : "常规按总分降序，同分按 ID 排序。"}</p>
        </>
      ) : <p>未进入本次评分，不用 0 分表示未知或阻塞。</p>}
    </div>
  );
}

export function ExplorationTrackingCard({ taskId }: { taskId: string }) {
  const [showSchedule, setShowSchedule] = React.useState(false);
  const [schedule, setSchedule] = React.useState<SchedulingReport | null>(null);
  const [leads, setLeads] = React.useState<LeadTraceReport | null>(null);
  const [error, setError] = React.useState("");
  const [focusDraft, setFocusDraft] = React.useState("");
  const [focus, setFocus] = React.useState("");
  const [state, setState] = React.useState("");
  const [offset, setOffset] = React.useState(0);
  const [refresh, setRefresh] = React.useState(0);
  React.useEffect(() => {
    setSchedule(null);
  }, [taskId, focus, state, offset, showSchedule]);
  React.useEffect(() => {
    setLeads(null);
  }, [taskId, focus, state, offset]);
  usePolling(async (alive) => {
    try {
      const [nextSchedule, nextLeads] = await Promise.all([
        showSchedule ? api.taskScheduling(taskId, focus || undefined) : Promise.resolve(null),
        api.taskLeadTraces(taskId, state, offset),
      ]);
      if (alive()) { setSchedule(nextSchedule); setLeads(nextLeads); setError(""); }
    } catch (e) {
      if (alive()) setError((e as Error).message);
      throw e;
    }
  }, 10_000, [taskId, focus, state, offset, refresh, showSchedule]);

  function search() {
    const value = focusDraft.trim();
    if (value && !/^[1-9]\d*$/.test(value)) { setError("意图 ID 必须是正整数"); return; }
    setFocus(value);
    setRefresh((n) => n + 1);
  }
  return (
    <div className="space-y-4">
      {error && <p role="alert" className="text-sm text-muted-foreground">调度/追踪读取失败：{error}</p>}
      <Collapsible open={showSchedule} onOpenChange={setShowSchedule}>
        <Card>
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <CardTitle className="flex items-center gap-2 text-base"><ListOrderedIcon className="size-4" /> 调度解释</CardTitle>
              <CollapsibleTrigger asChild>
                <Button size="sm" variant="outline">{showSchedule ? "收起调度解释" : "查看调度解释"}</Button>
              </CollapsibleTrigger>
            </div>
            <CardDescription>{showSchedule ? "与 worker 共用评分实现；当前快照不保证下一刻排名不变。候选排名与任务运行门分开显示。" : "默认收起，按需查看候选排名、评分分解与等待原因。"}</CardDescription>
          </CardHeader>
          <CollapsibleContent>
            <CardContent className="space-y-3">
              <div className="flex flex-wrap items-center gap-2">
                <Input aria-label="查询意图 ID" placeholder="意图 ID；留空查看候选" className="h-8 w-52" value={focusDraft} onChange={(e) => setFocusDraft(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") search(); }} />
                <Button size="sm" variant="outline" onClick={search}>查询</Button>
                <Button size="sm" variant="ghost" onClick={() => setRefresh((n) => n + 1)}><RefreshCwIcon className="size-3.5" />刷新</Button>
              </div>
              {!schedule ? <p className="text-sm text-muted-foreground">正在读取…</p> : (
                <>
                  <p className="text-sm">待领 {schedule.open_total} · 依赖条件满足 {schedule.claimable_total} · 本次评分候选 {schedule.candidate_count} · 工作中 {schedule.active_works}/{schedule.worker_slots || "—"}</p>
                  {schedule.gate_reason && <p className="text-sm text-muted-foreground">当前运行门：{scheduleWaitLabel(schedule.gate_reason)}。下方是常规候选排名，不代表现在会执行。</p>}
                  {schedule.dispatch_mode === "goal_drain" && <p className="text-sm text-muted-foreground">达标收尾：常规排名暂不用于领取，仅 #{schedule.continuation_ids?.join("、#") || "无"} 可续读原工作输出。</p>}
                  <Table>
                    <TableHeader><TableRow><TableHead>排名 / ID</TableHead><TableHead>方向</TableHead><TableHead>得分</TableHead><TableHead>等待与原因</TableHead></TableRow></TableHeader>
                    <TableBody>{schedule.rows.map((row) => (
                      <TableRow key={row.intent_id}>
                        <TableCell className="align-top font-mono text-xs">{row.rank ?? "—"} / #{row.intent_id}</TableCell>
                        <TableCell className="min-w-48 whitespace-normal align-top">{row.summary || "(无摘要)"}<div className="mt-1"><Badge variant="outline">{row.state}</Badge></div></TableCell>
                        <TableCell className="align-top tabular-nums">{schedulingScore(row)}</TableCell>
                        <TableCell className="min-w-56 whitespace-normal align-top text-xs">
                          <p>自创建：{trackingDuration(row.waiting_seconds)}</p>
                          <p className="mt-1 text-muted-foreground">{row.wait_reasons?.length ? row.wait_reasons.map(scheduleWaitLabel).join("；") : "候选已就绪，等待空闲槽位领取"}</p>
                          <details className="mt-2"><summary className="cursor-pointer">评分分解</summary><div className="mt-2"><ScoreDetails row={row} /></div></details>
                        </TableCell>
                      </TableRow>
                    ))}</TableBody>
                  </Table>
                  {!schedule.rows.length && <p className="text-sm text-muted-foreground">暂无待领候选。</p>}
                  {!focus && schedule.open_total > schedule.rows.length && <p className="text-xs text-muted-foreground">仅展示前 {schedule.rows.length} 条。用意图 ID 可查询窗口外、阻塞意图及历史快照。</p>}
                  {schedule.last_dispatch && <details><summary className="cursor-pointer text-sm">上次保存的常规领取评分（不是当前重算）</summary><div className="mt-2"><ScoreDetails row={schedule.last_dispatch} /></div></details>}
                </>
              )}
            </CardContent>
          </CollapsibleContent>
        </Card>
      </Collapsible>
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base"><GitBranchIcon className="size-4" /> 分叉接手与执行追踪</CardTitle>
          <CardDescription>L 为旁路线索，P 为意图提案；关联执行结束不等于漏洞已验证。历史缺时间时保持未知，不算作零延迟。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <NativeSelect aria-label="筛选线索状态" value={state} onChange={(e) => { setState(e.target.value); setOffset(0); }} className="max-w-xs" size="sm">
            <NativeSelectOption value="">全部状态</NativeSelectOption>
            {Object.entries(LEAD_TRACE_LABELS).map(([value, label]) => <NativeSelectOption key={value} value={value}>{label}</NativeSelectOption>)}
          </NativeSelect>
          {!leads ? <p className="text-sm text-muted-foreground">正在读取…</p> : (
            <>
              <p className="text-sm">全任务 {leads.stats.total} 条 · 待接手 {leads.stats.pending} · 曾接手 {leads.stats.adopted} · 执行中 {leads.stats.running} · 需跟进 {leads.stats.needs_follow_up}</p>
              <p className="text-xs text-muted-foreground">来源事实→接手平均 {trackingDuration(leads.stats.average_adoption_seconds)}（{leads.stats.adoption_samples} 个已知样本）；来源事实→关联意图首次执行平均 {trackingDuration(leads.stats.average_execution_seconds)}（{leads.stats.execution_samples} 个已知样本）。</p>
              <Table>
                <TableHeader><TableRow><TableHead>分叉 / 来源</TableHead><TableHead>观察或方向</TableHead><TableHead>当前追踪状态</TableHead><TableHead>接手时间与执行历史</TableHead></TableRow></TableHeader>
                <TableBody>{leads.rows.map((lead) => (
                  <TableRow key={lead.id}>
                    <TableCell className="align-top font-mono text-xs">{lead.id}<p>fact #{lead.fact_id}</p></TableCell>
                    <TableCell className="min-w-48 whitespace-normal align-top">{lead.text}{lead.source_retired && <p className="text-xs text-muted-foreground">来源事实已废弃</p>}</TableCell>
                    <TableCell className="align-top"><Badge variant="outline">{LEAD_TRACE_LABELS[lead.state as LeadTraceState] ?? lead.state}</Badge></TableCell>
                    <TableCell className="min-w-64 whitespace-normal align-top text-xs">
                      <p>接手：{traceTime(lead.adopted_at)}</p><p>首次执行：{traceTime(lead.first_started_at)}</p>
                      <details className="mt-2"><summary className="cursor-pointer">来源与 {lead.intent_total} 条关联意图</summary>
                        <p className="mt-2 text-muted-foreground"><ClockIcon className="mr-1 inline size-3" />来源事实：{traceTime(lead.fact_created_at)}（北京时间）</p>
                        {!lead.intents.length && <p className="mt-2 text-muted-foreground">暂无明确关联；不因事实有其它子意图就推断本分叉已接手。</p>}
                        {lead.intents.map((intent) => <div key={intent.intent_id} className="mt-2 space-y-1 border-t pt-2">
                          <p>#{intent.intent_id} [{intent.state}] {intent.summary}</p>
                          <p className="text-muted-foreground">关联方式：{intent.match === "explicit" ? "显式 ID" : intent.match === "exact" ? "唯一原文匹配" : "历史关联（时间可能未知）"}</p>
                          <p>接手 {traceTime(intent.linked_at)} · 领取 {traceTime(intent.first_claimed_at)}</p>
                          <p>执行 {traceTime(intent.first_started_at)} · 结束 {traceTime(intent.completed_at)}</p>
                          {intent.source_changed && <p>来源文本已变化，旧结果不能当作新分叉的结论。原关联文本：{intent.text_snapshot}</p>}
                          {intent.last_dispatch && <details><summary className="cursor-pointer">当时的常规调度评分</summary><ScoreDetails row={intent.last_dispatch} /></details>}
                        </div>)}
                        {lead.history_truncated && <p className="text-muted-foreground">关联总数 {lead.intent_total}，仅展示最近 20 条；其余可通过任务意图历史按 ID 回查。</p>}
                      </details>
                    </TableCell>
                  </TableRow>
                ))}</TableBody>
              </Table>
              {!leads.rows.length && <p className="text-sm text-muted-foreground">当前筛选没有结构化分叉记录。普通 fact 中未拆出的文字不会被猜测成已追踪线索。</p>}
              <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span>当前筛选 {leads.total} 条 · 每页 {LEAD_TRACE_PAGE_SIZE} 条 · 第 {Math.floor(offset / LEAD_TRACE_PAGE_SIZE) + 1} 页</span>
                <div className="flex gap-2"><Button size="sm" variant="outline" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - LEAD_TRACE_PAGE_SIZE))}>上一页</Button><Button size="sm" variant="outline" disabled={!leads.has_more} onClick={() => setOffset(offset + LEAD_TRACE_PAGE_SIZE)}>下一页</Button></div>
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
