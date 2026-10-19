"use client";

import * as React from "react";
import { usePolling } from "@/hooks/use-polling";

import { useSearchParams } from "next/navigation";

import { ApprovalRecords } from "@/components/approval-records";
import { PageShell, Section, SectionNav } from "@/components/page-sections";
import { api } from "@/lib/api";

import { AssetRulesSection } from "./asset-rules-section";
import { JudgeSection } from "./judge-section";
import { RulesSection } from "./rules-section";

// 拦截与审批工作台：命令拦截 / 模型兜底审批 / 资产黑名单 / 审批记录——
// 同一套「拦截—审批」机制的四个面，此前在侧栏是三个平级标签（拦截规则 / 资产拦截 / 审批记录），
// 现在合并成一个页面，用与系统设置同款的分区导航切换。
//
// 旧的 /system/intercept/assets 与 /system/intercept/approvals 保留为别名壳页
// （传 initialSection），仪表盘的待审批直达链接与书签都不会断。

const SECTIONS = [
  { id: "rules", label: "命令拦截" },
  { id: "judge", label: "模型兜底审批" },
  { id: "assets", label: "资产黑名单" },
  { id: "approvals", label: "审批记录" },
] as const;

type SectionId = (typeof SECTIONS)[number]["id"];

function isSectionId(v: string): v is SectionId {
  return SECTIONS.some((s) => s.id === v);
}

export function InterceptWorkbench({ initialSection }: { initialSection?: SectionId }) {
  const params = useSearchParams();
  const tabParam = params?.get("tab") ?? "";
  const [active, setActive] = React.useState<SectionId>(isSectionId(tabParam) ? tabParam : (initialSection ?? "rules"));
  // 待审批条数：与侧栏红点同一个数据源，进页面先拉一次、之后每 10s 刷一次。
  const [pending, setPending] = React.useState(0);

  usePolling(async (live) => {
    const poll = () => {
      void api
        .interceptPending()
        .then((list) => {
          if (live()) setPending(list.length);
        })
        .catch(() => {});
    };
    poll();
  }, 10000, []);

  // 深链：?tab=xxx 变化时切过去（静态导出下由外层 Suspense 兜住 useSearchParams）。
  React.useEffect(() => {
    if (isSectionId(tabParam)) setActive(tabParam);
  }, [tabParam]);

  const navSections = SECTIONS.map((s) => ({
    ...s,
    badge: s.id === "approvals" ? pending : undefined,
  }));

  return (
    <PageShell
      nav={
        <SectionNav
          sections={navSections}
          active={active}
          onChange={(id) => setActive(id as SectionId)}
          ariaLabel="拦截与审批分区"
        />
      }
    >
      <div className="flex flex-col gap-1">
        <h1 className="text-xl font-semibold tracking-tight">拦截与审批</h1>
        <p className="text-muted-foreground text-sm">
          工具调用前先按拦截规则匹配；未命中的可交模型兜底判定，需要人裁决的进审批记录
        </p>
      </div>

      {active === "rules" && (
        <Section
          title="命令拦截"
          hint="按优先级逐条匹配工具调用；拦截范围决定哪些工具进入判定（范围外的工具，规则与模型都不介入）。"
        >
          <RulesSection />
        </Section>
      )}

      {active === "judge" && (
        <Section
          title="模型兜底审批"
          hint="仅在没有任何规则命中时介入：给一次模型裁决，拿不准就转人工审批。改完立即对之后的调用生效。"
        >
          <JudgeSection />
        </Section>
      )}

      {active === "assets" && (
        <Section
          title="资产黑名单"
          hint="命中即拦：不对该域名 / IP / URL / 网段执行任何操作（全局规则，各任务共享；任务级资产拦截在任务页）。"
        >
          <AssetRulesSection />
        </Section>
      )}

      {active === "approvals" && (
        <Section
          title="审批记录"
          hint="待处理与历史裁决：可放行、拒绝，或改参后放行（只允许比原参数更保守）。任务详情里也有同一份的按任务视图。"
        >
          <ApprovalRecords />
        </Section>
      )}
    </PageShell>
  );
}
