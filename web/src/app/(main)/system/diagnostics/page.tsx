"use client";

import * as React from "react";
import Link from "next/link";
import { ActivityIcon, AlertTriangleIcon, ExternalLinkIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import {
  cacheStructureExplanation,
  diagnosticHitRate,
  type CacheDiagnostics,
  type RuntimeMetrics,
} from "@/lib/diagnostics";
import { createLatestRequest } from "@/lib/request-lifecycle";

const fmt = (v: number | undefined) => (v === undefined ? "—" : v.toLocaleString("zh-CN"));
const percentage = (v: number | undefined) => (v === undefined ? "—" : `${v.toFixed(1)}%`);
const stamp = (v: string) => new Date(v).toLocaleString("zh-CN");
function LogLink({ query = "", children = "查看日志" }: { query?: string; children?: React.ReactNode }) {
  return (
    <Link
      className="inline-flex items-center gap-1 text-sm underline underline-offset-4"
      href={`/system/logs?q=${encodeURIComponent(query)}`}
    >
      {children}
      <ExternalLinkIcon className="size-3" />
    </Link>
  );
}

export default function DiagnosticsPage() {
  const [metrics, setMetrics] = React.useState<RuntimeMetrics | null>(null);
  const [cache, setCache] = React.useState<CacheDiagnostics | null>(null);
  const [errors, setErrors] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [taskInput, setTaskInput] = React.useState("");
  const [task, setTask] = React.useState("");
  const [worker, setWorker] = React.useState("");
  const [filter, setFilter] = React.useState("");
  const [before, setBefore] = React.useState(0);
  const [version, setVersion] = React.useState(0);
  const [toggling, setToggling] = React.useState(false);
  const owner = React.useRef(createLatestRequest());
  React.useEffect(() => {
    const id = new URLSearchParams(window.location.search).get("task") ?? "";
    if (/^\d+$/.test(id)) {
      setTaskInput(id);
      setTask(id);
    }
  }, []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: version 是手动刷新及诊断开关变更后的触发器。
  React.useEffect(() => {
    setCache(null);
    const refresh = async () => {
      const request = owner.current.start();
      setLoading(true);
      const [m, c] = await Promise.allSettled([
        api.metrics(request.signal),
        api.cacheDiagnostics({ task, worker, cache: filter, before }, request.signal),
      ]);
      if (!request.alive()) return;
      const issues: string[] = [];
      if (m.status === "fulfilled") setMetrics(m.value);
      else issues.push(`运行指标：${(m.reason as Error).message}`);
      if (c.status === "fulfilled") setCache(c.value);
      else issues.push(`缓存诊断：${(c.reason as Error).message}`);
      setErrors(issues.join("；"));
      setLoading(false);
    };
    void refresh();
    const timer = window.setInterval(() => {
      if (!document.hidden && before === 0) void refresh();
    }, 10000);
    return () => {
      window.clearInterval(timer);
      owner.current.cancel();
    };
  }, [task, worker, filter, before, version]);

  async function capture(enabled: boolean) {
    if (toggling) return;
    if (
      enabled &&
      !window.confirm(
        "开启本进程 10 分钟结构诊断？只记录哈希、长度、断点和用量数值，不记录正文/凭据，不发送额外模型请求。重启后样本丢失。",
      )
    )
      return;
    setToggling(true);
    try {
      await api.setCacheDiagnostics({ enabled, seconds: 600 });
      setVersion((v) => v + 1);
      toast.success(enabled ? "结构诊断已开启 10 分钟" : "结构诊断已关闭并清空样本");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setToggling(false);
    }
  }

  let captureState = "未读取";
  if (cache) captureState = cache.state.enabled ? "已开启" : "关闭";
  const resources = metrics?.system?.supported ? metrics.system : null;
  const counters = [
    ["CPU 占用", percentage(resources?.cpu.percent)],
    ["内存占用", percentage(resources?.memory.total_bytes ? resources.memory.percent : undefined)],
    ["数据目录磁盘占用", percentage(resources?.data_disk.total_bytes ? resources.data_disk.percent : undefined)],
    ["在途模型调用", fmt(metrics?.llm_in_flight)],
    ["数据库等待（累计）", fmt(metrics?.db?.wait)],
    ["SSE 溢出（累计）", fmt(metrics?.sse?.overflow_events)],
  ];

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl">
            <ActivityIcon />
            运行体检与缓存诊断
          </h1>
          <p className="mt-1 text-muted-foreground text-sm">
            读取运行状态和计量；不会请求模型或目标。最新页每 10 秒刷新，历史页不自动翻回。
          </p>
        </div>
        <Button variant="outline" onClick={() => setVersion((v) => v + 1)} disabled={loading}>
          <RefreshCwIcon />
          {loading ? "刷新中…" : "刷新"}
        </Button>
      </div>
      {errors && (
        <div role="alert" className="rounded border p-3 text-sm">
          <AlertTriangleIcon className="mr-2 inline size-4" />
          刷新失败，保留可用的旧快照：{errors} <LogLink />
        </div>
      )}
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        {counters.map(([label, value]) => (
          <Card key={label}>
            <CardContent className="space-y-2 p-4">
              <p className="text-muted-foreground text-xs">{label}</p>
              <p className="font-semibold text-2xl tabular-nums">{value}</p>
            </CardContent>
          </Card>
        ))}
      </div>
      <p className="text-muted-foreground text-xs">
        资源范围：{resources?.scope ?? "不支持或尚未采样"} · 更新：{metrics ? stamp(metrics.now) : "—"} ·
        累计指标在进程重启后重置；有累计值不等于当前正在故障。
      </p>
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">连接、写入与传输</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <p>
              数据库连接：使用 {fmt(metrics?.db?.in_use)} / 打开 {fmt(metrics?.db?.open)} / 上限{" "}
              {fmt(metrics?.db?.max_open)}
            </p>
            <p>
              活动入库失败：{fmt(metrics?.dropped_activity?.total)}（累计） <LogLink query="activity" />
            </p>
            <p>
              HTTP 请求：{fmt(metrics?.http?.total)} · 慢请求：{fmt(metrics?.http?.slow_total)}（累计）{" "}
              <LogLink query="http" />
            </p>
            <p>
              运行时 goroutine：{fmt(metrics?.runtime?.goroutines)} · 启动时长：{fmt(metrics?.uptime_seconds)} 秒
            </p>
            <p>
              <Link href="/system/llm" className="underline underline-offset-4">
                模型配置与故障转移
              </Link>{" "}
              ·{" "}
              <Link href="/system/storage" className="underline underline-offset-4">
                存储体检
              </Link>
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">HTTP 路径统计（进程累计）</CardTitle>
          </CardHeader>
          <CardContent className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>路径</TableHead>
                  <TableHead>请求</TableHead>
                  <TableHead>4xx / 5xx</TableHead>
                  <TableHead>P95</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {metrics?.http?.by_path.map((r) => (
                  <TableRow key={r.path}>
                    <TableCell className="font-mono text-xs">{r.path}</TableCell>
                    <TableCell>{fmt(r.count)}</TableCell>
                    <TableCell>
                      {r.errors_4xx} / {r.errors_5xx}
                    </TableCell>
                    <TableCell>{r.count ? `${r.p95_ms} ms` : "—"}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </div>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">模型配置统计（近 24 小时）</CardTitle>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>配置</TableHead>
                <TableHead>调用</TableHead>
                <TableHead>输入 / 输出</TableHead>
                <TableHead>缓存命中率</TableHead>
                <TableHead>零缓存调用</TableHead>
                <TableHead>P50 / P95</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {metrics?.llm?.by_profile.map((r) => (
                <TableRow key={r.key}>
                  <TableCell>{r.key || "默认配置"}</TableCell>
                  <TableCell>{fmt(r.calls)}</TableCell>
                  <TableCell>
                    {fmt(r.input_tokens)} / {fmt(r.output_tokens)}
                  </TableCell>
                  <TableCell>{diagnosticHitRate(r.input_tokens, r.cache_read)}</TableCell>
                  <TableCell>{fmt(r.zero_cache_calls)}</TableCell>
                  <TableCell>{r.calls ? `${r.p50_ms} / ${r.p95_ms} ms` : "—"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <p className="mt-3 text-muted-foreground text-xs">
            首调零缓存会话：
            {metrics?.llm
              ? Object.entries(metrics.llm.cold_start_sessions)
                  .map(([name, count]) => `${name} ${count}`)
                  .join(" · ") || "0"
              : "—"}
            。零缓存调用还包括会话中途零命中的情况，两者不能混同。
          </p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">逐调用计量与结构样本</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <Input
              className="w-40"
              aria-label="任务 ID"
              placeholder="任务 ID，留空为全部"
              value={taskInput}
              onChange={(e) => setTaskInput(e.target.value)}
            />
            <Button
              variant="outline"
              onClick={() => {
                setTask(taskInput.trim());
                setBefore(0);
                setVersion((v) => v + 1);
              }}
            >
              查询
            </Button>
            <Select
              value={worker || "all"}
              onValueChange={(v) => {
                setWorker(v === "all" ? "" : v);
                setBefore(0);
              }}
            >
              <SelectTrigger className="w-36">
                <SelectValue placeholder="全部角色" />
              </SelectTrigger>
              <SelectContent>
                {[
                  ["all", "全部角色"],
                  ["planner", "Planner"],
                  ["worker", "Worker"],
                  ["goals", "Goals"],
                  ["mainagent", "主 Agent"],
                ].map(([v, label]) => (
                  <SelectItem key={v} value={v}>
                    {label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={filter || "all"}
              onValueChange={(v) => {
                setFilter(v === "all" ? "" : v);
                setBefore(0);
              }}
            >
              <SelectTrigger className="w-36">
                <SelectValue placeholder="全部用量" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部用量</SelectItem>
                <SelectItem value="zero">零缓存</SelectItem>
                <SelectItem value="low">命中率低于 50%</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Badge variant="outline">结构诊断：{captureState}</Badge>
            {cache?.state.environment ? (
              <span>由环境变量启用，需修改环境后重启关闭。</span>
            ) : (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!cache || toggling}
                  onClick={() => capture(!cache?.state.enabled)}
                >
                  {cache?.state.enabled ? "关闭并清空" : "开启 10 分钟"}
                </Button>
                {cache?.state.expires_at && <span>到期：{stamp(cache.state.expires_at)}</span>}
              </>
            )}
            <Button
              size="sm"
              variant="ghost"
              disabled={toggling}
              onClick={async () => {
                if (!window.confirm("只清空结构样本，不删除计量台账，确认吗？")) return;
                try {
                  await api.setCacheDiagnostics({ clear: true });
                  setVersion((v) => v + 1);
                } catch (e) {
                  toast.error((e as Error).message);
                }
              }}
            >
              清空结构样本
            </Button>
          </div>
          <p className="text-muted-foreground text-xs">
            计量始终可见，不要求录制正文。结构样本仅留本进程近 30 分钟、最多 256 条，本次展示最近 100
            条；不保存正文、请求头或凭据，重启即丢失。角色/缓存筛选作用于计量，结构样本按任务筛选。
          </p>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>时间 / 调用 ID</TableHead>
                  <TableHead>任务 / 角色</TableHead>
                  <TableHead>实际配置 / 模型</TableHead>
                  <TableHead>输入 / 输出</TableHead>
                  <TableHead>缓存读 / 写</TableHead>
                  <TableHead>命中率</TableHead>
                  <TableHead>状态 / 延迟</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {cache?.calls.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell className="whitespace-nowrap text-xs">
                      {stamp(r.at)}
                      <br />#{r.id}
                    </TableCell>
                    <TableCell>
                      #{r.task_id || "非任务"} · {r.worker}
                      <p className="max-w-56 truncate font-mono text-xs" title={r.session}>
                        {r.session}
                      </p>
                    </TableCell>
                    <TableCell>
                      <p>{r.profile || "默认"}</p>
                      <p className="max-w-56 truncate text-xs">{r.model}</p>
                    </TableCell>
                    <TableCell>
                      {fmt(r.input)} / {fmt(r.output)}
                    </TableCell>
                    <TableCell>
                      {fmt(r.cache_read)} / {fmt(r.cache_write)}
                    </TableCell>
                    <TableCell>
                      {diagnosticHitRate(r.input, r.cache_read)}
                      {r.first_call && <p className="text-muted-foreground text-xs">会话首调</p>}
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">{r.status}</Badge> {r.latency_ms} ms
                    </TableCell>
                  </TableRow>
                ))}
                {cache && !cache.calls.length && (
                  <TableRow>
                    <TableCell colSpan={7} className="py-6 text-center text-muted-foreground">
                      该条件下没有计量样本，不代表命中率为 0。
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
          <div className="flex gap-2">
            <Button size="sm" variant="outline" disabled={!before} onClick={() => setBefore(0)}>
              返回最新
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!cache?.next_before || loading}
              onClick={() => {
                if (cache?.next_before) setBefore(cache.next_before);
              }}
            >
              更早调用
            </Button>
          </div>
          <p className="text-muted-foreground text-xs">
            下列请求 ID 与计量调用 ID 不是同一编号。原始 usage
            帧可能是累计快照，不能相加或替代台账。仅提供实际配置名称；没有明确路由事件时，不猜测切换配置的原因。
          </p>
          <div className="space-y-3">
            {cache?.requests.map((r) => (
              <details key={r.id} className="min-w-0 rounded border p-3 text-sm">
                <summary className="cursor-pointer">
                  结构请求 #{r.id} · {stamp(r.at)} · {r.model} · HTTP {r.status || "未取得响应"}
                  {!r.complete ? " · 观察中" : ""}
                </summary>
                <div className="mt-3 min-w-0 space-y-2">
                  <p className="break-all font-mono text-xs">会话：{r.session}</p>
                  <p>{cacheStructureExplanation(r)}</p>
                  <p>
                    消息数：{r.messages} · 前序消息数：{r.previous_messages} · 共享前缀：{r.shared_messages}
                  </p>
                  <p className="break-all font-mono text-xs">
                    system：{r.system.bytes} bytes · {r.system.sha256}
                    <br />
                    tools：{r.tools.bytes} bytes · {r.tools.sha256}
                  </p>
                  <p>断点：{r.breakpoints.join("、") || "未发现显式断点"}（并非所有协议都需显式断点）</p>
                  <p>
                    usage 观察帧：{r.observed_frames}，保留首帧与最新帧，最多 16 条。
                    {r.truncated ? "存在超限帧，未采集其内容。" : ""}
                  </p>
                  {r.frames.length ? (
                    <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-3 text-xs">
                      {JSON.stringify(r.frames, null, 2)}
                    </pre>
                  ) : (
                    <p className="text-muted-foreground">无原始用量帧；缺少样本时，缓存原因未知。</p>
                  )}
                </div>
              </details>
            ))}
            {cache && !cache.requests.length && (
              <p className="rounded border p-4 text-muted-foreground text-sm">
                尚无结构样本，缓存归因未知。可在需要排查时短暂开启；此页面不会主动调用模型来制造样本。
              </p>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
