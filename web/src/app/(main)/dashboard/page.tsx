"use client";

import * as React from "react";

import dynamic from "next/dynamic";

import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api";
import type { TaskCategory } from "@/lib/types";

import { RowAssetTraffic } from "./_components/assettrafficrow";
import { CommandHeader } from "./_components/command-header";
import { DashboardSkeleton } from "./_components/dashboard-helpers";
import { OpsWorkspace } from "./_components/ops-workspace";
import { RowStatCards } from "./_components/statcards";
import { useDashboardState } from "./_components/use-dashboard-state";

const TelemetryPanel = dynamic(() => import("./_components/telemetry-panel").then((m) => m.TelemetryPanel), {
  ssr: false,
  loading: () => <Skeleton className="h-[310px] w-full rounded-xl" />,
});

const CreateTaskSheet = dynamic(
  () => import("../function/tasks/_components/create-task-sheet").then((m) => m.CreateTaskSheet),
  { ssr: false },
);

export default function DashboardPage() {
  const { initialLoading, headerSlice, statCards, opsWorkspace, telemetryPanel, infraGrid } = useDashboardState();
  const [createMounted, setCreateMounted] = React.useState(false);
  const [createOpen, setCreateOpen] = React.useState(false);
  const [categories, setCategories] = React.useState<TaskCategory[]>([]);

  const loadCategories = React.useCallback(() => {
    api
      .taskCategories()
      .then(setCategories)
      .catch(() => setCategories([]));
  }, []);

  const handleOpenCreate = React.useCallback(() => {
    if (!createMounted) {
      setCreateMounted(true);
      loadCategories();
    }
    setCreateOpen(true);
  }, [createMounted, loadCategories]);

  if (initialLoading) {
    return <DashboardSkeleton />;
  }

  return (
    <div className="flex flex-1 flex-col gap-4 pb-6">
      {/* Band 0: 指挥台顶栏 & 条件态势告警带 */}
      <CommandHeader s={headerSlice} onOpenCreate={handleOpenCreate} />

      {/* Band 1: 5 张可钻取 KPI 态势卡 */}
      <RowStatCards s={statCards} />

      {/* Band 2: 任务矩阵 + 发现/活动流工作区 */}
      <OpsWorkspace s={opsWorkspace} />

      {/* Band 3: 统一 LLM 用量 / 成本 / 故障转移池中心 */}
      <TelemetryPanel s={telemetryPanel} />

      {/* Band 4: 资产 · 流量 · 移动与排期 · 系统基座与审批 */}
      <RowAssetTraffic s={infraGrid} />

      {createMounted && (
        <CreateTaskSheet
          hideTrigger
          open={createOpen}
          onOpenChange={setCreateOpen}
          categories={categories}
          onCreated={() => void headerSlice.refreshNow()}
          onCategoriesChanged={loadCategories}
        />
      )}
    </div>
  );
}
