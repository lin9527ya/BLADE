"use client";

import * as React from "react";

import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api } from "@/lib/api";
import type { TaskNode } from "@/lib/types";
import { cn } from "@/lib/utils";

import {
  collapseDigestGraph,
  digestMemberIds,
  nodeSummary,
  prioTone,
  summaryFields,
  TypeMeta,
  typeMeta,
  viewKind,
} from "./graph-helpers";

// 节点详情弹窗：字段行、payload 的可展开/美化视图与「显示密钥」开关。

export function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 py-1.5 text-sm">
      <span className="text-muted-foreground w-14 shrink-0">{label}</span>
      <span className="text-foreground min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

// ── 美化数据：把节点 payload 的 JSON 渲染成人类可读的字段视图 ────────────────
// 原始 JSON 里 payload 还是被二次转义的字符串，长证据/摘要混在引号转义里很难读；
// 这里按字段语义直接给出内容：严重度/置信度用徽标、id 数组用 chips、长文本用段落、
// PoC/证据用等宽块、嵌套对象（evidence / closure / member_ids）递归展开。
const payloadFieldLabels: Record<string, string> = {
  summary: "摘要",
  detail: "详情",
  evidence: "证据",
  poc: "PoC",
  text: "目标文本",
  goal: "目标",
  description: "描述",
  name: "名称",
  vulnclass: "漏洞类别",
  severity: "严重度",
  confidence: "置信度",
  risk_area: "风险类",
  intent_id: "意图",
  asset_ids: "关联资产",
  by: "记录者",
  reason: "原因",
  note: "备注",
  closure: "闭合信息",
  outcome: "闭合结论",
  counterevidence: "反证",
  proof_gap: "待补证缺口",
  member_ids: "折叠成员",
  member_count: "成员数",
  facts: "事实",
  intents: "意图",
  source_intent: "来源意图",
  kind: "种类",
  value: "值",
  url: "URL",
  host: "主机",
  // 战果与凭据（P27/P36）
  access: "通路",
  privilege: "权限",
  username: "账号",
  secret: "凭据本体",
  targets: "可登目标",
  verified: "已验证",
  service: "服务",
  port: "端口",
  locator: "位置",
  cleanup: "清理方式",
  removed_evidence: "清理证据",
};

// 字段展示顺序：语义在前（摘要/目标/结论/证据…），其余按原 JSON 顺序落尾。
const payloadFieldOrder = [
  "summary",
  "text",
  "goal",
  "name",
  "vulnclass",
  "severity",
  "confidence",
  "risk_area",
  "detail",
  "evidence",
  "poc",
  // 战果/凭据/遗留物的结构化字段：先身份与位置，再清理与验证信息。
  "kind",
  "host",
  "locator",
  "url",
  "access",
  "privilege",
  "username",
  "secret",
  "targets",
  "value",
  "cleanup",
  "removed_evidence",
  "verified",
  "service",
  "port",
  "closure",
  "outcome",
  "counterevidence",
  "proof_gap",
  "intent_id",
  "source_intent",
  "asset_ids",
  "member_ids",
  "member_count",
  "by",
  "reason",
  "note",
];

const confidenceLabel: Record<string, string> = {
  high: "高（可复现触发链）",
  medium: "中（强异常，未完整对照）",
  low: "低（据指纹/版本推断）",
};

// 凭据/遗留物的 kind 是封闭词表，直接显示英文枚举对人不友好。
const lootKindLabel: Record<string, string> = {
  password: "口令",
  hash: "哈希",
  key: "私钥/密钥",
  token: "令牌",
  webshell: "Webshell",
  upload: "上传文件",
  account: "账号",
  scheduled_task: "计划任务",
  config_change: "配置变更",
  service: "服务",
  other: "其它",
};

