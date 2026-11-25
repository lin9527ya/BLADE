import type { CoverageGraphEdge, CoverageGraphNode } from "./types";

// 资产关系图/网络拓扑图的纯计算层（层级子树排版、分域网格排版、状态过滤、检索、双向关系溯源）：
// 与 G6 / React 解耦，便于在 Node 环境下直接做确定性单元测试。

export const COVERAGE_FOLD_LIMIT = 12;
export const COVERAGE_FOLD_STEP = 12;

export const ASSET_COL_W = 250;
export const ASSET_ROW_H = 88;
export const ASSET_COMP_GAP = 64;
export const ASSET_PADDING_X = 96;
export const ASSET_PADDING_Y = 72;

export const NETWORK_ZONE_W = 500;
export const NETWORK_WORLD_W = NETWORK_ZONE_W * 3;
export const NETWORK_TOP_OFFSET = 112;
export const NETWORK_ROW_H = 92;

export type CoverageKind = CoverageGraphNode["kind"];
export type CoverageZone = "internet" | "boundary" | "internal";
export type CoverageViewMode = "asset" | "network";
export type CoverageStatusFilter = "all" | "tested" | "untested";

export type CoverageFoldNode = {
  fold: true;
  key: string;
  groupId: string;
  parentKey: string;
  kind: CoverageKind;
  hidden: CoverageGraphNode[];
};

export type CoverageAssetRenderNode = {
  fold: false;
  key: string;
  kind: CoverageKind;
  node: CoverageGraphNode;
};

export type CoverageRenderNode = CoverageAssetRenderNode | CoverageFoldNode;

const KIND_ORDER: Record<CoverageKind, number> = {
  company: 0,
  root_domain: 1,
  subdomain: 2,
  ip: 3,
  service: 4,
  app: 5,
  endpoint: 6,
};

const ZONE_ORDER: CoverageZone[] = ["internet", "boundary", "internal"];

export function coverageZoneOf(node: CoverageGraphNode): CoverageZone {
  if (node.zone === "internet" || node.zone === "boundary" || node.zone === "internal") {
    return node.zone;
  }
  if (node.kind === "company" || node.kind === "root_domain" || node.kind === "subdomain" || node.kind === "ip") {
    return "internet";
  }
  if (node.kind === "service") return "boundary";
  return "internal";
}

function sortCoverageNodes(a: CoverageGraphNode, b: CoverageGraphNode): number {
  if (a.tested !== b.tested) return a.tested ? -1 : 1;
  if (a.in_scope !== b.in_scope) return a.in_scope ? -1 : 1;
  const ko = KIND_ORDER[a.kind] - KIND_ORDER[b.kind];
  if (ko !== 0) return ko;
  return (a.label || "").localeCompare(b.label || "");
}

function sortRenderNodes(a: CoverageRenderNode, b: CoverageRenderNode): number {
  if (a.fold !== b.fold) return a.fold ? 1 : -1;
  if (a.fold && b.fold) {
    const ko = KIND_ORDER[a.kind] - KIND_ORDER[b.kind];
    return ko !== 0 ? ko : a.key.localeCompare(b.key);
  }
  if (!a.fold && !b.fold) {
    return sortCoverageNodes(a.node, b.node);
  }
  return 0;
}

/**
 * 按测试覆盖状态过滤资产节点，并自动保留命中节点通往根公司的所有上游父节点以维持树结构完整。
 */
export function filterCoverageGraphByStatus(
  nodes: CoverageGraphNode[],
  edges: CoverageGraphEdge[],
  statusFilter: CoverageStatusFilter,
): { nodes: CoverageGraphNode[]; edges: CoverageGraphEdge[] } {
  if (statusFilter === "all") return { nodes, edges };

  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const parentsOf = new Map<string, string[]>();
  for (const e of edges) {
    if (!byKey.has(e.src) || !byKey.has(e.dst)) continue;
    const arr = parentsOf.get(e.src);
    if (arr) arr.push(e.dst);
    else parentsOf.set(e.src, [e.dst]);
  }

  const keep = new Set<string>();
  const queue: string[] = [];
  for (const n of nodes) {
    const matches = statusFilter === "tested" ? n.in_scope && n.tested : n.in_scope && !n.tested;
    if (matches) {
      keep.add(n.key);
      queue.push(n.key);
    }
  }

  for (let qi = 0; qi < queue.length; qi++) {
    const cur = queue[qi];
    for (const parent of parentsOf.get(cur) ?? []) {
      if (!keep.has(parent)) {
        keep.add(parent);
        queue.push(parent);
      }
    }
  }

  return {
    nodes: nodes.filter((n) => keep.has(n.key)),
    edges: edges.filter((e) => keep.has(e.src) && keep.has(e.dst)),
  };
}

