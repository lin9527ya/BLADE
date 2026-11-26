"use client";

import * as React from "react";

import {
  ActivityIcon,
  ArrowDownIcon,
  ArrowUpIcon,
  CheckCircle2Icon,
  Loader2Icon,
  RotateCcwIcon,
  XCircleIcon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { LLMPoolChainTestResult, LLMPoolMember, LLMPoolStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

import { cooldownText, formatBreakerPolicySummary } from "./llm-helpers";

// LLM 轮询链（故障转移）配置抽屉：开关、顺位微调、整链探测与实时健康状态。

export function PoolDialog({
  open,
  onOpenChange,
  pool,
  onReload,
  onMembersChanged,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  pool: LLMPoolStatus | null;
  onReload: () => Promise<void>;
  onMembersChanged?: () => Promise<void>;
}) {
  const [busy, setBusy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [testedAt, setTestedAt] = React.useState<string>("");
  const [testResults, setTestResults] = React.useState<Record<string, LLMPoolChainTestResult>>({});

  // 冷却倒计时与半开探活状态由后端维护，抽屉打开且有异常/开启时 10s 自动刷新。
  usePolling(
    () => onReload(),
    open && pool && (pool.enabled || pool.chain.some((m) => m.state !== "ok")) ? 10_000 : 0,
    [open, pool, onReload],
  );

  async function toggle(patch: { llm_pool_enabled?: boolean; llm_pool_bind_fallback?: boolean }) {
    if (busy) return;
    setBusy(true);
    try {
      await api.setSettings(patch);
      await (onMembersChanged ? onMembersChanged() : onReload());
      if (patch.llm_pool_enabled !== undefined) {
        toast.success(patch.llm_pool_enabled ? "已开启 LLM 轮询" : "已关闭 LLM 轮询");
      } else {
        toast.success("已更新兜底设置");
      }
    } catch (e) {
      toast.error(`设置失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    if (busy) return;
    setBusy(true);
    try {
      await api.resetLLMPool(id);
      await onReload();
      toast.success(id ? "已恢复该配置" : "已恢复全部配置");
    } catch (e) {
      toast.error(`恢复失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function runChainTest() {
    if (testing) return;
    setTesting(true);
    try {
      const res = await api.testLLMPoolChain();
      const byId: Record<string, LLMPoolChainTestResult> = {};
      for (const r of res.results ?? []) {
        byId[r.profile_id] = r;
      }
      setTestResults(byId);
      setTestedAt(res.tested_at || "");
      const okCount = (res.results ?? []).filter((r) => r.ok).length;
      const total = (res.results ?? []).length;
      if (total === 0) {
        toast.info("当前没有可参与轮询的配置");
      } else if (okCount === total) {
        toast.success(`整链探活通过（${okCount}/${total} 就绪）`);
      } else {
        toast.warning(`链路探活完成：${okCount}/${total} 就绪，请检查异常项`);
      }
    } catch (e) {
      toast.error(`整链测试失败：${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  const enabled = pool?.enabled ?? false;
  const chain = pool?.chain ?? [];
  // 参与轮询的成员（排除被标记「不参与轮询」的），顺序即后端实际的尝试顺序。
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const nonActiveIncluded = chain.filter((m) => !m.active && !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped" || m.state === "half_open" || m.half_open);

  async function moveBackup(member: LLMPoolMember, dir: -1 | 1) {
    if (busy) return;
    const idx = nonActiveIncluded.findIndex((x) => x.profile_id === member.profile_id);
    const swapIdx = idx + dir;
    if (idx < 0 || swapIdx < 0 || swapIdx >= nonActiveIncluded.length) return;
    const nextOrder = [...nonActiveIncluded];
    const tmp = nextOrder[idx];
    nextOrder[idx] = nextOrder[swapIdx];
    nextOrder[swapIdx] = tmp;

    // 赋予严格递减优先级，避免原优先级相同时交换无效。
    const payload = nextOrder.map((m, i) => ({
      profile_id: m.profile_id,
      priority: (nextOrder.length - i) * 10,
      pool_exclude: m.excluded,
    }));
    setBusy(true);
    try {
      await api.updateLLMPoolMembers(payload);
      await (onMembersChanged ? onMembersChanged() : onReload());
      toast.success("已调整轮询顺位");
    } catch (e) {
      toast.error(`调整顺位失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function toggleExclude(member: LLMPoolMember) {
    if (busy || member.active) return;
    setBusy(true);
    try {
      await api.updateLLMPoolMembers([
        {
          profile_id: member.profile_id,
          priority: member.priority,
          pool_exclude: !member.excluded,
        },
      ]);
      await (onMembersChanged ? onMembersChanged() : onReload());
      toast.success(member.excluded ? `已恢复「${member.name}」参与轮询` : `已将「${member.name}」移出轮询`);
    } catch (e) {
      toast.error(`切换参与状态失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl">
        <DialogHeader className="border-b px-4 py-3">
          <DialogTitle className="flex items-center gap-2">
            <ZapIcon className="size-4" /> LLM 轮询 · 故障转移
          </DialogTitle>
          <DialogDescription>
            开启后，<b>未指定模型</b>的 Agent 在当前配置不可用（余额不足 / Key 失效 / 限流 /
            服务异常）时自动切到下一个配置。
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">启用轮询</Label>
              <p className="text-muted-foreground text-xs">默认关闭。关闭时始终只用激活配置，失败即失败。</p>
            </div>
            <Switch
              checked={enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
              aria-label="LLM 轮询开关"
            />
          </div>

          {enabled && (
            <>
              <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">指定模型失败时也兜底</Label>
                  <p className="text-muted-foreground text-xs">
                    默认关闭：Agent 或任务指定了某个配置就只用它，失败即失败（不会悄悄换成别的模型）。
                    开启后，指定的配置失败时也会回落到下面的轮询链。
                  </p>
                </div>
                <Switch
                  checked={pool?.bind_fallback ?? false}
                  disabled={busy}
                  onCheckedChange={(v) => void toggle({ llm_pool_bind_fallback: v })}
                  aria-label="绑定配置失败兜底开关"
                />
              </div>

              <Separator />

              <div className="grid gap-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <Label className="text-sm">轮询顺序</Label>
                  <div className="flex items-center gap-1.5">
                    {inChain.length > 0 && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={testing || busy}
                        onClick={() => void runChainTest()}
                      >
                        {testing ? <Loader2Icon className="animate-spin" /> : <ActivityIcon />}
                        测试整条链路
                      </Button>
                    )}
                    {tripped.length > 0 && (
                      <Button size="sm" variant="ghost" disabled={busy} onClick={() => void recover()}>
                        <RotateCcwIcon /> 全部恢复
                      </Button>
                    )}
                  </div>
                </div>
                {testedAt && (
                  <p className="text-muted-foreground text-xs">
                    最近整链探活时间：{new Date(testedAt).toLocaleTimeString()}
                    （仅验证可用性与真实输出，不改变熔断计数）
                  </p>
                )}
                {inChain.length < 2 && (
                  <p className="text-muted-foreground text-xs">
                    当前只有 {inChain.length} 个可用配置，轮询不会生效——至少需要 2 个已填 API Key 且参与轮询的配置。
                  </p>
                )}
                {chain.map((m) => {
                  const excluded = m.excluded && !m.active;
                  const order = excluded ? null : inChain.findIndex((x) => x.profile_id === m.profile_id) + 1;
                  const backupIdx = nonActiveIncluded.findIndex((x) => x.profile_id === m.profile_id);
                  const isHalfOpen = m.state === "half_open" || m.half_open;
                  const probe = testResults[m.profile_id];
                  return (
                    <div
                      key={m.profile_id}
                      className={cn(
                        "grid gap-1.5 rounded-lg border p-2.5 text-sm",
                        excluded && "opacity-60",
                        m.state === "tripped" && "border-destructive/40",
                        isHalfOpen && "border-amber-500/50",
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="w-5 shrink-0 text-center font-mono text-muted-foreground text-xs">
                          {order ?? "—"}
                        </span>
                        <span className="font-medium">{m.name}</span>
                        {m.active && (
                          <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                            激活
                          </Badge>
                        )}
                        {excluded && <Badge variant="outline">不参与轮询</Badge>}
                        {isHalfOpen && (
                          <Badge variant="outline" className="border-amber-500/50 text-amber-600 dark:text-amber-400">
                            半开探活中
                          </Badge>
                        )}
                        {(m.sticky_sessions ?? 0) > 0 && (
                          <Badge variant="outline" className="border-sky-500/40 text-sky-600 dark:text-sky-400">
                            Sticky {m.sticky_sessions}
                          </Badge>
                        )}
                        {m.trips > 0 && <span className="text-muted-foreground text-xs">累计熔断 {m.trips} 次</span>}
                        <div className="ml-auto flex items-center gap-1">
                          {m.state === "tripped" && m.cooldown_secs > 0 && (
                            <span className="mr-1 text-destructive text-xs">冷却 {cooldownText(m.cooldown_secs)}</span>
                          )}
                          {m.state === "degraded" && (
                            <span className="mr-1 text-amber-600 text-xs dark:text-amber-400">
                              连续失败 {m.fails} 次
                            </span>
                          )}
                          {m.state !== "ok" && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="size-7"
                              disabled={busy}
                              aria-label="立即恢复"
                              title="立即恢复：清除熔断，下次调用重试该配置"
                              onClick={() => void recover(m.profile_id)}
                            >
                              <RotateCcwIcon className="size-3.5" />
                            </Button>
                          )}
                          {!m.active && !m.excluded && (
                            <>
                              <Button
                                size="icon"
                                variant="ghost"
                                className="size-7"
                                disabled={busy || backupIdx <= 0}
                                aria-label="上移顺位"
                                title="上移轮询顺位"
                                onClick={() => void moveBackup(m, -1)}
                              >
                                <ArrowUpIcon className="size-3.5" />
                              </Button>
                              <Button
                                size="icon"
                                variant="ghost"
                                className="size-7"
                                disabled={busy || backupIdx < 0 || backupIdx >= nonActiveIncluded.length - 1}
                                aria-label="下移顺位"
                                title="下移轮询顺位"
                                onClick={() => void moveBackup(m, 1)}
                              >
                                <ArrowDownIcon className="size-3.5" />
                              </Button>
                            </>
                          )}
                          {!m.active && (
                            <Button
                              size="sm"
                              variant="ghost"
                              className="h-7 px-2 text-xs"
                              disabled={busy}
                              onClick={() => void toggleExclude(m)}
                            >
                              {m.excluded ? "参与" : "排除"}
                            </Button>
                          )}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 pl-7 text-muted-foreground text-xs">
                        <code className="truncate font-mono">{m.model}</code>
                        <span className="uppercase">{m.format}</span>
                        {!m.active && <span>优先级 {m.priority}</span>}
                      </div>
                      {m.last_error && (
                        <p className="truncate pl-7 font-mono text-muted-foreground text-xs" title={m.last_error}>
                          {m.last_error}
                        </p>
                      )}
                      {probe && (
                        <div
                          className={cn(
                            "flex items-center gap-1.5 pl-7 text-xs",
                            probe.ok ? "text-emerald-600 dark:text-emerald-400" : "text-destructive",
                          )}
                        >
                          {probe.ok ? (
                            <>
                              <CheckCircle2Icon className="size-3.5 shrink-0" />
                              <span>
                                探活就绪 · {probe.latency_ms ?? 0}ms{probe.reply ? ` · 回复 “${probe.reply}”` : ""}
                              </span>
                            </>
                          ) : (
                            <>
                              <XCircleIcon className="size-3.5 shrink-0" />
                              <span className="truncate" title={probe.error}>
                                探活失败：{probe.error}
                              </span>
                            </>
                          )}
                        </div>
                      )}
                    </div>
                  );
                })}
                {chain.length === 0 && (
                  <div className="rounded-lg border border-dashed p-4 text-center text-muted-foreground text-sm">
                    暂无配置
                  </div>
                )}
              </div>

              <div className="rounded-lg border border-dashed p-3 text-muted-foreground text-xs leading-relaxed">
                激活配置恒为第 1 顺位，其余按优先级从高到低（可用右侧箭头快速调序或排除）。当前熔断策略：
                <span className="text-foreground">{formatBreakerPolicySummary(pool?.policy)}</span>
                ；冷却到期进入单飞探活，恢复即重置，失败则升阶继续冷却。上下文窗口装不下当前请求的配置会被跳过。指定了模型的
                Agent 与任务默认不参与全局轮询。
              </div>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