export function PayloadValue({ k, v }: { k: string; v: unknown }) {
  if (v === null || v === undefined || v === "") {
    return <span className="text-muted-foreground text-xs">—</span>;
  }
  if (k === "kind" && typeof v === "string" && lootKindLabel[v]) {
    return <span className="text-xs">{lootKindLabel[v]}</span>;
  }
  if (k === "verified" && typeof v === "boolean") {
    return <span className="text-xs">{v ? "是（实测登入成功）" : "否"}</span>;
  }
  if (k === "severity" && typeof v === "string") {
    return <StatusBadge domain="severity" value={v} dot className="px-1.5 py-0 text-[10px]" />;
  }
  if (k === "confidence" && typeof v === "string") {
    return <span className="text-xs">{confidenceLabel[v] ?? v}</span>;
  }
  if (k === "risk_area" && typeof v === "string") {
    return <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">{v}</code>;
  }
  // 凭据的可登目标（P36）：逐条列 主机:端口（服务），并**分开标已验证/待验证**——
  // 列在一起会让人以为"列出来的都能登"。
  if (k === "targets" && Array.isArray(v)) {
    const rows = v.filter((x): x is Record<string, unknown> => !!x && typeof x === "object");
    if (rows.length === 0) return <span className="text-muted-foreground text-xs">—</span>;
    return (
      <span className="flex flex-col gap-1">
        {rows.map((t, i) => {
          const host = typeof t.host === "string" ? t.host : "";
          const port = typeof t.port === "number" && t.port > 0 ? `:${t.port}` : "";
          const svc = typeof t.service === "string" && t.service ? `（${t.service}）` : "";
          const verified = t.verified === true;
          return (
            <span key={`${host}${port}${i}`} className="flex flex-wrap items-center gap-2 text-xs">
              <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-[10px]">{`${host}${port}${svc}`}</code>
              <span className={verified ? "text-emerald-600 dark:text-emerald-400" : "text-muted-foreground"}>
                {verified ? "已验证可登" : "待验证"}
              </span>
              {typeof t.evidence === "string" && t.evidence !== "" && (
                <span className="text-muted-foreground truncate">{t.evidence}</span>
              )}
            </span>
          );
        })}
      </span>
    );
  }
  if (Array.isArray(v)) {
    if (v.length === 0) return <span className="text-muted-foreground text-xs">—</span>;
    if (v.every((x) => typeof x === "number")) {
      return (
        <span className="flex flex-wrap gap-1">
          {v.map((x) => (
            <code key={String(x)} className="bg-muted rounded px-1.5 py-0.5 font-mono text-[10px]">
              #{String(x)}
            </code>
          ))}
        </span>
      );
    }
    return (
      <span className="text-xs break-words whitespace-pre-wrap">
        {v.map((x) => (typeof x === "string" ? x : JSON.stringify(x))).join("、")}
      </span>
    );
  }
  if (typeof v === "object") {
    return (
      <div className="flex flex-col gap-1.5 border-l pl-2.5">
        {Object.entries(v as Record<string, unknown>).map(([kk, vv]) => (
          <div key={kk} className="flex items-start gap-2">
            <span className="text-muted-foreground w-16 shrink-0 pt-0.5 text-[11px]">
              {payloadFieldLabels[kk] ?? kk}
            </span>
            <span className="min-w-0 flex-1">
              <PayloadValue k={kk} v={vv} />
            </span>
          </div>
        ))}
      </div>
    );
  }
  const text = String(v);
  if (k === "poc" || k === "evidence" || text.includes("\n") || text.length > 120) {
    return (
      <pre className="bg-muted/40 max-h-72 overflow-auto rounded-md border p-2 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap">
        {text}
      </pre>
    );
  }
  return <span className="text-xs break-words whitespace-pre-wrap">{text}</span>;
}

// withRevealedSecret 把"这次点开看到的明文"注进待展示的节点（P36）。
//
// 服务端下发的 payload 里**本来就没有** secret（见 server/dto.go 的 redactCredentialSecret），
// 只有点过「显示明文」之后才由受控入口取回、并只对当前这条节点生效——所以换节点自动回到脱敏，
// 不需要额外的清理逻辑。明文入口那一次服务端会写留痕（谁、什么时候看了哪条凭据的明文）。
export function withRevealedSecret(n: TaskNode | null, revealed: string | null): TaskNode | null {
  if (!n || n.type !== "credential" || revealed === null) return n;
  let data: Record<string, unknown> | null = null;
  try {
    const parsed: unknown = JSON.parse(n.payload ?? "");
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      data = parsed as Record<string, unknown>;
    }
  } catch {
    return n; // 非 JSON payload：原样展示（没有可注入的结构）
  }
  if (!data) return n;
  delete data.secret_redacted;
  return { ...n, payload: JSON.stringify({ ...data, secret: revealed }) };
}