/**
 * 自顶向下级联折叠每个父节点下超限的同类子节点，超出部分生成“还有 N 个…”折叠节点。
 */
export function computeVisibleCoverageNodes(
  nodes: CoverageGraphNode[],
  edges: CoverageGraphEdge[],
  expanded: Map<string, number>,
  showAllNetworkNodes = false,
): { renderNodes: CoverageRenderNode[]; renderEdges: CoverageGraphEdge[] } {
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const parentOf = new Map<string, string>();
  const childrenOf = new Map<string, string[]>();
  for (const e of edges) {
    if (!byKey.has(e.src) || !byKey.has(e.dst)) continue;
    if (!parentOf.has(e.src)) parentOf.set(e.src, e.dst);
    const arr = childrenOf.get(e.dst);
    if (arr) arr.push(e.src);
    else childrenOf.set(e.dst, [e.src]);
  }

  const renderNodes: CoverageRenderNode[] = [];
  const visible = new Set<string>();
  const foldNodes: CoverageFoldNode[] = [];

  const queue: string[] = [];
  for (const n of nodes) {
    if (!parentOf.has(n.key)) {
      queue.push(n.key);
      visible.add(n.key);
    }
  }

  for (let qi = 0; qi < queue.length; qi++) {
    const parent = queue[qi];
    const kids = childrenOf.get(parent) ?? [];
    if (kids.length === 0) continue;
    const groups = new Map<CoverageKind, CoverageGraphNode[]>();
    for (const ck of kids) {
      const cn = byKey.get(ck);
      if (!cn) continue;
      const g = groups.get(cn.kind);
      if (g) g.push(cn);
      else groups.set(cn.kind, [cn]);
    }
    for (const [kind, arr] of groups) {
      arr.sort(sortCoverageNodes);
      const groupId = `${parent}::${kind}`;
      const shown =
        showAllNetworkNodes && arr.length <= 80 ? arr.length : (expanded.get(groupId) ?? COVERAGE_FOLD_LIMIT);
      const visibleChildren = arr.slice(0, shown);
      const hidden = arr.slice(shown);
      for (const c of visibleChildren) {
        if (!visible.has(c.key)) {
          visible.add(c.key);
          queue.push(c.key);
        }
      }
      if (hidden.length > 0) {
        foldNodes.push({ fold: true, key: `fold:${groupId}`, groupId, parentKey: parent, kind, hidden });
      }
    }
  }

  for (const n of nodes) {
    if (visible.has(n.key)) renderNodes.push({ fold: false, key: n.key, kind: n.kind, node: n });
  }
  const renderEdges: CoverageGraphEdge[] = edges.filter((e) => visible.has(e.src) && visible.has(e.dst));
  for (const f of foldNodes) {
    renderNodes.push(f);
    renderEdges.push({ src: f.key, dst: f.parentKey });
  }
  return { renderNodes, renderEdges };
}

/**
 * 资产关系图树形分层排版：
 * 沿父→子边（e.dst -> e.src）计算最长深度列并用子树纵向带宽展开，
 * 消除 service 与 app 同柱重叠、fold 节点脱离父节点以及跨域蜘蛛网交叉。
 */
