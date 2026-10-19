"use client";

import * as React from "react";

import { CheckIcon, Loader2Icon, RadarIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { IntelAsset, IntelSearchResult } from "@/lib/types";

// FofaSearchDialog：资产页的 FOFA 检索（P28）。
//
// 三条与后端一致的边界，界面上照说：
//   · 与 agent 的 intel_search 是**同一条执行路径**——同配额、同台账、同落盘，这里多查几次
//     就是真的多花几次额度（所以页面右上有「今日 x/y」）；
//   · **只查不扩**：结果不自动入库，勾选后走与 CSV 导入同一套 preview/apply（已存在只合并）；
//   · 端点可在 系统设置 → 情报源 改（企业版/自建/**第三方中转站**同一处），这里只显示当前源。

const EXAMPLES = [
  'domain="example.com"',
  'domain="example.com" && protocol="http"',
  'ip="1.2.3.0/24"',
  'cert="example.com"',
  'title="后台"',
];

// rowKey 用行内容做键：FOFA 没有稳定行 id，重复行按重复渲染也无妨。
function rowKey(a: IntelAsset, i: number): string {
  return `${i}:${a.host ?? ""}${a.ip ?? ""}${a.port ?? ""}`;
}

// assetRow 把一行折成**导入器认得的两个值**：URL 进 url 列、其余进 host 列。
// 导入器的列语义是死的（host 列只认 host、url 列只认 URL），而 FOFA 的 host 字段
// 常常就是带协议的 URL——不分列会被（正确地）判成"不是域名/IP/URL"。
// 端口只在值里没有时补（FOFA 的 host 常自带 :port，再补一次会变成 x:8443:8443）。
function assetRow(a: IntelAsset): { host: string; url: string } {
  const port = a.port ?? 0;
  let v = a.host || "";
  if (!v && a.ip) v = port > 0 ? `${a.ip}:${port}` : a.ip;
  if (!v) v = a.domain || "";
  if (!v) return { host: "", url: "" };
  if (port > 0 && !v.includes(":")) v = `${v}:${port}`;
  if (/^https?:\/\//i.test(v)) return { host: "", url: v };
  return { host: v, url: "" };
}

// extraColumns 取所有行 extra 键的并集（按首次出现顺序）：请求了什么字段就显示什么列。
function extraColumns(rows: IntelAsset[]): string[] {
  const seen: string[] = [];
  for (const r of rows) {
    for (const k of Object.keys(r.extra ?? {})) {
      if (!seen.includes(k)) seen.push(k);
    }
  }
  return seen;
}

function csvCell(v: string): string {
  return /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
}

export function FofaSearchDialog({ onImported }: { onImported: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [expr, setExpr] = React.useState("");
  const [searching, setSearching] = React.useState(false);
  const [importing, setImporting] = React.useState(false);
  const [error, setError] = React.useState("");
  const [res, setRes] = React.useState<IntelSearchResult | null>(null);
  const [picked, setPicked] = React.useState<Set<number>>(new Set());

  const rows = res?.assets ?? [];
  const cols = React.useMemo(() => extraColumns(rows), [rows]);

  async function run() {
    const q = expr.trim();
    if (!q) {
      setError("先填查询表达式（FOFA 语法）");
      return;
    }
    setSearching(true);
    setError("");
    try {
      const r = await api.intelSearch(q, { limit: 50 });
      setRes(r);
      setPicked(new Set(r.assets.map((_, i) => i))); // 默认全选：多数时候是"把命中的都收进来"
      if (r.assets.length === 0) toast.info("没有命中（换个表达式试试）");
    } catch (e) {
      setRes(null);
      setError(String((e as Error)?.message ?? e));
    } finally {
      setSearching(false);
    }
  }

  async function importPicked() {
    const pickedRows = rows
      .filter((_, i) => picked.has(i))
      .map(assetRow)
      .filter((r) => r.host || r.url);
    if (pickedRows.length === 0) {
      toast.error("没有可导入的行（勾选的行里没有 host/ip/domain）");
      return;
    }
    setImporting(true);
    try {
      // 与 CSV 导入同一套 preview → apply（已存在只合并），不另开一条入库路径。
      const csv = ["host,url", ...pickedRows.map((r) => `${r.host},${r.url}`)].join("\n");
      const prev = await api.previewAssetImport(csv);
      const apply = await api.importAssets(csv);
      toast.success(
        `导入完成：新增 ${apply.created ?? 0} 条、合并 ${apply.merged ?? 0} 条` +
          (prev && typeof prev.total === "number" ? `（识别 ${prev.total} 行）` : ""),
      );
      onImported();
      setOpen(false);
    } catch (e) {
      toast.error("导入失败：" + String((e as Error)?.message ?? e));
    } finally {
      setImporting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <RadarIcon className="size-4" /> FOFA 搜索
        </Button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-5xl">
        <DialogHeader className="border-b px-6 py-4">
          <DialogTitle className="flex flex-wrap items-center gap-2">
            FOFA 资产搜索
            {res && (
              <Badge variant="secondary" className="text-[11px]">
                命中 {res.total} · 返回 {res.returned}
              </Badge>
            )}
            {res && (
              <span className="text-muted-foreground text-xs font-normal">
                今日配额 {res.used_today}/{res.daily_quota || "不限"}
              </span>
            )}
          </DialogTitle>
          <DialogDescription className="text-xs">
            表达式用 FOFA 原生语法；端点（官方 / 企业版 / <b>第三方中转站</b>）与 Key 在 「系统设置 →
            情报源」里配。查询与 agent 的工具同一条路径：计入每日配额、进查询台账。
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3 border-b px-6 py-4">
          <div className="flex gap-2">
            <Input
              className="font-mono text-xs"
              placeholder='domain="example.com" && protocol="http"'
              value={expr}
              onChange={(e) => setExpr(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void run();
              }}
            />
            <Button onClick={() => void run()} disabled={searching}>
              {searching ? <Loader2Icon className="animate-spin" /> : <SearchIcon />} 搜索
            </Button>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-muted-foreground text-xs">示例：</span>
            {EXAMPLES.map((ex) => (
              <button
                key={ex}
                type="button"
                onClick={() => setExpr(ex)}
                className="bg-muted hover:bg-muted/70 rounded px-1.5 py-0.5 font-mono text-[11px]"
              >
                {ex}
              </button>
            ))}
          </div>
          {error && <p className="text-destructive text-xs leading-relaxed">{error}</p>}
        </div>

        <div className="min-h-0 flex-1 overflow-auto px-6 py-3">
          {rows.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              {searching ? "查询中…" : "还没有结果。填表达式后回车或点「搜索」。"}
            </p>
          ) : (
            <Table>
              <TableHeader className="sticky top-0 z-10 bg-card">
                <TableRow>
                  <TableHead className="w-8">
                    <Checkbox
                      checked={picked.size === rows.length}
                      onCheckedChange={(v) => setPicked(v === true ? new Set(rows.map((_, i) => i)) : new Set())}
                    />
                  </TableHead>
                  <TableHead>host / IP</TableHead>
                  <TableHead className="w-20">端口</TableHead>
                  <TableHead className="w-24">协议</TableHead>
                  <TableHead>域名</TableHead>
                  <TableHead>标题</TableHead>
                  <TableHead className="w-28">Server</TableHead>
                  {cols.map((c) => (
                    <TableHead key={c}>{c}</TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((a, i) => (
                  <TableRow key={rowKey(a, i)}>
                    <TableCell>
                      <Checkbox
                        checked={picked.has(i)}
                        onCheckedChange={() =>
                          setPicked((prev) => {
                            const next = new Set(prev);
                            if (next.has(i)) next.delete(i);
                            else next.add(i);
                            return next;
                          })
                        }
                      />
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {a.host || a.ip || <span className="text-muted-foreground">—</span>}
                    </TableCell>
                    <TableCell className="text-xs tabular-nums">{a.port || "—"}</TableCell>
                    <TableCell className="text-xs">{a.protocol || "—"}</TableCell>
                    <TableCell className="font-mono text-xs">{a.domain || "—"}</TableCell>
                    <TableCell className="max-w-[16rem] truncate text-xs" title={a.title}>
                      {a.title || "—"}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{a.server || "—"}</TableCell>
                    {cols.map((c) => (
                      <TableCell key={c} className="max-w-[14rem] truncate text-xs text-muted-foreground">
                        {a.extra?.[c] ?? "—"}
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t px-6 py-3">
          <span className="text-muted-foreground text-xs">
            已选 {picked.size} 条 · 导入走与 CSV 导入同一套口径（按形状分类、<b>已存在只合并</b>）
          </span>
          {res?.spool && (
            <span className="text-muted-foreground font-mono text-[11px]" title={res.spool}>
              结果已落盘
            </span>
          )}
          <div className="ml-auto flex items-center gap-2">
            <Button variant="outline" onClick={() => setOpen(false)}>
              关闭
            </Button>
            <Button onClick={() => void importPicked()} disabled={importing || picked.size === 0}>
              {importing ? <Loader2Icon className="animate-spin" /> : <CheckIcon />}
              导入为资产
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