export function PayloadBeautified({ node }: { node: TaskNode }) {
  let data: Record<string, unknown> | null = null;
  try {
    const parsed: unknown = JSON.parse(node.payload ?? "");
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      data = parsed as Record<string, unknown>;
    }
  } catch {
    /* 非 JSON payload：原样展示 */
  }
  if (!data) {
    return (
      <p className="text-muted-foreground text-xs break-words whitespace-pre-wrap">
        {node.payload?.trim() || "（无 payload）"}
      </p>
    );
  }
  // secret_redacted 是服务端的脱敏提示（不是数据）：属性区已有「明文」一行带查看入口，
  // 在这里再渲染一个裸布尔值只会让人以为它是业务字段。
  const keys = Object.keys(data).filter((k) => k !== "secret_redacted");
  const ordered = [
    ...payloadFieldOrder.filter((k) => keys.includes(k)),
    ...keys.filter((k) => !payloadFieldOrder.includes(k)),
  ];
  return (
    <div className="flex flex-col gap-2.5">
      {ordered.map((k) => (
        <div key={k} className="flex flex-col gap-1">
          <span className="text-muted-foreground text-[11px] font-medium">{payloadFieldLabels[k] ?? k}</span>
          <PayloadValue k={k} v={data[k]} />
        </div>
      ))}
      {data.secret_redacted === true && (
        <p className="text-muted-foreground text-[11px]">凭据本体已脱敏；在节点详情里可点「显示明文」查看。</p>
      )}
    </div>
  );
}