export function layoutCoverageAssetNodes(
  renderNodes: CoverageRenderNode[],
  renderEdges: CoverageGraphEdge[],
): { positions: Map<string, { x: number; y: number }>; height: number } {
  const positions = new Map<string, { x: number; y: number }>();
  if (renderNodes.length === 0) return { positions, height: 640 };

  const nodeMap = new Map(renderNodes.map((rn) => [rn.key, rn]));
  const rawChildren = new Map<string, string[]>();
  const rawParents = new Map<string, string[]>();

  for (const e of renderEdges) {
    if (!nodeMap.has(e.src) || !nodeMap.has(e.dst) || e.src === e.dst) continue;
    const childList = rawChildren.get(e.dst);
    if (childList) {
      if (!childList.includes(e.src)) childList.push(e.src);
    } else {
      rawChildren.set(e.dst, [e.src]);
    }
    const parentList = rawParents.get(e.src);
    if (parentList) {
      if (!parentList.includes(e.dst)) parentList.push(e.dst);
    } else {
      rawParents.set(e.src, [e.dst]);
    }
  }

  const roots = renderNodes.filter((rn) => (rawParents.get(rn.key)?.length ?? 0) === 0).sort(sortRenderNodes);

  // 为可能存在于环路或极少数无根连通片兜底挑选起点
  const assigned = new Set<string>();
  const treeChildren = new Map<string, string[]>();
  const primaryParent = new Map<string, string>();
  const orderedRoots: string[] = [];

  const attachTree = (rootKey: string) => {
    if (assigned.has(rootKey)) return;
    assigned.add(rootKey);
    orderedRoots.push(rootKey);
    const q = [rootKey];
    for (let qi = 0; qi < q.length; qi++) {
      const u = q[qi];
      const kids = (rawChildren.get(u) ?? [])
        .map((k) => nodeMap.get(k))
        .filter((rn): rn is CoverageRenderNode => Boolean(rn))
        .sort(sortRenderNodes);
      for (const child of kids) {
        if (assigned.has(child.key)) continue;
        assigned.add(child.key);
        primaryParent.set(child.key, u);
        const list = treeChildren.get(u);
        if (list) list.push(child.key);
        else treeChildren.set(u, [child.key]);
        q.push(child.key);
      }
    }
  };

  for (const r of roots) attachTree(r.key);
  const remaining = renderNodes.filter((rn) => !assigned.has(rn.key)).sort(sortRenderNodes);
  for (const r of remaining) attachTree(r.key);

  // 计算每棵树内节点的拓扑最大列深度（保证全部父→子边均指向右侧，不会逆回）
  const depth = new Map<string, number>(renderNodes.map((rn) => [rn.key, 0]));

  // 先基于生成树设定初始深度，再对满足“父节点不在自己子树内”的多父边做松弛以保持全左→右
  const setTreeDepth = (u: string, d: number) => {
    depth.set(u, d);
    for (const v of treeChildren.get(u) ?? []) {
      setTreeDepth(v, d + 1);
    }
  };
  for (const r of orderedRoots) setTreeDepth(r, 0);

  for (let pass = 0; pass < renderNodes.length && pass < 8; pass++) {
    let changed = false;
    for (const e of renderEdges) {
      const pu = depth.get(e.dst);
      const pv = depth.get(e.src);
      if (pu === undefined || pv === undefined) continue;
      if (pv <= pu) {
        // 避免简单 2 节点或短环无限增大深度
        if (primaryParent.get(e.dst) === e.src) continue;
        depth.set(e.src, pu + 1);
        changed = true;
      }
    }
    if (!changed) break;
  }

  // 子树纵向占高计算（每个叶子占 1 行，父节点居于首尾子节点中点）
  const spanOf = new Map<string, number>();
  const measureSpan = (u: string): number => {
    const kids = treeChildren.get(u) ?? [];
    if (kids.length === 0) {
      spanOf.set(u, 1);
      return 1;
    }
    let total = 0;
    for (const k of kids) total += measureSpan(k);
    const s = Math.max(1, total);
    spanOf.set(u, s);
    return s;
  };
  for (const r of orderedRoots) measureSpan(r);

  let cursorY = ASSET_PADDING_Y;
  const assignCoords = (u: string, bandTop: number): number => {
    const kids = treeChildren.get(u) ?? [];
    const col = depth.get(u) ?? 0;
    const x = ASSET_PADDING_X + col * ASSET_COL_W;
    if (kids.length === 0) {
      const y = bandTop + ASSET_ROW_H / 2;
      positions.set(u, { x, y });
      return y;
    }
    let childTop = bandTop;
    const childYs: number[] = [];
    for (const k of kids) {
      const kSpan = spanOf.get(k) ?? 1;
      childYs.push(assignCoords(k, childTop));
      childTop += kSpan * ASSET_ROW_H;
    }
    const y = (childYs[0] + childYs[childYs.length - 1]) / 2;
    positions.set(u, { x, y });
    return y;
  };

  for (const r of orderedRoots) {
    const treeSpan = spanOf.get(r) ?? 1;
    assignCoords(r, cursorY);
    cursorY += treeSpan * ASSET_ROW_H + ASSET_COMP_GAP;
  }

  return { positions, height: Math.max(680, cursorY - ASSET_COMP_GAP + ASSET_PADDING_Y) };
}

