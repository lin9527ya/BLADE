"use client";

import * as React from "react";

import { Card, CardContent, CardHeader } from "@/components/ui/card";
import {
  ActivityIcon,
  BugIcon,
  NetworkIcon,
  TargetIcon,
  ZapIcon,
} from "lucide-react";

import type { useDashboardState } from "./use-dashboard-state";
import { fmtTokens, LiveDot } from "./dashboard-helpers";

export function RowStatCards({ s }: { s: ReturnType<typeof useDashboardState> }) {
  const { tasks, findings, settings, traffic, tasksByStatus, findingsBySev, totalAssets, displayedTokens } = s;

  return (
    <>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        {/* 活跃任务 */}
        <Card className="gap-1">
          <CardHeader className="pb-0">
            <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
              <TargetIcon className="size-3" /> 活跃任务
            </div>
            <div className="flex items-baseline gap-1.5">
              <span className="text-2xl font-semibold tabular-nums">{tasksByStatus.running ?? 0}</span>
              <span className="text-xs text-muted-foreground">/ {tasks.length}</span>
            </div>
          </CardHeader>
          <CardContent className="flex flex-wrap gap-x-2.5 gap-y-0.5 text-[10px]">
            {(tasksByStatus.running ?? 0) > 0 && <span className="text-blue-400">探索 {tasksByStatus.running}</span>}
            {(tasksByStatus.paused ?? 0) > 0 && <span className="text-amber-400">暂停 {tasksByStatus.paused}</span>}
            {(tasksByStatus.done ?? 0) > 0 && <span className="text-emerald-400">完成 {tasksByStatus.done}</span>}
            {tasks.length === 0 && <span className="text-muted-foreground">暂无任务</span>}
          </CardContent>
        </Card>

        {/* 确认发现 */}
        <Card className="gap-1">
          <CardHeader className="pb-0">
            <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
              <BugIcon className="size-3" /> 确认发现
            </div>
            <div className="text-2xl font-semibold tabular-nums">{findings.length}</div>
          </CardHeader>
          <CardContent className="flex flex-wrap gap-2.5 text-[10px]">
            <span className="text-rose-500">严重 {findingsBySev.critical}</span>
            <span className="text-red-400">高危 {findingsBySev.high}</span>
            <span className="text-amber-400">中危 {findingsBySev.medium}</span>
            <span className="text-slate-400">低危 {findingsBySev.low}</span>
          </CardContent>
        </Card>

        {/* 资产节点 */}
        <Card className="gap-1">
          <CardHeader className="pb-0">
            <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
              <NetworkIcon className="size-3" /> 资产节点
            </div>
            <div className="text-2xl font-semibold tabular-nums">{totalAssets}</div>
          </CardHeader>
          <CardContent className="text-[10px] text-muted-foreground">跨任务共享</CardContent>
        </Card>

        {/* 流量交互 */}
        <Card className="gap-1">
          <CardHeader className="pb-0">
            <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
              <ActivityIcon className="size-3" /> 流量交互
            </div>
            <div className="text-2xl font-semibold tabular-nums">{traffic.length}</div>
          </CardHeader>
          <CardContent className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
            {settings?.traffic_capture ? (
              <>
                <LiveDot />
                <span>录制中</span>
              </>
            ) : (
              <span>捕获未开启</span>
            )}
          </CardContent>
        </Card>

        {/* LLM 用量 */}
        <Card className="gap-1">
          <CardHeader className="pb-0">
            <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
              <ZapIcon className="size-3" /> Token 用量
            </div>
            <div className="text-2xl font-semibold tabular-nums">
              {fmtTokens(displayedTokens.input + displayedTokens.output) || "—"}
            </div>
          </CardHeader>
          <CardContent className="text-[10px] text-muted-foreground">
            入 {fmtTokens(displayedTokens.input)}（含缓存 {fmtTokens(displayedTokens.cacheRead)}）· 出{" "}
            {fmtTokens(displayedTokens.output)}
          </CardContent>
        </Card>
      </div>
    </>
  );
}