// 节点详情。原先是右侧抽屉，改成居中弹窗——全站统一：从右侧滑出的界面只剩移动端侧栏
// 那一个（用户明确要求过），详情面板留着抽屉会显得像另一个应用。
export function NodeDetailDialog({
  node,
  nodesById,
  onSelect,
  onOpenChange,
  taskId,
}: {
  node: TaskNode | null;
  nodesById: Map<string, TaskNode>;
  onSelect: (n: TaskNode) => void;
  onOpenChange: (open: boolean) => void;
  taskId?: string;
}) {
  const [copied, setCopied] = React.useState(false);
  // 明文只对"当前这条、且这次明确点过"的凭据放行：换节点自动回到脱敏（与下面的
  // rawNodeId 同一个套路——用派生状态而不是 effect）。
  const [reveal, setReveal] = React.useState<{ id: string; secret: string } | null>(null);
  const [revealing, setRevealing] = React.useState(false);
  const revealedSecret = node && reveal && reveal.id === node.id ? reveal.secret : null;
  const displayNode = React.useMemo(() => withRevealedSecret(node, revealedSecret), [node, revealedSecret]);
  const doReveal = () => {
    if (!node || !taskId) return;
    setRevealing(true);
    api
      .revealCredential(node.id, taskId)
      .then((r) => {
        if (!r.secret) {
          toast.message("这条凭据没有登记本体，没有可显示的明文");
          return;
        }
        setReveal({ id: node.id, secret: r.secret });
        toast.success("已显示明文；本次查看已记入任务时间线");
      })
      .catch((e: unknown) => toast.error("读取明文失败：" + String((e as Error)?.message ?? e)))
      .finally(() => setRevealing(false));
  };
  // 数据展示默认「美化」，手动切到「原始」。用「记住哪个节点切到了原始」这个派生
  // 状态实现：换节点自动回到美化，也省掉一个 effect。
  const [rawNodeId, setRawNodeId] = React.useState<string | null>(null);
  const showRaw = !!node && rawNodeId === node.id;
  const meta = node ? (typeMeta[viewKind(node)] ?? typeMeta.intent) : null;
  const Icon = meta?.icon;
  const raw = displayNode ? JSON.stringify(displayNode, null, 2) : "";
  const memberIds = node ? digestMemberIds(node) : [];
  const members = memberIds.map((id) => nodesById.get(id)).filter((m): m is TaskNode => !!m);

  const copy = () => {
    void navigator.clipboard?.writeText(raw).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <Dialog open={node !== null} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl">
        {node && meta && Icon && (
          <>
            <DialogHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span
                  className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}
                >
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <DialogTitle className="leading-tight">{meta.label}</DialogTitle>
                  <span className="text-muted-foreground font-mono text-xs">{node.id}</span>
                </div>
                {node.inherited && node.source_task_id && (
                  <Badge variant="outline" className="ml-auto shrink-0">
                    来源任务 #{node.source_task_id} · 只读
                  </Badge>
                )}
              </div>
            </DialogHeader>

            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="text-muted-foreground mb-1 text-xs font-medium">属性</h4>
                  <DetailRow label="类型">{meta.label}</DetailRow>
                  <DetailRow label="状态">
                    {node.type === "goal" || node.type === "intent" ? (
                      <StatusBadge
                        domain={node.type === "goal" ? "goal" : "intent"}
                        value={node.state}
                        dot
                        className="px-1.5 py-0 text-[10px]"
                      />
                    ) : (
                      <span className="font-mono text-xs">{node.state}</span>
                    )}
                  </DetailRow>
                  <DetailRow label="优先级">
                    <span className="tabular-nums">{node.priority}</span>
                  </DetailRow>
                  <DetailRow label="来源">
                    <span className="font-mono text-xs">{node.origin}</span>
                  </DetailRow>
                  {node.inherited && node.source_task_id && (
                    <DetailRow label="来源任务">
                      <span className="font-mono text-xs">#{node.source_task_id}（继承，只读）</span>
                    </DetailRow>
                  )}
                  <DetailRow label="时间">{new Date(node.ts).toLocaleString("zh-CN")}</DetailRow>
                  {node.type === "credential" && (
                    <>
                      <DetailRow label="指纹">
                        <span className="font-mono text-xs">{node.fingerprint || "（无本体，无指纹）"}</span>
                      </DetailRow>
                      <DetailRow label="明文">
                        {revealedSecret !== null ? (
                          <button
                            type="button"
                            onClick={() => setReveal(null)}
                            className="text-muted-foreground hover:text-foreground text-xs underline underline-offset-2"
                          >
                            已显示 · 收起
                          </button>
                        ) : taskId ? (
                          <button
                            type="button"
                            onClick={doReveal}
                            disabled={revealing}
                            className="text-xs underline underline-offset-2"
                          >
                            {revealing ? "读取中…" : "显示明文（会留痕）"}
                          </button>
                        ) : (
                          <span className="text-muted-foreground text-xs">在任务页的链路图里可查看（会留痕）</span>
                        )}
                      </DetailRow>
                    </>
                  )}
                </section>

                {node.type === "digest" && (
                  <>
                    {nodeSummary(node) && (
                      <section className="border-t pt-3">
                        <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">压缩结论</h4>
                        <p className="text-foreground text-sm leading-relaxed break-words whitespace-pre-wrap">
                          {nodeSummary(node)}
                        </p>
                      </section>
                    )}
                    <section className="border-t pt-3">
                      <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">
                        压缩的节点（{memberIds.length}）
                      </h4>
                      <div className="flex flex-col gap-1.5">
                        {members.map((m) => {
                          const mMeta = typeMeta[viewKind(m)] ?? typeMeta.intent;
                          const MIcon = mMeta.icon;
                          return (
                            <button
                              key={m.id}
                              type="button"
                              onClick={() => onSelect(m)}
                              className="hover:bg-accent flex items-start gap-2 rounded-md border p-2 text-left transition-colors"
                            >
                              <span
                                className={cn(
                                  "mt-0.5 flex size-5 shrink-0 items-center justify-center rounded",
                                  mMeta.iconBg,
                                )}
                              >
                                <MIcon className="size-3 text-white" />
                              </span>
                              <span className="min-w-0 flex-1">
                                <span className="text-foreground line-clamp-2 text-xs leading-snug">
                                  {nodeSummary(m) || mMeta.label}
                                </span>
                                <span className="text-muted-foreground block font-mono text-[10px]">
                                  {mMeta.label} · #{m.id}
                                </span>
                              </span>
                            </button>
                          );
                        })}
                        {members.length < memberIds.length && (
                          <span className="text-muted-foreground text-[10px]">
                            另有 {memberIds.length - members.length} 个成员节点未随当前图加载
                          </span>
                        )}
                      </div>
                    </section>
                  </>
                )}

                <section className="border-t pt-3">
                  <div className="mb-2 flex items-center justify-between gap-2">
                    <div className="bg-muted inline-flex rounded-md p-0.5">
                      <button
                        type="button"
                        onClick={() => setRawNodeId(null)}
                        className={cn(
                          "rounded px-2.5 py-1 text-xs transition-colors",
                          !showRaw
                            ? "bg-background text-foreground shadow-sm"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        美化数据
                      </button>
                      <button
                        type="button"
                        onClick={() => setRawNodeId(node.id)}
                        className={cn(
                          "rounded px-2.5 py-1 text-xs transition-colors",
                          showRaw
                            ? "bg-background text-foreground shadow-sm"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        原始数据
                      </button>
                    </div>
                    {showRaw && (
                      <button
                        type="button"
                        onClick={copy}
                        className="text-muted-foreground hover:text-foreground text-xs transition-colors"
                      >
                        {copied ? "已复制" : "复制"}
                      </button>
                    )}
                  </div>
                  {showRaw ? (
                    <pre className="bg-muted/50 text-foreground max-h-[60vh] max-w-full overflow-auto rounded-md border p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
                      {raw}
                    </pre>
                  ) : (
                    <div className="max-h-[60vh] overflow-auto pr-1">
                      <PayloadBeautified node={displayNode ?? node} />
                    </div>
                  )}
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
