"use client";

import * as React from "react";

import {
  BuildingIcon,
  DownloadIcon,
  GlobeIcon,
  LayersIcon,
  LayoutTemplateIcon,
  LinkIcon,
  type LucideIcon,
  NetworkIcon,
  RefreshCwIcon,
  SmartphoneIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { AssetDslSearch } from "@/components/asset-dsl-search";
import { AssetImportDialog } from "@/components/asset-import-dialog";
import { CompanyKnowledgeDialog } from "@/components/company-knowledge-dialog";
import { FofaSearchDialog } from "@/components/fofa-search-dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { Asset, Company } from "@/lib/types";
import { cn } from "@/lib/utils";

import {
  AppendScopeDialog,
  AssetCard,
  assetExtras,
  assetPortStatus,
  assetTarget,
  CompanyAvatar,
  CompanyDialog,
  csvCell,
  EditScopeDialog,
  fmtSeen,
  KIND_LABEL,
} from "./_components/asset-parts";

const METHOD_COLOR: Record<string, string> = {
  GET: "bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-400",
  POST: "bg-blue-100 text-blue-700 dark:bg-blue-500/15 dark:text-blue-400",
  PUT: "bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-400",
  PATCH: "bg-orange-100 text-orange-700 dark:bg-orange-500/15 dark:text-orange-400",
  DELETE: "bg-red-100 text-red-700 dark:bg-red-500/15 dark:text-red-400",
  HEAD: "bg-purple-100 text-purple-700 dark:bg-purple-500/15 dark:text-purple-400",
  OPTIONS: "bg-slate-100 text-slate-600 dark:bg-slate-500/15 dark:text-slate-400",
};

function MethodBadge({ method }: { method: string }) {
  const m = method.toUpperCase();
  return (
    <span
      className={cn(
        "inline-block rounded px-1.5 py-0.5 font-mono text-[10px] font-semibold leading-none",
        METHOD_COLOR[m] ?? "bg-muted text-muted-foreground",
      )}
    >
      {m || "—"}
    </span>
  );
}

function statusTone(code: number) {
  if (code >= 500) return "text-red-500 dark:text-red-400";
  if (code >= 400) return "text-amber-500 dark:text-amber-400";
  if (code >= 300) return "text-blue-500 dark:text-blue-400";
  if (code >= 200) return "text-emerald-500 dark:text-emerald-400";
  return "text-muted-foreground";
}

const TABS: { key: string; label: string; icon: LucideIcon }[] = [
  // P28：「全部」是默认档——6 类资产同在一张表里，类型降级为列 + 筛选，不再一类一个标签。
  { key: "all", label: "全部", icon: LayersIcon },
  { key: "company", label: "企业", icon: BuildingIcon },
  { key: "root_domain", label: "根域名", icon: GlobeIcon },
  { key: "ip", label: "IP", icon: NetworkIcon },
  { key: "subdomain", label: "子域名", icon: GlobeIcon },
  { key: "app", label: "应用", icon: SmartphoneIcon },
  { key: "service", label: "服务", icon: LayoutTemplateIcon },
  { key: "endpoint", label: "接口", icon: LinkIcon },
];

export default function AssetsPage() {
  const [rows, setRows] = React.useState<Asset[]>([]);
  const [exporting, setExporting] = React.useState(false);
  const [total, setTotal] = React.useState(0);
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [tab, setTab] = React.useState("company");
  const [query, setQuery] = React.useState("");
  const [loaded, setLoaded] = React.useState(false);
  const [loading, setLoading] = React.useState(false);
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [dslError, setDslError] = React.useState("");
  const [refreshKey, setRefreshKey] = React.useState(0);
  const assetsRequestRef = React.useRef(0);
  const [rowsTab, setRowsTab] = React.useState("");

  // asset selection & delete
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [deleteIds, setDeleteIds] = React.useState<number[]>([]);
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  // company delete
  const [companyDeleteTarget, setCompanyDeleteTarget] = React.useState<Company | null>(null);
  const [companyDeleteAssets, setCompanyDeleteAssets] = React.useState(false);
  const [companyDeleting, setCompanyDeleting] = React.useState(false);

  // Companies + per-type counts (tab badges) — loaded on demand, no background polling.
  const loadMeta = React.useCallback(() => {
    api
      .companies()
      .then(setCompanies)
      .catch(() => {
        /* Keep the last successful company snapshot on a transient failure. */
      });
    api
      .assetCounts()
      .then(setCounts)
      .catch(() => {
        /* Keep the last successful counters on a transient failure. */
      });
  }, []);

  // Manual refresh: reload counts/companies and re-fetch the current page.
  const refresh = React.useCallback(() => {
    loadMeta();
    setRefreshKey((k) => k + 1);
    setSelected(new Set());
  }, [loadMeta]);

  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const toggleSelectAll = (ids: number[]) => {
    setSelected((prev) => {
      const allSelected = ids.every((id) => prev.has(id));
      const next = new Set(prev);
      if (allSelected) {
        ids.forEach((id) => {
          next.delete(id);
        });
      } else {
        ids.forEach((id) => {
          next.add(id);
        });
      }
      return next;
    });
  };

  const openDelete = (ids: number[]) => {
    setDeleteIds(ids);
    setDeleteOpen(true);
  };

  const confirmDelete = async () => {
    setDeleting(true);
    try {
      const res = await api.deleteAssets(deleteIds);
      toast.success(`已删除 ${res.deleted} 条资产`);
      setSelected(new Set());
      refresh();
    } catch (e) {
      toast.error("删除失败：" + String((e as Error)?.message ?? e));
    } finally {
      setDeleting(false);
      setDeleteOpen(false);
    }
  };

  const confirmDeleteCompany = async () => {
    if (!companyDeleteTarget) return;
    setCompanyDeleting(true);
    try {
      const res = await api.deleteCompany(companyDeleteTarget.id, companyDeleteAssets);
      const msg =
        companyDeleteAssets && res.assets_deleted > 0
          ? `已删除企业，同时删除 ${res.assets_deleted} 条资产`
          : "已删除企业";
      toast.success(msg);
      refresh();
    } catch (e) {
      toast.error("删除失败：" + String((e as Error)?.message ?? e));
    } finally {
      setCompanyDeleting(false);
      setCompanyDeleteTarget(null);
      setCompanyDeleteAssets(false);
    }
  };

  React.useEffect(() => {
    loadMeta();
  }, [loadMeta]);

  // Reset query + page + selection when switching tabs
  // biome-ignore lint/correctness/useExhaustiveDependencies: tab changes intentionally reset tab-local controls.
  React.useEffect(() => {
    setQuery("");
    setDslError("");
    setSelected(new Set());
    setRows([]);
    setRowsTab("");
    setTotal(0);
    setLoaded(false);
  }, [tab]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: these controls intentionally reset server pagination.
  React.useEffect(() => setPage(0), [tab, size, query]);

  const dslMode = query.trim() !== "";

  // Server-side paginated page loader for the active data tab (company tab excluded).
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is an explicit manual-reload trigger.
  React.useEffect(() => {
    if (tab === "company") return;
    const request = ++assetsRequestRef.current;
    const dsl = query.trim();
    setLoading(true);
    const offset = page * size;
    const run = async () => {
      try {
        // 「全部」把 type 传空：后端空类型 = 全类型（P28），DSL 也随之跨类型匹配。
        const typ = tab === "all" ? "" : tab;
        const r = dsl ? await api.searchAssets(dsl, typ, size, offset) : await api.assets(typ, size, offset);
        if (assetsRequestRef.current !== request) return;
        setRows(r.assets);
        setRowsTab(tab);
        setTotal(r.total);
        setDslError("");
      } catch (e) {
        if (assetsRequestRef.current !== request) return;
        setDslError(String((e as Error)?.message ?? e));
        setRows([]);
        setRowsTab(tab);
        setTotal(0);
      } finally {
        if (assetsRequestRef.current === request) {
          setLoading(false);
          setLoaded(true);
        }
      }
    };
    const tid = setTimeout(run, dsl ? 400 : 0);
    return () => {
      clearTimeout(tid);
      if (assetsRequestRef.current === request) assetsRequestRef.current++;
    };
  }, [tab, page, size, query, refreshKey]);

  React.useEffect(() => {
    if (tab === "company") return;
    const lastPage = Math.max(0, Math.ceil(total / size) - 1);
    if (page > lastPage) setPage(lastPage);
  }, [page, size, tab, total]);

  const companyById = React.useMemo(() => {
    const m = new Map<number, string>();
    for (const c of companies) m.set(c.id, c.name);
    return m;
  }, [companies]);

  const companyName = (id?: number) => (id ? (companyById.get(id) ?? "") : "");

  const tabCounts: Record<string, number> = { ...counts, company: companies.length };
  const totalAssets = Object.values(counts).reduce((a, b) => a + b, 0);

  // rows are already the current server-side page.
  const tabData = (type: string): Asset[] => (rowsTab === type ? rows : []);
  const currentRows = tabData(tab);
  const slice = <T,>(list: T[]) => list;

  const searchBox = (
    <AssetDslSearch
      query={query}
      onChange={setQuery}
      loading={loading}
      error={dslError}
      count={dslMode ? total : undefined}
    />
  );

  // exportCSV 导出**当前筛选条件**下的资产（不是当前页）：逐页拉取，封顶 5000 行并如实提示截断。
  // 列名用导入侧认得的 host/ip/url（P26 的表头口径），因此导出的文件可以直接再导入。
  async function exportCSV() {
    const cap = 5000;
    const pageSize = 500;
    setExporting(true);
    try {
      const typ = tab === "all" ? "" : tab;
      const dsl = query.trim();
      const out: string[] = ["host,ip,url,type,title,company,last_seen"];
      let offset = 0;
      let total = 0;
      for (;;) {
        const r = dsl ? await api.searchAssets(dsl, typ, pageSize, offset) : await api.assets(typ, pageSize, offset);
        total = r.total;
        for (const a of r.assets) {
          const host = a.domain || a.root_domain || "";
          const url = a.url || "";
          out.push(
            [
              host,
              a.ip || a.c_segment || "",
              url,
              KIND_LABEL[a.type] ?? a.type,
              a.page_title || a.app_name || a.service_name || "",
              companyName(a.company_id),
              a.last_seen || "",
            ]
              .map(csvCell)
              .join(","),
          );
        }
        offset += r.assets.length;
        if (r.assets.length === 0 || offset >= r.total || out.length - 1 >= cap) break;
      }
      const truncated = total > out.length - 1;
      const blob = new Blob(["\ufeff" + out.join("\n") + "\n"], { type: "text/csv;charset=utf-8" });
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `blade-assets-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();
      URL.revokeObjectURL(a.href);
      toast.success(
        truncated
          ? `已导出 ${out.length - 1} 行（命中 ${total} 条，封顶 ${cap} 行，其余请缩小筛选后再导）`
          : `已导出 ${out.length - 1} 行`,
      );
    } catch (e) {
      toast.error("导出失败：" + String((e as Error)?.message ?? e));
    } finally {
      setExporting(false);
    }
  }

  return (
    <div className="flex h-[calc(100vh-6rem)] min-h-0 flex-col gap-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">资产</h1>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-sm text-muted-foreground">
            共 <span className="tabular-nums">{totalAssets}</span> 项资产
          </span>
          {selected.size > 0 && (
            <Button variant="destructive" size="sm" onClick={() => openDelete(Array.from(selected) as number[])}>
              <Trash2Icon className="size-3.5" /> 删除已选 ({selected.size})
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={() => void exportCSV()} disabled={exporting}>
            <DownloadIcon className={cn("size-4", exporting && "animate-spin")} />
            {exporting ? "导出中…" : "导出 CSV"}
          </Button>
          <FofaSearchDialog onImported={refresh} />
          <Button variant="outline" size="sm" onClick={refresh} disabled={loading}>
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} /> 刷新
          </Button>
          <AssetImportDialog onImported={refresh} />
          <CompanyDialog onSaved={refresh} />
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab} className="flex min-h-0 flex-1 flex-col gap-4">
        <div className="overflow-x-auto overflow-y-hidden">
          <TabsList variant="default">
            {TABS.map((t) => (
              <TabsTrigger key={t.key} value={t.key}>
                <t.icon className="size-3.5" />
                {t.label}
                <span className="ml-1 tabular-nums text-muted-foreground">{tabCounts[t.key]}</span>
              </TabsTrigger>
            ))}
          </TabsList>
        </div>

        {/* 企业 */}
        <TabsContent value="company" className="mt-0 flex min-h-0 flex-1 flex-col">
          <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
            <div className="min-h-0 flex-1 overflow-auto">
              <Table>
                <TableHeader className="sticky top-0 z-10 bg-card">
                  <TableRow>
                    <TableHead>企业</TableHead>
                    <TableHead className="w-24 text-right">资产数</TableHead>
                    <TableHead>资产范围</TableHead>
                    <TableHead className="w-36 text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {companies.map((c) => (
                    <TableRow key={c.id}>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          <CompanyAvatar name={c.name} logo={c.logo} />
                          <span className="font-medium">{c.name}</span>
                        </div>
                      </TableCell>
                      <TableCell className="text-right tabular-nums text-sm">{c.asset_count}</TableCell>
                      <TableCell>
                        {c.scope?.length ? (
                          <div className="flex flex-wrap gap-1">
                            {c.scope.map((s, i) => (
                              <Badge key={i} variant="secondary" className="font-mono text-[11px]">
                                {s.raw}
                              </Badge>
                            ))}
                          </div>
                        ) : (
                          <span className="text-xs text-muted-foreground">未设置范围</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-1.5">
                          <CompanyKnowledgeDialog company={c} onChanged={refresh} />
                          <EditScopeDialog company={c} onSaved={refresh} />
                          <AppendScopeDialog company={c} onSaved={refresh} />
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-7 text-muted-foreground hover:text-destructive"
                            onClick={() => {
                              setCompanyDeleteTarget(c);
                              setCompanyDeleteAssets(false);
                            }}
                            aria-label={`删除企业 ${c.name}`}
                          >
                            <Trash2Icon className="size-3.5" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                  {companies.length === 0 && (
                    <TableRow>
                      <TableCell colSpan={4} className="py-10 text-center text-sm text-muted-foreground">
                        还没有企业。点击右上角「新增企业」并填写资产范围，系统会自动认领命中的资产。
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          </Card>
        </TabsContent>

        {/* 资产表：全部类型与 6 类资产共用同一张表——「全部」下多一列类型，选中某一类时该列隐藏。
            列按 kind 渲染目标与附加信息（assetTarget/assetPortStatus/assetExtras），
            不再一类一张表：加一类资产不用再抄一份表格代码。 */}
        {(["all", "root_domain", "ip", "subdomain", "app", "service", "endpoint"] as const).map((kind) => (
          <TabsContent key={kind} value={kind} className="mt-0 flex min-h-0 flex-1 flex-col gap-2">
            {searchBox}
            <AssetCard
              cols={
                kind === "all"
                  ? ["", "类型", "目标", "端口 / 状态", "标题 / 技术", "归属企业", "最后发现", ""]
                  : ["", "目标", "端口 / 状态", "标题 / 技术", "归属企业", "最后发现", ""]
              }
              loaded={loaded}
              total={total}
              page={page}
              size={size}
              onSize={setSize}
              onPage={setPage}
              rows={currentRows}
              selected={selected}
              onToggleAll={(ids) => toggleSelectAll(ids)}
            >
              {slice(tabData(kind)).map((a) => (
                <TableRow key={a.id} className={selected.has(a.id) ? "bg-muted/40" : undefined}>
                  <TableCell className="w-8 pr-0">
                    <Checkbox checked={selected.has(a.id)} onCheckedChange={() => toggleSelect(a.id)} />
                  </TableCell>
                  {kind === "all" && (
                    <TableCell>
                      <Badge variant="secondary" className="text-[11px]">
                        {KIND_LABEL[a.type] ?? a.type}
                      </Badge>
                    </TableCell>
                  )}
                  <TableCell className="font-mono text-xs font-medium">{assetTarget(a)}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{assetPortStatus(a)}</TableCell>
                  <TableCell className="max-w-[22rem] truncate text-xs text-muted-foreground" title={assetExtras(a)}>
                    {assetExtras(a)}
                  </TableCell>
                  <TableCell className="text-xs">
                    {companyName(a.company_id) || <span className="text-muted-foreground">未归属</span>}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{fmtSeen(a.last_seen)}</TableCell>
                  <TableCell className="w-8 pl-0">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-7 text-muted-foreground hover:text-destructive"
                      onClick={() => openDelete([a.id])}
                      aria-label={`删除资产 ${assetTarget(a)}`}
                    >
                      <Trash2Icon className="size-3.5" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </AssetCard>
          </TabsContent>
        ))}
      </Tabs>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除</AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除 <span className="font-semibold tabular-nums">{deleteIds.length}</span>{" "}
              条资产记录，此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmDelete();
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={!!companyDeleteTarget}
        onOpenChange={(o) => {
          if (!o) {
            setCompanyDeleteTarget(null);
            setCompanyDeleteAssets(false);
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除企业 · {companyDeleteTarget?.name}</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <div className="space-y-3">
                <p>此操作将永久删除该企业及其资产范围配置，不可撤销。</p>
                <label
                  htmlFor="delete-assets-opt"
                  className="flex cursor-pointer items-center gap-2.5 rounded-md border p-3 hover:bg-muted/50"
                >
                  <Checkbox
                    id="delete-assets-opt"
                    checked={companyDeleteAssets}
                    onCheckedChange={(v) => setCompanyDeleteAssets(!!v)}
                  />
                  <span className="text-sm leading-snug">
                    同时删除该企业下的所有资产
                    <span className="block text-xs text-muted-foreground">不勾选则保留资产，仅取消归属关系</span>
                  </span>
                </label>
              </div>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={companyDeleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmDeleteCompany();
              }}
              disabled={companyDeleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {companyDeleting ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// csvCell：CSV 单元格转义（含逗号/引号/换行就加引号，引号翻倍）。