/**
 * 网络拓扑视图坐标计算：三个等宽分栏（互联网 / 边界 / 内网），留出顶部标题留白避免重叠。
 */
export function layoutCoverageNetworkNodes(renderNodes: CoverageRenderNode[]): {
  positions: Map<string, { x: number; y: number }>;
  height: number;
} {
  const buckets: Record<CoverageZone, CoverageRenderNode[]> = { internet: [], boundary: [], internal: [] };
  for (const rn of renderNodes) {
    buckets[rn.fold ? "boundary" : coverageZoneOf(rn.node)].push(rn);
  }

  const positions = new Map<string, { x: number; y: number }>();
  let maxRows = 1;

  ZONE_ORDER.forEach((zone, zoneIdx) => {
    const items = [...buckets[zone]].sort(sortRenderNodes);
    const zoneStart = zoneIdx * NETWORK_ZONE_W;
    if (items.length <= 6) {
      maxRows = Math.max(maxRows, items.length);
      items.forEach((rn, idx) => {
        positions.set(rn.key, {
          x: zoneStart + NETWORK_ZONE_W / 2,
          y: NETWORK_TOP_OFFSET + idx * NETWORK_ROW_H,
        });
      });
    } else {
      const cols = [zoneStart + 130, zoneStart + NETWORK_ZONE_W - 130];
      const rows = Math.ceil(items.length / 2);
      maxRows = Math.max(maxRows, rows);
      items.forEach((rn, idx) => {
        const col = idx % 2;
        const row = Math.floor(idx / 2);
        positions.set(rn.key, {
          x: cols[col],
          y: NETWORK_TOP_OFFSET + row * NETWORK_ROW_H,
        });
      });
    }
  });

  const height = Math.max(720, NETWORK_TOP_OFFSET + maxRows * NETWORK_ROW_H + 72);
  return { positions, height };
}

/**
 * 双向关系路径追踪：既向上溯源公司/域名归属，也向下追踪挂接的子资产/服务/端点。
 */
export function collectCoverageLineage(
  focusKey: string,
  edges: CoverageGraphEdge[],
): { activeKeys: string[]; upstreamCount: number; downstreamCount: number } {
  const parentsOf = new Map<string, string[]>();
  const childrenOf = new Map<string, string[]>();
  for (const e of edges) {
    const pArr = parentsOf.get(e.src);
    if (pArr) pArr.push(e.dst);
    else parentsOf.set(e.src, [e.dst]);

    const cArr = childrenOf.get(e.dst);
    if (cArr) cArr.push(e.src);
    else childrenOf.set(e.dst, [e.src]);
  }

  const bfs = (adj: Map<string, string[]>): string[] => {
    const seen = new Set<string>([focusKey]);
    const out: string[] = [];
    const q = [focusKey];
    for (let i = 0; i < q.length; i++) {
      for (const nxt of adj.get(q[i]) ?? []) {
        if (!seen.has(nxt)) {
          seen.add(nxt);
          out.push(nxt);
          q.push(nxt);
        }
      }
    }
    return out;
  };

  const upstream = bfs(parentsOf);
  const downstream = bfs(childrenOf);
  // 顺序：上游（根→父） -> 当前 -> 下游，保持路径面板从上往下读
  const activeKeys = [...upstream.reverse(), focusKey, ...downstream];
  return {
    activeKeys,
    upstreamCount: upstream.length,
    downstreamCount: downstream.length,
  };
}

/**
 * 资产节点关键词检索，返回当前可见节点中命中的 key 列表。
 */
export function findMatchingCoverageKeys(renderNodes: CoverageRenderNode[], query: string): string[] {
  const raw = query.trim().toLowerCase();
  if (!raw) return [];
  const exact: string[] = [];
  const rest: string[] = [];

  for (const rn of renderNodes) {
    if (rn.fold) continue;
    const n = rn.node;
    if (n.label.toLowerCase() === raw || (n.asset_id && String(n.asset_id) === raw)) {
      exact.push(rn.key);
      continue;
    }
    const hay = [
      n.label,
      n.domain,
      n.root_domain,
      n.ip,
      n.url,
      n.page_title,
      n.app_name,
      n.service_type,
      n.port ? String(n.port) : "",
      n.asset_id ? `#${n.asset_id}` : "",
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    if (hay.includes(raw)) rest.push(rn.key);
  }

  return [...exact, ...rest];
}
