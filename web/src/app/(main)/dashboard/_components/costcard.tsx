"use client";

import { CoinsIcon } from "lucide-react";

import { Card } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { cn } from "@/lib/utils";

import { fmtTokens } from "./dashboard-helpers";
import type { useDashboardState } from "./use-dashboard-state";

function usd(n: number): string {
  if (!Number.isFinite(n)) return "—";
  if (n >= 100) return `$${n.toFixed(0)}`;
  return `$${n.toFixed(2)}`;
}

function budgetText(status?: string, budget?: number): string {
  if (!budget || !status || status === "unset") return "";
  if (status === "exceeded") return "超预算";
  if (status === "warning") return "接近预算";
  return "预算内";
}

export function RowCostCard({ s }: { s: ReturnType<typeof useDashboardState>["costCard"] }) {
  const { costs } = s;
  const todayKey = new Date().toISOString().slice(0, 10);
  const today = costs?.daily.find((d) => d.date === todayKey)?.cost_usd ?? 0;
  const monthCost = costs?.month.cost_usd ?? 0;
  const budget = costs?.budget_usd ?? 0;
  const pct = budget > 0 ? Math.min(100, (monthCost / budget) * 100) : 0;
  const status = costs?.budget_status ?? "unset";
  const recent = costs?.daily.slice(-14) ?? [];
  const maxDay = Math.max(...recent.map((d) => d.cost_usd), 0.000001);
  const topModels = costs?.by_model.filter((m) => m.cost_usd > 0).slice(0, 4) ?? [];
  const topCompanies = costs?.by_company.filter((c) => c.cost_usd > 0).slice(0, 3) ?? [];
  const topCampaigns = costs?.by_campaign.filter((c) => c.cost_usd > 0).slice(0, 3) ?? [];
  const peakCost = costs?.total.peak_cost_usd ?? 0;
  const offpeakCost = costs?.total.offpeak_cost_usd ?? 0;

  return (
    <Card className="p-4">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 font-semibold text-xs">
          <CoinsIcon className="size-3.5 text-muted-foreground" />
          LLM 成本
          <span className="font-normal text-[10px] text-muted-foreground">按模型价格估算 · 近 30 天</span>
        </div>
        <span
          className={cn(
            "rounded-md border px-2 py-0.5 font-medium text-[10px]",
            status === "exceeded"
              ? "border-red-500/40 bg-red-500/10 text-red-400"
              : status === "warning"
                ? "border-amber-500/40 bg-amber-500/10 text-amber-400"
                : "border-border text-muted-foreground",
          )}
        >
          {status === "exceeded"
            ? "已超预算"
            : status === "warning"
              ? "接近预算"
              : status === "ok"
                ? "预算正常"
                : "未设预算"}
        </span>
      </div>

      <div className="grid gap-6 lg:grid-cols-[260px_1fr]">
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <div className="text-[10px] text-muted-foreground">今日</div>
              <div className="font-bold text-2xl tabular-nums">{usd(today)}</div>
            </div>
            <div>
              <div className="text-[10px] text-muted-foreground">本月</div>
              <div className="font-bold text-2xl tabular-nums">{usd(monthCost)}</div>
            </div>
          </div>
          <div>
            <div className="mb-1 flex items-center justify-between text-[10px] text-muted-foreground">
              <span>月度预算</span>
              <span className="tabular-nums">{budget > 0 ? `${usd(monthCost)} / ${usd(budget)}` : "未设置"}</span>
            </div>
            <Progress value={pct} className="h-1.5" />
          </div>
          <div className="flex flex-wrap gap-x-3 gap-y-1 text-[10px] text-muted-foreground">
            <span>缓存节省 {usd(costs?.total.cache_savings_usd ?? 0)}</span>
            {offpeakCost > 0 && (
              <span>
                近 30 天 峰时 {usd(peakCost)} · 谷时 <span className="text-emerald-500">{usd(offpeakCost)}</span>
              </span>
            )}
            {(costs?.total.unpriced_tokens ?? 0) > 0 && (
              <span className="text-amber-500">未计价 {fmtTokens(costs?.total.unpriced_tokens ?? 0)} token</span>
            )}
          </div>
          {topModels.length > 0 && (
            <div className="flex flex-col gap-1.5">
              <div className="text-[10px] text-muted-foreground">花费最高模型</div>
              {topModels.map((m) => (
                <div key={m.model} className="flex items-center justify-between gap-2 text-[10px]">
                  <span className="min-w-0 truncate font-mono" title={m.model}>
                    {m.model}
                  </span>
                  <span className="shrink-0 font-semibold tabular-nums">{usd(m.cost_usd)}</span>
                </div>
              ))}
            </div>
          )}
          {topCompanies.length > 0 && (
            <div className="flex flex-col gap-1.5">
              <div className="text-[10px] text-muted-foreground">按公司</div>
              {topCompanies.map((c) => (
                <div key={c.id} className="flex items-center justify-between gap-2 text-[10px]">
                  <span className="min-w-0 truncate" title={c.name}>
                    {c.name || `#${c.id}`}
                    {budgetText(c.budget_status, c.budget_usd) ? (
                      <span
                        className={cn(
                          "ml-1",
                          c.budget_status === "exceeded" ? "text-red-400" : "text-muted-foreground",
                        )}
                      >
                        {budgetText(c.budget_status, c.budget_usd)}
                      </span>
                    ) : null}
                  </span>
                  <span className="shrink-0 font-semibold tabular-nums">{usd(c.cost_usd)}</span>
                </div>
              ))}
            </div>
          )}
          {topCampaigns.length > 0 && (
            <div className="flex flex-col gap-1.5">
              <div className="text-[10px] text-muted-foreground">按战役</div>
              {topCampaigns.map((c) => (
                <div key={c.id} className="flex items-center justify-between gap-2 text-[10px]">
                  <span className="min-w-0 truncate" title={c.name}>
                    {c.name || `#${c.id}`}
                    {budgetText(c.budget_status, c.budget_usd) ? (
                      <span
                        className={cn(
                          "ml-1",
                          c.budget_status === "exceeded" ? "text-red-400" : "text-muted-foreground",
                        )}
                      >
                        {budgetText(c.budget_status, c.budget_usd)}
                      </span>
                    ) : null}
                  </span>
                  <span className="shrink-0 font-semibold tabular-nums">{usd(c.cost_usd)}</span>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="flex min-h-36 flex-col justify-end">
          <div className="mb-2 flex items-center justify-between text-[10px] text-muted-foreground">
            <span>每日成本</span>
            <span>峰值 {usd(maxDay)}</span>
          </div>
          {recent.length === 0 ? (
            <div className="flex flex-1 items-center justify-center rounded-lg border bg-muted/10 text-muted-foreground text-xs">
              暂无计价数据
            </div>
          ) : (
            <div className="flex h-32 items-end gap-1">
              {recent.map((d) => (
                <div key={d.date} className="group relative flex h-full min-w-0 flex-1 flex-col justify-end">
                  <div
                    className="w-full rounded-t-sm bg-emerald-500/60 transition-colors group-hover:bg-emerald-400"
                    style={{ height: `${Math.max(2, (d.cost_usd / maxDay) * 100)}%` }}
                    title={`${d.date} · ${usd(d.cost_usd)}`}
                  />
                  <span className="mt-1 rotate-0 text-center text-[8px] text-muted-foreground">{d.date.slice(5)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </Card>
  );
}
