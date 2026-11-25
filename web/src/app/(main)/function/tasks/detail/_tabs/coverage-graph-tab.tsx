"use client";

import * as React from "react";

import type { Graph as G6Graph } from "@antv/g6";
import {
  AppWindow,
  Building2,
  ChevronDown,
  ChevronUp,
  Globe,
  Link2,
  type LucideIcon,
  Maximize2,
  Radio,
  RefreshCw,
  RotateCcw,
  Search,
  Server,
  Waypoints,
  X,
} from "lucide-react";
import { useTheme } from "next-themes";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import {
  COVERAGE_FOLD_LIMIT,
  COVERAGE_FOLD_STEP,
  type CoverageFoldNode,
  type CoverageKind,
  type CoverageRenderNode,
  type CoverageStatusFilter,
  type CoverageViewMode,
  type CoverageZone,
  collectCoverageLineage,
  computeVisibleCoverageNodes,
  coverageZoneOf,
  filterCoverageGraphByStatus,
  findMatchingCoverageKeys,
  layoutCoverageAssetNodes,
  layoutCoverageNetworkNodes,
  NETWORK_WORLD_W,
} from "@/lib/coverage-layout";
import type {
  CoverageAssetRef,
  CoverageAssetRefs,
  CoverageGraphEdge,
  CoverageGraphNode,
  ExplorationLedgerResponse,
} from "@/lib/types";
import { cn } from "@/lib/utils";

const zoneMeta: Record<CoverageZone, { label: string; description: string; className: string }> = {
  internet: {
    label: "互联网区",
    description: "公网域名 / 外部入口",
    className: "bg-sky-50/70 dark:bg-sky-950/20",
  },
  boundary: {
    label: "边界区",
    description: "服务 / 网关 / 暴露面",
    className: "bg-amber-50/70 dark:bg-amber-950/15",
  },
  internal: {
    label: "内网区",
    description: "业务资产 / 应用 / 端点",
    className: "bg-emerald-50/65 dark:bg-emerald-950/15",
  },
};

type KindMeta = { label: string; icon: LucideIcon; iconBg: string; hex: string; size: number };

const kindMeta: Record<CoverageKind, KindMeta> = {
  company: { label: "公司", icon: Building2, iconBg: "bg-slate-500", hex: "#64748b", size: 46 },
  root_domain: { label: "根域名", icon: Globe, iconBg: "bg-indigo-500", hex: "#6366f1", size: 38 },
  subdomain: { label: "子域名", icon: Waypoints, iconBg: "bg-blue-500", hex: "#3b82f6", size: 30 },
  ip: { label: "IP", icon: Server, iconBg: "bg-cyan-600", hex: "#0891b2", size: 28 },
  service: { label: "服务", icon: Radio, iconBg: "bg-amber-500", hex: "#f59e0b", size: 26 },
  app: { label: "App", icon: AppWindow, iconBg: "bg-fuchsia-500", hex: "#d946ef", size: 26 },
  endpoint: { label: "端点", icon: Link2, iconBg: "bg-rose-500", hex: "#f43f5e", size: 22 },
};

// G6 节点图标用平台一致的 lucide 图标：把 lucide 的 SVG 路径渲染成白色描边 data URI。
function svgUri(inner: string, filled = false): string {
  const attrs = filled
    ? 'fill="#fff" stroke="none"'
    : 'fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" ${attrs}>${inner}</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const KIND_ICON: Record<CoverageKind, string> = {
  company: svgUri(
    '<path d="M10 12h4"/><path d="M10 8h4"/><path d="M14 21v-3a2 2 0 0 0-4 0v3"/><path d="M6 10H4a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-2"/><path d="M6 21V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v16"/>',
  ),
  root_domain: svgUri(
    '<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>',
  ),
  subdomain: svgUri(
    '<path d="m10.586 5.414-5.172 5.172"/><path d="m18.586 13.414-5.172 5.172"/><path d="M6 12h12"/><circle cx="12" cy="20" r="2"/><circle cx="12" cy="4" r="2"/><circle cx="20" cy="12" r="2"/><circle cx="4" cy="12" r="2"/>',
  ),
  ip: svgUri(
    '<rect width="20" height="8" x="2" y="2" rx="2" ry="2"/><rect width="20" height="8" x="2" y="14" rx="2" ry="2"/><line x1="6" x2="6.01" y1="6" y2="6"/><line x1="6" x2="6.01" y1="18" y2="18"/>',
  ),
  service: svgUri(
    '<path d="M16.247 7.761a6 6 0 0 1 0 8.478"/><path d="M19.075 4.933a10 10 0 0 1 0 14.134"/><path d="M4.925 19.067a10 10 0 0 1 0-14.134"/><path d="M7.753 16.239a6 6 0 0 1 0-8.478"/><circle cx="12" cy="12" r="2"/>',
  ),
  app: svgUri(
    '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="M10 4v4"/><path d="M2 8h20"/><path d="M6 4v4"/>',
  ),
  endpoint: svgUri(
    '<path d="M9 17H7A5 5 0 0 1 7 7h2"/><path d="M15 7h2a5 5 0 1 1 0 10h-2"/><line x1="8" x2="16" y1="12" y2="12"/>',
  ),
};

type DatabaseEngine = "mysql" | "sqlserver" | "postgres" | "redis" | "database";

const DATABASE_ICON: Record<DatabaseEngine, string> = {
  mysql: svgUri('<text x="12" y="15" text-anchor="middle" font-size="8" font-weight="700">MY</text>', true),
  sqlserver: svgUri('<text x="12" y="15" text-anchor="middle" font-size="7" font-weight="700">MS</text>', true),
  postgres: svgUri('<text x="12" y="15" text-anchor="middle" font-size="7" font-weight="700">PG</text>', true),
  redis: svgUri('<text x="12" y="15" text-anchor="middle" font-size="8" font-weight="700">RD</text>', true),
  database: svgUri(
    '<ellipse cx="12" cy="6" rx="7" ry="3"/><path d="M5 6v9c0 1.7 3.1 3 7 3s7-1.3 7-3V6"/><path d="M5 10c0 1.7 3.1 3 7 3s7-1.3 7-3"/>',
  ),
};

function databaseEngine(node: CoverageGraphNode): DatabaseEngine | null {
  const raw = `${node.service_type ?? ""} ${node.label} ${node.page_title ?? ""}`.toLowerCase();
  if (/mysql|mariadb/.test(raw)) return "mysql";
  if (/sql\s*server|mssql|microsoft sql/.test(raw)) return "sqlserver";
  if (/postgres|postgresql/.test(raw)) return "postgres";
  if (/redis/.test(raw)) return "redis";
  if (/database|数据库|dbms|oracle|mongodb|mongo|clickhouse/.test(raw)) return "database";
  return null;
}

const FOLD_ICON = svgUri(
  '<circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/>',
  true,
);

type G6NodeDatum = {
  id: string;
  kind: CoverageKind;
  fold: boolean;
  tested: boolean;
  inScope: boolean;
  lbl: string;
  size: number;
  active: boolean;
  searchHit: boolean;
  iconSrc: string;
  x: number;
  y: number;
  style: { x: number; y: number };
};

type G6EdgeDatum = { source: string; target: string; active: boolean };

function trunc(s: string, n = 26): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}

function graphLabel(n: CoverageGraphNode, viewMode: CoverageViewMode = "asset"): string {
  if (viewMode === "network") {
    if (n.kind === "endpoint") {
      if (n.url) {
        try {
          return new URL(n.url).pathname || "/";
        } catch {
          /* 非法 URL：回退到资产标签 */
        }
      }
      return n.label || n.domain || n.ip || "端点";
    }
    if (n.kind === "service") {
      const engine = databaseEngine(n);
      const host = n.domain || n.ip || n.label;
      const suffix = n.port ? `:${n.port}` : "";
      return engine ? `${engine.toUpperCase()} · ${host}${suffix}` : `${host}${suffix}`;
    }
    return n.label || n.domain || n.ip || n.app_name || kindMeta[n.kind].label;
  }
  if (n.kind === "service") {
    const parts: string[] = [];
    if (n.port) parts.push(`:${n.port}`);
    if (n.page_title) parts.push(n.page_title);
    if (n.status_code) parts.push(String(n.status_code));
    return parts.length ? parts.join(" · ") : n.domain || n.ip || n.label;
  }
  if (n.kind === "endpoint" && n.url) {
    try {
      return new URL(n.url).pathname || "/";
    } catch {
      /* 非法 URL：回退到完整 label */
    }
  }
  return n.label;
}

function toG6Nodes(
  renderNodes: CoverageRenderNode[],
  positions: Map<string, { x: number; y: number }>,
  activeKeys: Set<string>,
  searchHitKey: string | null,
  viewMode: CoverageViewMode,
): G6NodeDatum[] {
  return renderNodes.map((rn) => {
    const position = positions.get(rn.key) ?? { x: 400, y: 300 };
    if (rn.fold) {
      return {
        id: rn.key,
        kind: rn.kind,
        fold: true,
        tested: false,
        inScope: false,
        lbl: `还有 ${rn.hidden.length} 个${kindMeta[rn.kind].label}`,
        size: 24,
        active: activeKeys.has(rn.key),
        searchHit: searchHitKey === rn.key,
        iconSrc: FOLD_ICON,
        style: { x: position.x, y: position.y },
        ...position,
      };
    }
    let maxLen = 26;
    if (viewMode === "network") {
      maxLen = rn.kind === "service" ? 24 : 20;
    }
    const baseLbl = graphLabel(rn.node, viewMode) || kindMeta[rn.kind].label;
    return {
      id: rn.key,
      kind: rn.kind,
      fold: false,
      tested: rn.node.tested,
      inScope: rn.node.in_scope,
      lbl: `${rn.node.tested ? "✓ " : ""}${trunc(baseLbl, maxLen)}`,
      size: kindMeta[rn.kind].size,
      active: activeKeys.has(rn.key),
      searchHit: searchHitKey === rn.key,
      iconSrc:
        viewMode === "network" && databaseEngine(rn.node)
          ? DATABASE_ICON[databaseEngine(rn.node) as DatabaseEngine]
          : KIND_ICON[rn.kind],
      style: { x: position.x, y: position.y },
      ...position,
    };
  });
}

const nd = (d: unknown) => d as G6NodeDatum;

function nodeFill(d: G6NodeDatum, isDark: boolean): string {
  if (d.fold) return isDark ? "#1e293b" : "#f1f5f9";
  if (!d.inScope) return isDark ? "#334155" : "#cbd5e1";
  if (d.tested) return kindMeta[d.kind].hex;
  return "#64748b";
}

function nodeStroke(d: G6NodeDatum, isDark: boolean): string {
  if (d.searchHit) return "#f59e0b";
  if (d.active) return "#0d9488";
  if (d.fold || !d.inScope) return isDark ? "#64748b" : "#94a3b8";
  if (d.tested) return "#10b981";
  return isDark ? "#94a3b8" : "#475569";
}

function nodeLineWidth(d: G6NodeDatum): number {
  if (d.searchHit) return 3.5;
  if (d.active) return 3;
  if (d.fold || !d.inScope) return 1.2;
  return d.tested ? 2.8 : 1.5;
}

// ---------------------------------------------------------------------------
// 抽屉：资产节点看详情；折叠节点看隐藏列表 + "展示更多"。
// ---------------------------------------------------------------------------
function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === "") return null;
  return (
    <div className="flex items-start gap-3 py-1 text-sm">
      <span className="w-20 shrink-0 text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 break-words text-foreground">{children}</span>
    </div>
  );
}

function RefList({ title, items }: { title: string; items: CoverageAssetRef[] }) {
  if (items.length === 0) return null;
  return (
    <div>
      <h4 className="mb-1 font-medium text-muted-foreground text-xs">
        {title}（{items.length}）
      </h4>
      <div className="flex flex-col gap-1">
        {items.map((r) => (
          <div
            key={`${r.kind}-${r.id}`}
            className="flex flex-wrap items-start gap-2 rounded-md bg-muted/50 px-2 py-1.5 text-xs"
          >
            <span className="shrink-0 font-mono text-muted-foreground">#{r.id}</span>
            {r.state && <span className="shrink-0 text-muted-foreground">{r.state}</span>}
            <span className="min-w-32 flex-1 break-words text-foreground">{r.summary || "—"}</span>
            {r.inherited && r.source_task_id && (
              <Badge variant="outline" className="shrink-0">
                来源 #{r.source_task_id} · 只读
              </Badge>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function AssetSheet({
  node,
  taskId,
  onOpenChange,
}: {
  node: CoverageGraphNode | null;
  taskId: string;
  onOpenChange: (open: boolean) => void;
}) {
  const meta = node ? kindMeta[node.kind] : null;
  const Icon = meta?.icon;
  const raw = node ? JSON.stringify(node, null, 2) : "";
  const [refs, setRefs] = React.useState<CoverageAssetRefs | null>(null);

  React.useEffect(() => {
    setRefs(null);
    if (!node?.asset_id) return;
    let cancelled = false;
    api
      .taskAssetRefs(taskId, node.asset_id)
      .then((r) => {
        if (!cancelled) setRefs(r);
      })
      .catch(() => {
        /* 无关联或出错：不展示该区块 */
      });
    return () => {
      cancelled = true;
    };
  }, [node, taskId]);

  return (
    <Sheet open={node !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-lg">
        {node && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span
                  className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}
                >
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <SheetTitle className="leading-tight">{meta.label}</SheetTitle>
                  <span className="block truncate font-mono text-muted-foreground text-xs" title={node.label}>
                    {node.label}
                  </span>
                </div>
              </div>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="mb-1 font-medium text-muted-foreground text-xs">属性</h4>
                  <DetailRow label="类型">{meta.label}</DetailRow>
                  <DetailRow label="测试状态">
                    {node.in_scope ? (
                      node.tested ? (
                        <span className="text-emerald-600 dark:text-emerald-400">已测试</span>
                      ) : (
                        <span className="text-neutral-500">未测试</span>
                      )
                    ) : (
                      <span className="text-neutral-400">范围外（连接节点）</span>
                    )}
                  </DetailRow>
                  <DetailRow label="域名">{node.domain}</DetailRow>
                  <DetailRow label="根域名">{node.root_domain}</DetailRow>
                  <DetailRow label="IP">{node.ip}</DetailRow>
                  <DetailRow label="端口">{node.port ? node.port : undefined}</DetailRow>
                  <DetailRow label="URL">
                    {node.url ? <span className="break-all font-mono text-xs">{node.url}</span> : undefined}
                  </DetailRow>
                  <DetailRow label="标题">{node.page_title}</DetailRow>
                  <DetailRow label="状态码">{node.status_code ? node.status_code : undefined}</DetailRow>
                  <DetailRow label="App">{node.app_name}</DetailRow>
                  <DetailRow label="资产ID">
                    {node.asset_id ? <span className="font-mono text-xs">{node.asset_id}</span> : undefined}
                  </DetailRow>
                </section>
                {refs && (refs.intents.length > 0 || refs.facts.length > 0 || refs.findings.length > 0) && (
                  <section className="flex flex-col gap-3 border-t pt-3">
                    <RefList title="关联意图" items={refs.intents} />
                    <RefList title="关联事实" items={refs.facts} />
                    <RefList title="关联发现" items={refs.findings} />
                  </section>
                )}
                <section className="border-t pt-3">
                  <h4 className="mb-1.5 font-medium text-muted-foreground text-xs">原始数据</h4>
                  <pre className="max-w-full overflow-hidden whitespace-pre-wrap break-all rounded-md border bg-muted/50 p-3 font-mono text-foreground text-xs leading-relaxed">
                    {raw}
                  </pre>
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function FoldSheet({
  fold,
  onOpenChange,
  onShowMore,
  onPick,
}: {
  fold: CoverageFoldNode | null;
  onOpenChange: (open: boolean) => void;
  onShowMore: (groupId: string) => void;
  onPick: (n: CoverageGraphNode) => void;
}) {
  const meta = fold ? kindMeta[fold.kind] : null;
  const Icon = meta?.icon;
  return (
    <Sheet open={fold !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {fold && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <SheetTitle className="text-base">
                未展示的{meta.label}（{fold.hidden.length}）
              </SheetTitle>
              <p className="text-muted-foreground text-xs">已测优先展示。点「展示更多」把下一批拉进图里。</p>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex flex-col gap-1 p-3">
                {fold.hidden.map((n) => (
                  <button
                    key={n.key}
                    type="button"
                    onClick={() => onPick(n)}
                    className="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent"
                  >
                    <span
                      className={cn(
                        "flex size-5 shrink-0 items-center justify-center rounded",
                        n.tested ? meta.iconBg : "bg-neutral-400 dark:bg-neutral-600",
                      )}
                    >
                      <Icon className="size-3 text-white" />
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-xs" title={n.label}>
                      {n.label}
                    </span>
                    {n.tested && <span className="size-1.5 shrink-0 rounded-full bg-emerald-500" />}
                  </button>
                ))}
              </div>
            </ScrollArea>
            <div className="border-t p-3">
              <Button className="w-full" variant="outline" onClick={() => onShowMore(fold.groupId)}>
                展示更多（+{COVERAGE_FOLD_STEP}）
              </Button>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
function GraphInner({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  const { resolvedTheme } = useTheme();
  const isDark = resolvedTheme === "dark";
  const isDarkRef = React.useRef(isDark);
  isDarkRef.current = isDark;

  const [data, setData] = React.useState<{
    nodes: CoverageGraphNode[];
    edges: CoverageGraphEdge[];
  } | null>(null);
  const [expanded, setExpanded] = React.useState<Map<string, number>>(new Map());
  const [selectedAsset, setSelectedAsset] = React.useState<CoverageGraphNode | null>(null);
  const [selectedFoldId, setSelectedFoldId] = React.useState<string | null>(null);
  const [activePath, setActivePath] = React.useState<string[]>([]);
  const [highlightedKind, setHighlightedKind] = React.useState<CoverageKind | null>(null);
  const [statusFilter, setStatusFilter] = React.useState<CoverageStatusFilter>("all");
  const [searchQuery, setSearchQuery] = React.useState("");
  const [searchIndex, setSearchIndex] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [lastSyncedAt, setLastSyncedAt] = React.useState<Date | null>(null);
  const [viewMode, setViewMode] = React.useState<CoverageViewMode>("asset");
  const [legendExpanded, setLegendExpanded] = React.useState(false);
  const [viewport, setViewport] = React.useState<{ zoom: number; position: [number, number] }>({
    zoom: 1,
    position: [0, 0],
  });

  const viewModeRef = React.useRef<CoverageViewMode>(viewMode);
  viewModeRef.current = viewMode;
  const containerRef = React.useRef<HTMLDivElement>(null);
  const graphRef = React.useRef<G6Graph | null>(null);
  const renderMapRef = React.useRef<Map<string, CoverageRenderNode>>(new Map());
  const gDataRef = React.useRef<{ nodes: G6NodeDatum[]; edges: G6EdgeDatum[] }>({ nodes: [], edges: [] });
  const lastFittedStructuralSigRef = React.useRef("");
  const lastAutoSearchRef = React.useRef("");

  const syncViewport = React.useCallback(() => {
    const graph = graphRef.current;
    if (!graph || graph.destroyed) return;
    const position = graph.getPosition();
    setViewport({ zoom: graph.getZoom(), position: [position[0], position[1]] });
  }, []);

  const fitNetworkView = React.useCallback(
    async (graph: G6Graph) => {
      const w = containerRef.current?.clientWidth ?? 0;
      const zoom = w > 0 ? Math.min(1, Math.max(0.45, (w - 24) / NETWORK_WORLD_W)) : 0.78;
      await graph.zoomTo(zoom);
      await graph.translateTo([0, 0]);
      syncViewport();
    },
    [syncViewport],
  );

  const fitFullView = React.useCallback(async () => {
    const graph = graphRef.current;
    if (!graph || graph.destroyed) return;
    if (viewModeRef.current === "network") {
      await fitNetworkView(graph);
    } else {
      await graph.fitView({ when: "always" }, { duration: 240 });
      syncViewport();
    }
  }, [fitNetworkView, syncViewport]);

  const resetView = React.useCallback(
    async (mode: CoverageViewMode = viewModeRef.current) => {
      setActivePath([]);
      setHighlightedKind(null);
      setSelectedAsset(null);
      setSelectedFoldId(null);
      const graph = graphRef.current;
      if (!graph || graph.destroyed) return;
      if (mode === "network") {
        await fitNetworkView(graph);
      } else {
        await graph.zoomTo(1);
        await graph.fitCenter();
        syncViewport();
      }
    },
    [fitNetworkView, syncViewport],
  );

  const fetchGraph = React.useCallback(() => {
    setLoading(true);
    api
      .taskCoverageGraph(taskId)
      .then((g) => {
        const nodes = coverageEnabled ? (g.nodes ?? []) : (g.nodes ?? []).map((n) => ({ ...n, tested: false }));
        setData({ nodes, edges: g.edges ?? [] });
        setLastSyncedAt(new Date());
      })
      .catch(() => {
        /* 保留上一次数据 */
      })
      .finally(() => setLoading(false));
  }, [taskId, coverageEnabled]);

  React.useEffect(() => {
    fetchGraph();
  }, [fetchGraph]);

  const filteredData = React.useMemo(() => {
    if (!data) return null;
    const effectiveFilter: CoverageStatusFilter = coverageEnabled ? statusFilter : "all";
    return filterCoverageGraphByStatus(data.nodes, data.edges, effectiveFilter);
  }, [data, coverageEnabled, statusFilter]);

  const { renderNodes, renderEdges } = React.useMemo(
    () =>
      filteredData
        ? computeVisibleCoverageNodes(filteredData.nodes, filteredData.edges, expanded, viewMode === "network")
        : { renderNodes: [] as CoverageRenderNode[], renderEdges: [] as CoverageGraphEdge[] },
    [filteredData, expanded, viewMode],
  );

  const displayNodeKeys = React.useMemo(() => new Set(renderNodes.map((node) => node.key)), [renderNodes]);
  const displayEdges = React.useMemo(
    () => renderEdges.filter((edge) => displayNodeKeys.has(edge.src) && displayNodeKeys.has(edge.dst)),
    [displayNodeKeys, renderEdges],
  );

  const layoutResult = React.useMemo(
    () =>
      viewMode === "network"
        ? layoutCoverageNetworkNodes(renderNodes)
        : layoutCoverageAssetNodes(renderNodes, displayEdges),
    [renderNodes, displayEdges, viewMode],
  );

  const worldHeight = layoutResult.height;

  const zoneCounts = React.useMemo(() => {
    const counts: Record<CoverageZone, number> = { internet: 0, boundary: 0, internal: 0 };
    for (const node of renderNodes) counts[node.fold ? "boundary" : coverageZoneOf(node.node)] += 1;
    return counts;
  }, [renderNodes]);

  const kindCounts = React.useMemo(() => {
    const counts = new Map<CoverageKind, number>();
    for (const rn of renderNodes) {
      if (!rn.fold) counts.set(rn.kind, (counts.get(rn.kind) ?? 0) + 1);
    }
    return counts;
  }, [renderNodes]);

  const presentKinds = React.useMemo(
    () => (Object.keys(kindMeta) as CoverageKind[]).filter((k) => (kindCounts.get(k) ?? 0) > 0),
    [kindCounts],
  );

  const searchMatches = React.useMemo(
    () => findMatchingCoverageKeys(renderNodes, searchQuery),
    [renderNodes, searchQuery],
  );
  const displaySearchIndex =
    searchMatches.length > 0 ? ((searchIndex % searchMatches.length) + searchMatches.length) % searchMatches.length : 0;
  const activeSearchKey = searchMatches.length > 0 ? searchMatches[displaySearchIndex] : null;

  const kindHighlightSet = React.useMemo(() => {
    if (!highlightedKind) return new Set<string>();
    return new Set(renderNodes.filter((rn) => !rn.fold && rn.kind === highlightedKind).map((rn) => rn.key));
  }, [highlightedKind, renderNodes]);

  const effectiveActiveKeys = React.useMemo(() => {
    const out = new Set<string>(activePath);
    for (const k of kindHighlightSet) out.add(k);
    if (activeSearchKey) out.add(activeSearchKey);
    return out;
  }, [activePath, kindHighlightSet, activeSearchKey]);

  const structuralSig = React.useMemo(
    () =>
      `${renderNodes
        .map((n) => {
          let tag = "u";
          if (n.fold) tag = "f";
          else if (n.node.tested) tag = "t";
          return `${n.key}:${tag}`;
        })
        .sort()
        .join(",")}|${displayEdges.length}|${viewMode}|${statusFilter}`,
    [renderNodes, displayEdges, viewMode, statusFilter],
  );

  const renderSig = React.useMemo(
    () =>
      `${structuralSig}|${activePath.join(",")}|${highlightedKind ?? ""}|${activeSearchKey ?? ""}|${isDark ? 1 : 0}`,
    [structuralSig, activePath, highlightedKind, activeSearchKey, isDark],
  );

  gDataRef.current = {
    nodes: toG6Nodes(renderNodes, layoutResult.positions, effectiveActiveKeys, activeSearchKey, viewMode),
    edges: displayEdges.map((e) => ({
      source: e.dst,
      target: e.src,
      active: effectiveActiveKeys.has(e.src) && effectiveActiveKeys.has(e.dst),
    })),
  };

  const rmap = new Map<string, CoverageRenderNode>();
  for (const rn of renderNodes) rmap.set(rn.key, rn);
  renderMapRef.current = rmap;

  const displayEdgesRef = React.useRef(displayEdges);
  displayEdgesRef.current = displayEdges;

  const applyData = React.useCallback(() => {
    const graph = graphRef.current;
    if (!graph || graph.destroyed) return;
    graph.setData(gDataRef.current);
    const shouldAutoFit = lastFittedStructuralSigRef.current !== structuralSig;
    lastFittedStructuralSigRef.current = structuralSig;

    void graph
      .render()
      .then(() => {
        if (graph.destroyed) return;
        if (!shouldAutoFit) {
          syncViewport();
          return;
        }
        if (viewModeRef.current === "network") {
          return fitNetworkView(graph);
        }
        return graph.fitView({ when: "overflow" }, { duration: 200 }).then(() => syncViewport());
      })
      .catch((err) => {
        if (!graph.destroyed) console.error("[coverage-graph] render:", err);
      });
  }, [structuralSig, fitNetworkView, syncViewport]);

  // 搜索词变更时聚焦第一个命中节点。
  React.useEffect(() => {
    const trimmed = searchQuery.trim();
    if (!trimmed) {
      lastAutoSearchRef.current = "";
      setSearchIndex(0);
      return;
    }
    if (lastAutoSearchRef.current !== trimmed && searchMatches.length > 0) {
      lastAutoSearchRef.current = trimmed;
      setSearchIndex(0);
      const graph = graphRef.current;
      if (graph && !graph.destroyed) {
        void graph.focusElement([searchMatches[0]], { duration: 220 }).then(() => syncViewport());
      }
    }
  }, [searchQuery, searchMatches, syncViewport]);

  const stepSearch = React.useCallback(
    (delta: number) => {
      if (searchMatches.length === 0) return;
      const next =
        (((displaySearchIndex + delta) % searchMatches.length) + searchMatches.length) % searchMatches.length;
      setSearchIndex(next);
      const graph = graphRef.current;
      if (graph && !graph.destroyed) {
        void graph.focusElement([searchMatches[next]], { duration: 220 }).then(() => syncViewport());
      }
    },
    [searchMatches, displaySearchIndex, syncViewport],
  );

  // 建图（一次）。动态 import 避开 SSR/静态导出期的 window 依赖。
  React.useEffect(() => {
    let destroyed = false;
    let graph: G6Graph | null = null;
    void (async () => {
      const { Graph } = await import("@antv/g6");
      if (destroyed || !containerRef.current) return;
      graph = new Graph({
        container: containerRef.current,
        autoResize: true,
        padding: 36,
        zoomRange: [0.3, 3],
        background: "transparent",
        node: {
          style: {
            size: (d: unknown) => nd(d).size,
            fill: (d: unknown) => nodeFill(nd(d), isDarkRef.current),
            stroke: (d: unknown) => nodeStroke(nd(d), isDarkRef.current),
            lineWidth: (d: unknown) => nodeLineWidth(nd(d)),
            lineDash: (d: unknown) => (nd(d).fold || !nd(d).inScope ? [3, 3] : [0]),
            iconSrc: (d: unknown) => nd(d).iconSrc,
            iconWidth: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            iconHeight: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            labelText: (d: unknown) => nd(d).lbl,
            labelFontSize: () => (viewModeRef.current === "network" ? 10 : 10.5),
            labelPlacement: "bottom",
            labelFill: () => (isDarkRef.current ? "#e2e8f0" : "#334155"),
            labelBackground: true,
            labelBackgroundFill: () => (isDarkRef.current ? "rgba(15,23,42,0.88)" : "rgba(255,255,255,0.9)"),
            labelBackgroundRadius: 4,
            labelPadding: [2, 5],
            opacity: (d: unknown) =>
              gDataRef.current.nodes.some((node) => node.active || node.searchHit) && !nd(d).active && !nd(d).searchHit
                ? 0.3
                : 1,
          },
        },
        edge: {
          type: () => (viewModeRef.current === "asset" ? "cubic-horizontal" : "quadratic"),
          style: {
            stroke: (d: unknown) => {
              if ((d as G6EdgeDatum).active) return "#0d9488";
              return isDarkRef.current ? "#475569" : "#94a3b8";
            },
            lineWidth: (d: unknown) => ((d as G6EdgeDatum).active ? 2.6 : 1.2),
            lineDash: (d: unknown) => (viewModeRef.current === "network" && !(d as G6EdgeDatum).active ? [4, 3] : [0]),
            opacity: (d: unknown) =>
              gDataRef.current.edges.some((edge) => edge.active) && !(d as G6EdgeDatum).active ? 0.16 : 0.85,
            endArrow: true,
            endArrowSize: 6,
          },
        },
        behaviors: ["drag-element", "drag-canvas", "zoom-canvas"],
      });
      graph.on("aftertransform", syncViewport);
      graph.on("afterresize", syncViewport);
      graph.on("canvas:click", () => {
        setActivePath([]);
        setHighlightedKind(null);
      });
      graph.on("node:click", (evt: unknown) => {
        const id = (evt as { target?: { id?: string } }).target?.id;
        if (!id) return;
        const rn = renderMapRef.current.get(id);
        if (!rn) return;
        setHighlightedKind(null);
        if (rn.fold) {
          setSelectedFoldId(rn.key);
          return;
        }
        setSelectedAsset(rn.node);
        const { activeKeys } = collectCoverageLineage(rn.node.key, displayEdgesRef.current);
        setActivePath(activeKeys);
        void graph?.focusElement(activeKeys).then(() => syncViewport());
      });
      graphRef.current = graph;
      applyData();
    })();
    return () => {
      destroyed = true;
      try {
        graph?.stopLayout();
      } catch {
        /* 图可能尚未建成或已无布局上下文 */
      }
      graph?.destroy();
      graphRef.current = null;
    };
  }, [applyData, syncViewport]);

  // renderSig 作为重绘触发依赖，gDataRef 已在每次渲染更新为最新快照。
  // biome-ignore lint/correctness/useExhaustiveDependencies: renderSig 精准控制 setData/render 时机
  React.useEffect(() => {
    applyData();
  }, [renderSig, applyData]);

  const showMore = React.useCallback((groupId: string) => {
    setExpanded((prev) => {
      const m = new Map(prev);
      m.set(groupId, (m.get(groupId) ?? COVERAGE_FOLD_LIMIT) + COVERAGE_FOLD_STEP);
      return m;
    });
  }, []);

  const selectedFold =
    selectedFoldId != null
      ? ((renderNodes.find((n) => n.fold && n.key === selectedFoldId) as CoverageFoldNode | undefined) ?? null)
      : null;
  React.useEffect(() => {
    if (selectedFoldId != null && !selectedFold) setSelectedFoldId(null);
  }, [selectedFoldId, selectedFold]);

  const total = data?.nodes.length ?? 0;
  const tested = data?.nodes.filter((n) => n.in_scope && n.tested).length ?? 0;
  const inScope = data?.nodes.filter((n) => n.in_scope).length ?? 0;
  let assetSubtitle = "按资产层级展示关联关系";
  if (loading) {
    assetSubtitle = "正在读取资产库关系…";
  } else if (lastSyncedAt) {
    assetSubtitle = `按资产层级展示关联关系 · ${lastSyncedAt.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })}`;
  }
  let emptyText = "暂无范围内资产（请先锚定任务范围）";
  if (loading) {
    emptyText = "正在读取资产库关系…";
  } else if (total > 0) {
    emptyText = "当前筛选条件下暂无匹配资产";
  }

  return (
    <div className="flex h-[72vh] w-full flex-col overflow-hidden rounded-xl">
      {/* 顶部画布外紧凑工具栏 */}
      <div className="flex flex-wrap items-center gap-2 border-border/60 border-b bg-card/60 px-3 py-2 text-xs">
        <div className="inline-flex items-center rounded-md bg-muted p-0.5">
          <Button
            type="button"
            variant={viewMode === "asset" ? "secondary" : "ghost"}
            size="xs"
            onClick={() => {
              setViewMode("asset");
              void resetView("asset");
            }}
          >
            资产关系图
          </Button>
          <Button
            type="button"
            variant={viewMode === "network" ? "secondary" : "ghost"}
            size="xs"
            onClick={() => {
              setViewMode("network");
              void resetView("network");
            }}
          >
            网络拓扑
          </Button>
        </div>

        <div className="relative flex items-center">
          <Search className="pointer-events-none absolute left-2 size-3.5 text-muted-foreground" />
          <Input
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                stepSearch(e.shiftKey ? -1 : 1);
              } else if (e.key === "Escape") {
                setSearchQuery("");
              }
            }}
            placeholder="搜域名 / IP / 端口 / URL…"
            className="h-7 w-44 pr-6 pl-7 text-xs sm:w-52"
          />
          {searchQuery && (
            <button
              type="button"
              onClick={() => setSearchQuery("")}
              className="absolute right-1.5 text-muted-foreground hover:text-foreground"
              aria-label="清空搜索"
            >
              <X className="size-3" />
            </button>
          )}
        </div>

        {searchQuery.trim() && (
          <div className="flex items-center gap-1">
            <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
              {searchMatches.length > 0 ? `${displaySearchIndex + 1}/${searchMatches.length}` : "0/0"}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              disabled={searchMatches.length === 0}
              onClick={() => stepSearch(-1)}
              title="上一个匹配"
            >
              <ChevronUp className="size-3.5" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              disabled={searchMatches.length === 0}
              onClick={() => stepSearch(1)}
              title="下一个匹配"
            >
              <ChevronDown className="size-3.5" />
            </Button>
          </div>
        )}

        {coverageEnabled && (
          <div className="inline-flex items-center rounded-md border border-border/60 p-0.5">
            {(
              [
                ["all", "全部"],
                ["untested", "仅未测"],
                ["tested", "仅已测"],
              ] as const
            ).map(([val, label]) => (
              <Button
                key={val}
                type="button"
                variant={statusFilter === val ? "secondary" : "ghost"}
                size="xs"
                onClick={() => setStatusFilter(val)}
              >
                {label}
              </Button>
            ))}
          </div>
        )}

        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {total > 0 ? (
            <span className="mr-1 whitespace-nowrap text-muted-foreground">
              范围内 <span className="font-semibold text-foreground tabular-nums">{inScope}</span>
              {coverageEnabled && (
                <>
                  {" "}
                  · 已测{" "}
                  <span className="font-semibold text-emerald-600 tabular-nums dark:text-emerald-400">{tested}</span>
                </>
              )}
            </span>
          ) : (
            <span className="mr-1 text-muted-foreground">{loading ? "读取中…" : "暂无范围内资产"}</span>
          )}

          <Button type="button" variant="ghost" size="xs" onClick={() => void fitFullView()} title="适配全图到当前视口">
            <Maximize2 className="size-3" />
            适应
          </Button>
          <Button type="button" variant="ghost" size="xs" onClick={() => void resetView()} title="重置视图缩放与聚焦">
            <RotateCcw className="size-3" />
            重置
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={fetchGraph}
            disabled={loading}
            title="刷新资产库数据"
          >
            <RefreshCw className={cn("size-3", loading && "animate-spin")} />
            刷新
          </Button>
        </div>
      </div>

      {/* 画布主体 */}
      <div className="relative min-h-0 w-full flex-1 overflow-hidden bg-slate-100/80 dark:bg-slate-950/30">
        {viewMode === "network" ? (
          <div className="pointer-events-none absolute inset-0 z-0 overflow-hidden">
            <div
              className="absolute top-0 left-0 grid grid-cols-3"
              style={{
                width: NETWORK_WORLD_W,
                height: worldHeight,
                transform: `translate(${viewport.position[0]}px, ${viewport.position[1]}px) scale(${viewport.zoom})`,
                transformOrigin: "0 0",
              }}
            >
              {(Object.keys(zoneMeta) as CoverageZone[]).map((zone, index) => (
                <div
                  key={zone}
                  className={cn(
                    "relative border-slate-200/80 border-r dark:border-slate-800/80",
                    zoneMeta[zone].className,
                    index === 2 && "border-r-0",
                  )}
                >
                  <div className="absolute top-3 left-4">
                    <div className="font-semibold text-foreground/80 text-sm">{zoneMeta[zone].label}</div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">{zoneMeta[zone].description}</div>
                  </div>
                  {zoneCounts[zone] === 0 && (
                    <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 rounded-md border border-slate-300/70 border-dashed px-3 py-2 text-center text-muted-foreground/70 text-xs dark:border-slate-700/70">
                      暂无该区域资产证据
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        ) : (
          <div className="pointer-events-none absolute top-3 left-4 z-0">
            <div className="font-semibold text-foreground/80 text-sm">资产关系图</div>
            <div className="mt-0.5 text-[11px] text-muted-foreground">{assetSubtitle}</div>
          </div>
        )}

        <div ref={containerRef} className="relative z-10 h-full w-full" />

        {/* 左下角默认收起的动态图例浮层 */}
        {renderNodes.length > 0 && (
          <div
            className={cn(
              "pointer-events-auto absolute bottom-3 left-3 z-20 rounded-lg border bg-card/95 text-xs shadow-sm backdrop-blur",
              legendExpanded
                ? "flex max-h-[calc(100%-1.5rem)] w-[min(480px,calc(100%-1.5rem))] flex-col gap-2.5 overflow-auto p-3"
                : "flex items-center gap-2 px-2.5 py-1.5",
            )}
          >
            <div className="flex w-full items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <Waypoints className="size-3.5 text-violet-600 dark:text-violet-400" />
                <span className="font-medium text-foreground">图例</span>
                <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                  {renderNodes.filter((r) => !r.fold).length} 资产
                </span>
              </div>
              <button
                type="button"
                onClick={() => setLegendExpanded((v) => !v)}
                className="inline-flex items-center gap-0.5 text-muted-foreground hover:text-foreground"
              >
                {legendExpanded ? (
                  <>
                    收起
                    <ChevronDown className="size-3.5" />
                  </>
                ) : (
                  <>
                    展开
                    <ChevronUp className="size-3.5" />
                  </>
                )}
              </button>
            </div>

            {legendExpanded && (
              <>
                <div className="flex flex-wrap gap-1.5">
                  {presentKinds.map((k) => {
                    const m = kindMeta[k];
                    const Icon = m.icon;
                    const cnt = kindCounts.get(k) ?? 0;
                    const active = highlightedKind === k;
                    return (
                      <button
                        key={k}
                        type="button"
                        onClick={() => {
                          setActivePath([]);
                          setHighlightedKind((cur) => (cur === k ? null : k));
                        }}
                        className={cn(
                          "inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-foreground transition-colors",
                          active
                            ? "border-primary bg-primary/10 font-medium"
                            : "border-transparent hover:border-border hover:bg-accent/60",
                        )}
                        title="点击在画布中高亮此资产类型"
                      >
                        <span className={cn("flex size-4 items-center justify-center rounded", m.iconBg)}>
                          <Icon className="size-2.5 text-white" />
                        </span>
                        <span>{m.label}</span>
                        <span className="font-mono text-[10px] text-muted-foreground tabular-nums">{cnt}</span>
                      </button>
                    );
                  })}
                </div>

                <div className="flex flex-wrap gap-x-3 gap-y-1.5 border-border/60 border-t pt-2 text-muted-foreground">
                  {coverageEnabled && (
                    <>
                      <span className="inline-flex items-center gap-1.5">
                        <span className="flex size-3 items-center justify-center rounded-full bg-emerald-600 font-bold text-[9px] text-white">
                          ✓
                        </span>
                        已测（绿环）
                      </span>
                      <span className="inline-flex items-center gap-1.5">
                        <span className="size-3 rounded-full bg-slate-500" />
                        范围内未测
                      </span>
                    </>
                  )}
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-3 rounded-full border border-neutral-400 border-dashed bg-neutral-300 dark:bg-slate-700" />
                    范围外连接点
                  </span>
                </div>

                {viewMode === "network" && (
                  <div className="flex flex-wrap gap-x-3 gap-y-1.5 border-border/60 border-t pt-2">
                    {(Object.keys(zoneMeta) as CoverageZone[]).map((zone) => (
                      <span key={zone} className="inline-flex items-center gap-1.5 text-muted-foreground">
                        <span
                          className={cn(
                            "size-2.5 rounded-full",
                            zone === "internet" && "bg-sky-400",
                            zone === "boundary" && "bg-amber-400",
                            zone === "internal" && "bg-emerald-400",
                          )}
                        />
                        {zoneMeta[zone].label}
                      </span>
                    ))}
                  </div>
                )}

                <p className="border-border/60 border-t pt-2 text-muted-foreground/80 leading-relaxed">
                  {viewMode === "network"
                    ? "图中仅展示有资产证据的节点；虚线表示归属/关联关系，不代表已验证网络连通性。"
                    : "资产关系图实时读取资产库；点击节点高亮完整归属与下游资产并查看详情，点击「⋯」可展开更多。"}
                </p>
              </>
            )}
          </div>
        )}

        {/* 右上角高亮路径或类型聚焦面板 */}
        {(activePath.length > 0 || highlightedKind) && data && (
          <div className="pointer-events-auto absolute top-3 right-3 z-20 w-[min(320px,calc(100%-2rem))] rounded-lg border bg-card/95 p-3 shadow-sm backdrop-blur">
            {activePath.length > 0 ? (
              <>
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="font-semibold text-foreground text-sm">关系路径</div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">
                      已高亮关联链路（共 {activePath.length} 个节点）
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => {
                      setActivePath([]);
                      void fitFullView();
                    }}
                  >
                    清除
                  </Button>
                </div>
                <div className="mt-2.5 flex max-h-56 flex-col gap-1 overflow-auto pr-0.5">
                  {activePath.slice(0, 12).map((key) => {
                    const node = data.nodes.find((item) => item.key === key);
                    if (!node) return null;
                    const zone = coverageZoneOf(node);
                    return (
                      <button
                        key={key}
                        type="button"
                        onClick={() => setSelectedAsset(node)}
                        className={cn(
                          "flex min-w-0 items-center gap-2 rounded-md px-2 py-1 text-left text-xs transition-colors hover:bg-accent",
                          selectedAsset?.key === key ? "bg-accent font-medium" : "bg-muted/60",
                        )}
                      >
                        <span
                          className={cn(
                            "size-2 shrink-0 rounded-full",
                            zone === "internet" && "bg-sky-500",
                            zone === "boundary" && "bg-amber-500",
                            zone === "internal" && "bg-emerald-500",
                          )}
                        />
                        <span className="shrink-0 text-muted-foreground">{kindMeta[node.kind].label}</span>
                        <span className="min-w-0 flex-1 truncate font-mono" title={node.label}>
                          {node.label}
                        </span>
                      </button>
                    );
                  })}
                  {activePath.length > 12 && (
                    <span className="px-1 text-[11px] text-muted-foreground">
                      还有 {activePath.length - 12} 个下游子节点已在画布高亮
                    </span>
                  )}
                </div>
              </>
            ) : (
              highlightedKind && (
                <div className="flex items-center justify-between gap-2 text-xs">
                  <span className="text-foreground">
                    已高亮 <strong>{kindMeta[highlightedKind].label}</strong>（{kindCounts.get(highlightedKind) ?? 0}）
                  </span>
                  <Button variant="ghost" size="xs" onClick={() => setHighlightedKind(null)}>
                    清除
                  </Button>
                </div>
              )
            )}
          </div>
        )}

        {renderNodes.length === 0 && (
          <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center p-4">
            <div className="pointer-events-auto flex flex-col items-center gap-2 rounded-lg border bg-card/95 px-4 py-3 text-center text-xs shadow-sm backdrop-blur">
              <span className="text-muted-foreground">{emptyText}</span>
              {total > 0 && statusFilter !== "all" && (
                <Button variant="outline" size="xs" onClick={() => setStatusFilter("all")}>
                  显示全部资产（{total}）
                </Button>
              )}
            </div>
          </div>
        )}
      </div>

      <AssetSheet node={selectedAsset} taskId={taskId} onOpenChange={(o) => !o && setSelectedAsset(null)} />
      <FoldSheet
        fold={selectedFold}
        onOpenChange={(o) => !o && setSelectedFoldId(null)}
        onShowMore={showMore}
        onPick={(n) => {
          setSelectedFoldId(null);
          setSelectedAsset(n);
        }}
      />
    </div>
  );
}

const CHAIN_STATUS_LABEL: Record<string, string> = {
  unused: "未动用",
  open: "待推进",
  attempted: "尝试中",
  advanced: "已推进",
};

function CoverageLedgerPanel({ taskId }: { taskId: string }) {
  const [open, setOpen] = React.useState(false);
  const [ledger, setLedger] = React.useState<ExplorationLedgerResponse | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");

  const load = React.useCallback(() => {
    setLoading(true);
    setError("");
    api
      .explorationLedger(taskId)
      .then(setLedger)
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  }, [taskId]);

  React.useEffect(() => {
    if (open && !ledger && !loading) load();
  }, [open, ledger, loading, load]);

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <Card>
        <CardHeader className="py-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-sm">探索覆盖台账与战果通路</CardTitle>
              <CardDescription className="text-xs">
                按「资产 × 风险面」统计覆盖状态与未测缺口，并追踪活跃据点/凭据的下游推进情况。
              </CardDescription>
            </div>
            <div className="flex items-center gap-2">
              {open && (
                <Button size="sm" variant="ghost" onClick={load} disabled={loading}>
                  <RefreshCw className={cn("size-3.5", loading && "animate-spin")} /> 刷新
                </Button>
              )}
              <CollapsibleTrigger asChild>
                <Button size="sm" variant="outline">
                  {open ? "收起台账" : "查看台账"}
                </Button>
              </CollapsibleTrigger>
            </div>
          </div>
        </CardHeader>
        <CollapsibleContent>
          <CardContent className="space-y-3 pt-0 text-xs">
            {error && <p className="text-destructive">读取台账失败：{error}</p>}
            {!ledger ? (
              <p className="text-muted-foreground">{loading ? "正在读取台账…" : "暂无台账数据"}</p>
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="outline">资产 {ledger.ledger.assets}</Badge>
                  <Badge variant="outline">风险格 {ledger.ledger.cells_total}</Badge>
                  <Badge variant="secondary">已测 {ledger.ledger.counts?.tested ?? 0}</Badge>
                  <Badge variant="secondary">在跑 {ledger.ledger.counts?.inflight ?? 0}</Badge>
                  <Badge variant="secondary">未测 {ledger.ledger.counts?.untested ?? 0}</Badge>
                  <Badge variant="secondary">失败 {ledger.ledger.counts?.failed ?? 0}</Badge>
                  <Badge variant="outline">豁免 {ledger.ledger.waived ?? 0}</Badge>
                  <Badge variant="outline">待补缺口 {ledger.ledger.gaps_total ?? 0}</Badge>
                </div>
                {ledger.ledger.gaps && ledger.ledger.gaps.length > 0 && (
                  <div className="space-y-1">
                    <div className="font-medium text-muted-foreground">
                      待测缺口（前 {ledger.ledger.gaps.length} 项）
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      {ledger.ledger.gaps.map((g, idx) => (
                        <Badge
                          key={`${g.asset_id}-${g.risk_area}-${idx}`}
                          variant="outline"
                          className="font-mono text-[11px]"
                        >
                          {g.label} · {g.risk_area} ({g.status})
                        </Badge>
                      ))}
                    </div>
                  </div>
                )}
                {ledger.chains && (
                  <div className="space-y-1.5 border-t pt-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium text-muted-foreground">战果通路台账</span>
                      {(["unused", "open", "attempted", "advanced"] as const).map((st) => (
                        <Badge key={st} variant="outline" className="text-[11px]">
                          {CHAIN_STATUS_LABEL[st]} {ledger.chains?.counts?.[st] ?? 0}
                        </Badge>
                      ))}
                    </div>
                    {ledger.chains.rows.length > 0 && (
                      <div className="grid gap-1.5 sm:grid-cols-2">
                        {ledger.chains.rows.slice(0, 12).map((row) => (
                          <div
                            key={row.loot_id}
                            className="flex items-center justify-between gap-2 rounded border px-2 py-1"
                          >
                            <span className="truncate font-mono" title={row.label}>
                              [{row.kind === "credential" ? "凭据" : "据点"}] {row.label}
                            </span>
                            <Badge variant="secondary" className="shrink-0 text-[10px]">
                              {CHAIN_STATUS_LABEL[row.status] ?? row.status} · 动用 {row.uses}
                            </Badge>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                )}
              </>
            )}
          </CardContent>
        </CollapsibleContent>
      </Card>
    </Collapsible>
  );
}

export function CoverageGraphTab({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  return (
    <div className="flex flex-col gap-3">
      <Card>
        <CardContent className="p-0">
          <GraphInner key={taskId} taskId={taskId} coverageEnabled={coverageEnabled} />
        </CardContent>
      </Card>
      <CoverageLedgerPanel key={taskId} taskId={taskId} />
    </div>
  );
}
