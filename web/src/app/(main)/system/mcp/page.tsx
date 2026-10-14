"use client";

import * as React from "react";
import { toast } from "sonner";
import {
  ChevronRightIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SearchIcon,
  ServerIcon,
  Trash2Icon,
} from "lucide-react";

import { SubBlock } from "@/components/page-sections";
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
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import type { MCPServer, MCPGuardStatus, MCPTool, MCPToolSchema, MCPToolSchemaProp, Agent } from "@/lib/types";

type Transport = "stdio" | "http" | "sse";
type FormState = {
  name: string;
  transport: Transport;
  command: string;
  args: string;
  url: string;
  env: string;
  insecure: boolean;
};
const emptyForm: FormState = {
  name: "",
  transport: "stdio",
  command: "",
  args: "",
  url: "",
  env: "",
  insecure: false,
};

// 工具行展开的参数表：inputSchema 的 properties 平铺成行；array<object> 再把
// items.properties 作为子行带进来（与工具页参数表同一读法，这里只读）。
type SchemaRow = {
  name: string;
  type: string;
  required: boolean;
  desc: string;
  enumText: string;
  child: boolean;
};

function schemaRows(schema?: MCPToolSchema): SchemaRow[] {
  const rows: SchemaRow[] = [];
  const push = (name: string, p: MCPToolSchemaProp | undefined, required: boolean, child: boolean) => {
    rows.push({
      name,
      type: p?.type ?? "",
      required,
      desc: p?.description ?? "",
      enumText: p?.enum?.length ? ` · ${p.enum.map((v) => String(v)).join("|")}` : "",
      child,
    });
  };
  const required = new Set(schema?.required ?? []);
  for (const [name, p] of Object.entries(schema?.properties ?? {})) {
    push(name, p, required.has(name), false);
    const sub = p?.items?.properties;
    if (p?.type === "array" && sub && Object.keys(sub).length > 0) {
      const subReq = new Set(p.items?.required ?? []);
      for (const [sn, sp] of Object.entries(sub)) {
        push(sn, sp, subReq.has(sn), true);
      }
    }
  }
  return rows;
}

// ToolParams：工具行展开后的只读参数表（inputSchema 子集）。
function ToolParams({ schema }: { schema?: MCPToolSchema }) {
  const rows = schemaRows(schema);
  if (rows.length === 0) {
    return (
      <p className="text-muted-foreground mt-2 ml-5 text-xs">
        该工具没有声明参数（或这次发现没取到 schema——点「刷新」重新发现）。
      </p>
    );
  }
  return (
    <div className="bg-muted/30 mt-2 ml-5 overflow-x-auto rounded-md border p-2">
      <table className="w-full text-xs">
        <thead>
          <tr className="text-muted-foreground text-left">
            <th className="py-0.5 pr-3 font-normal">参数</th>
            <th className="py-0.5 pr-3 font-normal">类型</th>
            <th className="py-0.5 pr-3 font-normal">必填</th>
            <th className="py-0.5 font-normal">说明</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={(r.child ? "·" : "") + r.name} className="align-top">
              <td className="py-0.5 pr-3 font-mono">{r.child ? `↳ ${r.name}` : r.name}</td>
              <td className="text-muted-foreground py-0.5 pr-3 font-mono">
                {r.type || "?"}
                {r.enumText}
              </td>
              <td className="text-muted-foreground py-0.5 pr-3">{r.required ? "是" : ""}</td>
              <td className="text-muted-foreground py-0.5">{r.desc}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// bareToolName：缓存里的工具名是全名 mcp__<server>__<tool>（mcphttp.ToolName）。右栏里
// server 已是页面主体，展示时去掉前缀；开关发的仍是全名（后端按缓存名匹配）。
function bareToolName(server: string, full: string): string {
  const prefix = `mcp__${server}__`;
  return full.startsWith(prefix) ? full.slice(prefix.length) : full;
}

// InfoRow：明细行——左标签右值，值等宽可换行（URL/命令/错误都长）。
function InfoRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[8rem_1fr] sm:items-baseline sm:gap-3">
      <span className="text-muted-foreground text-xs">{label}</span>
      <div className="min-w-0 text-sm break-all">{children}</div>
    </div>
  );
}

// MCP 管理页照 CyberStrikeAI 的布局：满幅主从两栏——左栏服务器列表（只读行 + 选中态），
// 右栏是选中 server 的完整明细（连接信息 / 熔断与并发 / 工具列表 / 可见性），动作都在右栏头部。
// 添加与编辑仍走居中弹窗（7 个字段的表单，与工具页编辑器同一容器风格）。
export default function MCPPage() {
  const [servers, setServers] = React.useState<MCPServer[]>([]);
  // P24 守卫快照（按 server 名）：熔断中/并发占用/最后错误，供列表与明细展示、手动重置。
  const [guards, setGuards] = React.useState<Record<string, MCPGuardStatus>>({});
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [visibility, setVisibility] = React.useState<Record<number, string[]>>({});

  const [selectedId, setSelectedId] = React.useState<number | null>(null);
  const [toolQuery, setToolQuery] = React.useState("");

  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<MCPServer | null>(null); // null = add mode
  const [form, setForm] = React.useState<FormState>(emptyForm);
  const [saving, setSaving] = React.useState(false);
  const [pendingDelete, setPendingDelete] = React.useState<MCPServer | null>(null);

  const [tools, setTools] = React.useState<MCPTool[]>([]);
  const [toolsLoading, setToolsLoading] = React.useState(false);
  const [refreshing, setRefreshing] = React.useState(false);
  const [togglingTool, setTogglingTool] = React.useState<string | null>(null); // 正在翻转的工具名
  const [expandedTools, setExpandedTools] = React.useState<Set<string>>(new Set()); // 展开了参数表的工具名

  const load = React.useCallback(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => {});
    api
      .mcpServersWithGuards()
      .then(({ servers: ss, guards: gs }) => {
        setServers(ss);
        setGuards(gs);
        // 选中的 server 被删掉后（别处删除/刷新后消失）右栏退回空态。
        setSelectedId((prev) => (prev !== null && ss.some((s) => s.id === prev) ? prev : null));
        ss.forEach((s) =>
          api
            .resourceVisibility("mcp", s.id)
            .then((ids) => setVisibility((v) => ({ ...v, [s.id]: ids })))
            .catch(() => {}),
        );
      })
      .catch(() => {});
  }, []);
  React.useEffect(() => {
    load();
  }, [load]);

  const selected = servers.find((s) => s.id === selectedId) ?? null;
  const enabledCount = servers.filter((s) => s.enabled).length;
  const toolsDisabled = tools.filter((t) => !t.enabled).length;
  const breakerCount = servers.filter((s) => guards[s.name]?.open).length;

  const filteredTools = React.useMemo(() => {
    const q = toolQuery.trim().toLowerCase();
    if (!q) return tools;
    return tools.filter((t) => t.name.toLowerCase().includes(q) || (t.description ?? "").toLowerCase().includes(q));
  }, [tools, toolQuery]);

  function toggleToolRow(name: string) {
    setExpandedTools((prev) => {
      const next = new Set(prev);
      if (next.has(name)) {
        next.delete(name);
      } else {
        next.add(name);
      }
      return next;
    });
  }

  function selectServer(s: MCPServer) {
    setSelectedId(s.id);
    setToolQuery("");
    setExpandedTools(new Set());
    void loadTools(s.id);
  }

  function setF(patch: Partial<FormState>) {
    setForm((f) => ({ ...f, ...patch }));
  }

  function parseEnv(text: string): Record<string, string> {
    const out: Record<string, string> = {};
    for (const line of text.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const idx = trimmed.indexOf("=");
      if (idx > 0) {
        const key = trimmed.slice(0, idx).trim();
        if (key) out[key] = trimmed.slice(idx + 1).trim();
      }
    }
    return out;
  }
  function envToText(env: Record<string, string> | undefined): string {
    return Object.entries(env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n");
  }

  function openAdd() {
    setEditing(null);
    setForm(emptyForm);
    setOpen(true);
  }

  function openEdit(s: MCPServer) {
    setEditing(s);
    setForm({
      name: s.name,
      transport: s.transport,
      command: s.command ?? "",
      args: (s.args ?? []).join(" "),
      url: s.url ?? "",
      env: envToText(s.env),
      insecure: s.insecure ?? false,
    });
    setOpen(true);
  }

  async function loadTools(id: number) {
    setToolsLoading(true);
    try {
      setTools(await api.mcpTools(id));
    } catch {
      setTools([]);
    } finally {
      setToolsLoading(false);
    }
  }

  async function saveForm() {
    if (!form.name.trim()) {
      toast.error("请填写名称");
      return;
    }
    if (form.transport === "stdio" && !form.command.trim()) {
      toast.error("请填写命令");
      return;
    }
    if (form.transport !== "stdio" && !form.url.trim()) {
      toast.error("请填写远程 URL");
      return;
    }
    setSaving(true);
    try {
      const base =
        form.transport !== "stdio"
          ? {
              transport: form.transport,
              url: form.url.trim(),
              command: "",
              args: [] as string[],
              env: parseEnv(form.env), // 远程模式下 env 即请求头
              insecure: form.insecure,
            }
          : {
              transport: "stdio" as const,
              command: form.command.trim(),
              args: form.args.trim() ? form.args.trim().split(/\s+/) : [],
              env: parseEnv(form.env),
              insecure: false,
            };
      await api.saveMcpServer({
        ...(editing ? { id: editing.id } : {}),
        name: form.name.trim(),
        enabled: editing ? editing.enabled : true,
        ...base,
      });
      toast.success(editing ? "已保存" : "已添加 MCP 服务器");
      setOpen(false);
      if (!editing) setSelectedId(null); // 新加的服务器让用户自己在左栏点
      load();
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function refreshTools() {
    if (!selected) return;
    setRefreshing(true);
    try {
      const t = await api.refreshMcpServer(selected.id);
      setTools(t);
      toast.success(`发现 ${t.length} 个工具`);
      load();
    } catch (e) {
      toast.error("刷新失败：" + (e as Error).message);
    } finally {
      setRefreshing(false);
    }
  }

  async function removeServer(s: MCPServer) {
    try {
      await api.deleteMcpServer(s.id);
      toast.success(`已删除：${s.name}`);
      if (selectedId === s.id) {
        setSelectedId(null);
        setTools([]);
      }
      load();
    } catch (e) {
      toast.error("删除失败：" + (e as Error).message);
    }
  }

  async function toggleEnabled(s: MCPServer) {
    try {
      await api.saveMcpServer({ ...s, enabled: !s.enabled });
      load();
    } catch (e) {
      toast.error("操作失败：" + (e as Error).message);
    }
  }

  // P25：工具级开关（全局）——关掉的工具不再注入任何 Agent 的工具面。
  // 先本地翻转（开关手感即时），再用服务端结果对账；失败就重新拉一遍缓存。
  async function toggleTool(t: MCPTool, enabled: boolean) {
    if (!selected) return;
    setTogglingTool(t.name);
    setTools((ts) => ts.map((x) => (x.name === t.name ? { ...x, enabled } : x)));
    try {
      await api.toggleMcpTool(selected.id, t.name, enabled);
      toast.success(enabled ? `已启用「${t.name}」` : `已停用「${t.name}」：不再注入任何 Agent 的工具面`);
      load(); // 左栏的「N 个工具 · M 停用」跟着变
    } catch (e) {
      toast.error("操作失败：" + (e as Error).message);
      await loadTools(selected.id);
    } finally {
      setTogglingTool(null);
    }
  }

  // P24：手动重置该 server 的熔断（修好配置/服务恢复后用；失败会重新计数）。
  async function resetGuard(s: MCPServer) {
    try {
      const r = await api.resetMcpGuard(s.id);
      toast.success(r.note || `已重置「${s.name}」的熔断`);
      load();
    } catch (e) {
      toast.error("重置失败：" + (e as Error).message);
    }
  }

  async function toggleVisibility(serverId: number, agentId: string, agentName: string) {
    const on = (visibility[serverId] ?? []).includes(agentId);
    try {
      await api.toggleVisibility(agentId, "mcp", serverId, !on);
      toast.success(`${on ? "取消" : "授予"}「${agentName}」可见`);
      load();
    } catch (e) {
      toast.error("操作失败：" + (e as Error).message);
    }
  }

  function renderForm() {
    return (
      <div className="grid gap-4 py-4">
        <div className="grid gap-2">
          <Label>传输方式</Label>
          <div className="flex gap-2">
            <Button
              type="button"
              variant={form.transport === "stdio" ? "default" : "outline"}
              onClick={() => setF({ transport: "stdio" })}
            >
              stdio（本地）
            </Button>
            <Button
              type="button"
              variant={form.transport === "http" ? "default" : "outline"}
              onClick={() => setF({ transport: "http" })}
            >
              http（远程）
            </Button>
            <Button
              type="button"
              variant={form.transport === "sse" ? "default" : "outline"}
              onClick={() => setF({ transport: "sse" })}
            >
              sse（旧版）
            </Button>
          </div>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="m-name">名称</Label>
          <Input
            id="m-name"
            placeholder="filesystem"
            value={form.name}
            onChange={(e) => setF({ name: e.target.value })}
          />
        </div>
        {form.transport === "stdio" ? (
          <>
            <div className="grid gap-2">
              <Label htmlFor="m-cmd">命令</Label>
              <Input
                id="m-cmd"
                className="font-mono"
                placeholder="npx"
                value={form.command}
                onChange={(e) => setF({ command: e.target.value })}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="m-args">参数（空格分隔）</Label>
              <Input
                id="m-args"
                className="font-mono"
                placeholder="-y @modelcontextprotocol/server-filesystem /data"
                value={form.args}
                onChange={(e) => setF({ args: e.target.value })}
              />
            </div>
          </>
        ) : (
          <div className="grid gap-2">
            <Label htmlFor="m-url">远程 URL</Label>
            <Input
              id="m-url"
              className="font-mono"
              placeholder="https://mcp.example.com/mcp"
              value={form.url}
              onChange={(e) => setF({ url: e.target.value })}
            />
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={form.insecure} onCheckedChange={(v) => setF({ insecure: v === true })} />
              跳过 TLS 证书校验（自签证书）
            </label>
          </div>
        )}
        <div className="grid gap-2">
          <Label htmlFor="m-env">
            {form.transport !== "stdio"
              ? "请求头（每行 KEY=VALUE，如 Authorization=Bearer xxx）"
              : "环境变量（每行 KEY=VALUE）"}
          </Label>
          <Textarea
            id="m-env"
            className="font-mono"
            placeholder={form.transport !== "stdio" ? "Authorization=Bearer xxxx" : "API_KEY=xxxx\nFOO=bar"}
            value={form.env}
            onChange={(e) => setF({ env: e.target.value })}
          />
        </div>
      </div>
    );
  }

  function renderDetail(s: MCPServer) {
    const g = guards[s.name];
    const envKeys = Object.keys(s.env ?? {});
    const vis = visibility[s.id] ?? [];
    return (
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-8 p-4 lg:p-6">
        {/* 标题 + 动作（启用 / 编辑 / 删除 都在这里，列表行保持只读） */}
        <div className="flex flex-wrap items-start gap-3">
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <div className="flex flex-wrap items-center gap-2">
              <ServerIcon className="text-muted-foreground size-4 shrink-0" />
              <h2 className="truncate text-lg font-semibold tracking-tight">{s.name}</h2>
              <Badge variant="outline" className="uppercase">
                {s.transport}
              </Badge>
              {!s.enabled && (
                <Badge variant="outline" className="text-destructive">
                  已停用
                </Badge>
              )}
              {g?.open && (
                <Badge variant="destructive" title={g.last_error}>
                  熔断中 {g.open_remaining_sec ?? 0}s
                </Badge>
              )}
            </div>
            <p className="text-muted-foreground text-xs">
              外部 MCP 工具服务器 · 工具可被按 Agent 授权可见；停用后不再进入任何 Agent 的工具面。
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <label className="flex items-center gap-2 text-sm">
              <Switch checked={s.enabled} onCheckedChange={() => void toggleEnabled(s)} aria-label="启用" />
              启用
            </label>
            <Button size="sm" variant="outline" onClick={() => openEdit(s)}>
              <PencilIcon className="size-3.5" /> 编辑
            </Button>
            <Button size="sm" variant="outline" className="text-destructive" onClick={() => setPendingDelete(s)}>
              <Trash2Icon className="size-3.5" /> 删除
            </Button>
          </div>
        </div>

        <SubBlock title="连接信息">
          <div className="flex flex-col gap-3">
            <InfoRow label="传输方式">
              <span className="font-mono">{s.transport}</span>
              {s.transport === "stdio" ? "（本地起进程）" : "（远程 Streamable HTTP）"}
            </InfoRow>
            {s.transport === "stdio" ? (
              <>
                <InfoRow label="命令">
                  <span className="font-mono">{s.command || "（未设置）"}</span>
                </InfoRow>
                <InfoRow label="参数">
                  <span className="font-mono">{s.args && s.args.length > 0 ? s.args.join(" ") : "（无）"}</span>
                </InfoRow>
              </>
            ) : (
              <>
                <InfoRow label="远程 URL">
                  <span className="font-mono">{s.url || "（未设置）"}</span>
                </InfoRow>
                <InfoRow label="TLS 校验">{s.insecure ? "已跳过（自签证书）" : "正常校验"}</InfoRow>
              </>
            )}
            <InfoRow label={s.transport === "stdio" ? "环境变量" : "请求头"}>
              {envKeys.length === 0 ? (
                <span className="text-muted-foreground">（无）</span>
              ) : (
                <span className="font-mono text-xs">{envKeys.map((k) => `${k}=••••`).join("  ")}</span>
              )}
            </InfoRow>
          </div>
        </SubBlock>

        <SubBlock
          title="熔断与并发"
          actions={
            <Button
              size="sm"
              variant="outline"
              disabled={!g || (!g.open && g.consecutive_failures === 0)}
              onClick={() => void resetGuard(s)}
              title="清除熔断计数并立刻恢复真实调用（若服务仍不健康会重新计数）"
            >
              <RotateCcwIcon className="size-3.5" /> 重置熔断
            </Button>
          }
        >
          <div className="flex flex-col gap-3">
            <InfoRow label="并发上限">
              {!g || g.max_concurrent === 0 ? "不限" : `${g.max_concurrent}（进行中 ${g.inflight}）`}
            </InfoRow>
            <InfoRow label="连续失败">
              {(g?.consecutive_failures ?? 0) > 0 ? `${g?.consecutive_failures} 次` : "0 次"}
            </InfoRow>
            <InfoRow label="熔断状态">
              {g?.open ? (
                <span className="text-destructive">
                  已开闸，剩余 {g.open_remaining_sec ?? 0}s（期间调用立刻失败，不打服务端）
                </span>
              ) : (
                "正常"
              )}
            </InfoRow>
            {g?.last_error && (
              <InfoRow label="最后错误">
                <span className="font-mono text-xs">{g.last_error}</span>
              </InfoRow>
            )}
            <p className="text-muted-foreground text-xs leading-relaxed">
              守卫由平台按 server 名维护：只有传输层失败（连不上/超时/5xx）计数，工具返回的业务错误不算；
              连续失败达阈值即开闸，冷却后自动半开重试。上限与阈值在「系统配置 → 运行时」。
            </p>
          </div>
        </SubBlock>

        <SubBlock
          title={`工具列表${tools.length ? `（${tools.length}${toolsDisabled ? ` · ${toolsDisabled} 个已停用` : ""}）` : ""}`}
          actions={
            <>
              <div className="relative">
                <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
                <Input
                  className="h-8 w-44 pl-7 text-xs"
                  placeholder="筛选工具名/描述"
                  value={toolQuery}
                  onChange={(e) => setToolQuery(e.target.value)}
                />
              </div>
              <Button size="sm" variant="outline" disabled={refreshing} onClick={() => void refreshTools()}>
                <RefreshCwIcon className={cn("size-3.5", refreshing && "animate-spin")} /> 刷新
              </Button>
            </>
          }
        >
          {toolsLoading ? (
            <p className="text-muted-foreground text-sm">加载中…</p>
          ) : tools.length === 0 ? (
            <p className="text-muted-foreground text-sm">尚未发现工具，点「刷新」重新获取（需服务器可达且已启用）。</p>
          ) : filteredTools.length === 0 ? (
            <p className="text-muted-foreground text-sm">没有匹配「{toolQuery}」的工具。</p>
          ) : (
            <div className="flex flex-col divide-y">
              {filteredTools.map((t) => (
                <div key={t.name} className="py-2.5">
                  <div className="flex items-start gap-3">
                    <button
                      type="button"
                      onClick={() => toggleToolRow(t.name)}
                      aria-expanded={expandedTools.has(t.name)}
                      title="展开参数表"
                      className="flex min-w-0 flex-1 items-start gap-2 text-left"
                    >
                      <ChevronRightIcon
                        className={cn(
                          "text-muted-foreground mt-0.5 size-3.5 shrink-0 transition-transform",
                          expandedTools.has(t.name) && "rotate-90",
                        )}
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <code
                            className={cn("font-mono text-sm", !t.enabled && "text-muted-foreground")}
                            title={t.name}
                          >
                            {bareToolName(s.name, t.name)}
                          </code>
                          {!t.enabled && (
                            <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                              已停用
                            </Badge>
                          )}
                        </div>
                        {t.description && (
                          <p className="text-muted-foreground mt-0.5 text-xs leading-relaxed">{t.description}</p>
                        )}
                      </div>
                    </button>
                    <Switch
                      checked={t.enabled}
                      disabled={togglingTool === t.name}
                      onCheckedChange={(v) => void toggleTool(t, v)}
                      aria-label={`启用工具 ${t.name}`}
                      title="关掉后该工具不注入任何 Agent 的工具面（全局，与 Agent 无关）"
                    />
                  </div>
                  {expandedTools.has(t.name) && <ToolParams schema={t.schema} />}
                </div>
              ))}
            </div>
          )}
        </SubBlock>

        <SubBlock title="按 Agent 可见性">
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap gap-x-4 gap-y-2">
              {agents.length === 0 && <span className="text-muted-foreground text-sm">（无 Agent）</span>}
              {agents.map((a) => (
                <label key={a.key} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={vis.includes(a.id)}
                    onCheckedChange={() => void toggleVisibility(s.id, a.id, a.name)}
                  />
                  {a.name}
                </label>
              ))}
            </div>
            <p className="text-muted-foreground text-xs leading-relaxed">
              未勾选的 Agent 看不到这台服务器的任何工具；勾选即时生效（下一次构建工具面时）。
            </p>
          </div>
        </SubBlock>
      </div>
    );
  }

  return (
    <div data-content-padding="false" className="flex flex-1 flex-col overflow-hidden">
      <div className="flex items-center gap-3 border-b px-4 py-2.5 lg:px-6">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-sm leading-tight font-semibold">MCP</h1>
          <p className="text-muted-foreground text-xs">外部 MCP 工具服务器 · 按 Agent 授权可见</p>
        </div>
        <div className="ml-auto flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={load}>
            <RefreshCwIcon className="size-3.5" /> 刷新
          </Button>
          <Button size="sm" onClick={openAdd}>
            <PlusIcon className="size-3.5" /> 添加 MCP
          </Button>
        </div>
      </div>

      <div className="flex flex-1 flex-col overflow-hidden lg:flex-row">
        {/* ── 左栏：服务器列表（行只读，控件都在右栏，避免两处重复） ── */}
        <div className="flex max-h-[45vh] shrink-0 flex-col border-b lg:max-h-none lg:w-80 lg:border-r lg:border-b-0">
          <div className="flex flex-wrap items-center gap-1.5 border-b px-3 py-2">
            <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
              总数 {servers.length}
            </Badge>
            <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
              已启用 {enabledCount}
            </Badge>
            {servers.length - enabledCount > 0 && (
              <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                已停用 {servers.length - enabledCount}
              </Badge>
            )}
            {breakerCount > 0 && (
              <Badge variant="destructive" className="px-1.5 py-0 text-[10px]">
                熔断 {breakerCount}
              </Badge>
            )}
          </div>
          {/* Radix viewport 默认把内容包进 display:table 的 div（长名字会把行撑宽），改 block。 */}
          <ScrollArea className="flex-1 [&>[data-slot=scroll-area-viewport]>div]:!block">
            <div className="flex flex-col gap-1 p-2">
              {servers.length === 0 && (
                <p className="text-muted-foreground p-3 text-xs leading-relaxed">
                  还没有 MCP 服务器。点右上角「添加 MCP」接入 stdio（本地起进程）或 http（远程）。
                </p>
              )}
              {servers.map((s) => {
                const g = guards[s.name];
                const active = s.id === selectedId;
                return (
                  <button
                    key={s.id}
                    type="button"
                    onClick={() => selectServer(s)}
                    aria-current={active ? "true" : undefined}
                    className={cn(
                      "flex flex-col gap-1 rounded-md border px-2.5 py-2 text-left transition-colors",
                      active ? "border-primary/60 bg-muted/60" : "border-transparent hover:bg-muted/50",
                    )}
                  >
                    <div className="flex items-center gap-2">
                      <ServerIcon className="text-muted-foreground size-3.5 shrink-0" />
                      <span className="min-w-0 flex-1 truncate text-sm font-medium" title={s.name}>
                        {s.name}
                      </span>
                      <Badge variant="outline" className="px-1.5 py-0 text-[10px] uppercase">
                        {s.transport}
                      </Badge>
                    </div>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-muted-foreground text-xs">
                        {s.tools && s.tools.length > 0 ? `${s.tools.length} 个工具` : "尚未发现工具"}
                        {(s.tools_disabled ?? 0) > 0 ? ` · ${s.tools_disabled} 停用` : ""}
                      </span>
                      {!s.enabled && (
                        <Badge variant="outline" className="text-destructive px-1.5 py-0 text-[10px]">
                          已停用
                        </Badge>
                      )}
                      {g?.open && (
                        <Badge variant="destructive" className="px-1.5 py-0 text-[10px]">
                          熔断中 {g.open_remaining_sec ?? 0}s
                        </Badge>
                      )}
                      {!g?.open && (g?.consecutive_failures ?? 0) > 0 && (
                        <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                          失败 {g?.consecutive_failures}
                        </Badge>
                      )}
                    </div>
                  </button>
                );
              })}
            </div>
          </ScrollArea>
        </div>

        {/* ── 右栏：选中 server 的完整明细 ── */}
        <div className="flex-1 overflow-y-auto">
          {!selected ? (
            <div className="text-muted-foreground flex h-full flex-col items-center justify-center gap-2 p-8">
              <ServerIcon className="size-6" />
              <p className="text-sm">选择左侧的 MCP 服务器查看明细</p>
            </div>
          ) : (
            renderDetail(selected)
          )}
        </div>
      </div>

      {/* 添加 / 编辑：居中弹窗（7 个字段；工具列表已在右栏，弹窗只管配置） */}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-lg">
          <DialogHeader className="border-b px-4 py-3">
            <DialogTitle>{editing ? editing.name : "添加 MCP 服务器"}</DialogTitle>
            <DialogDescription>stdio（本地起进程）或 http（远程 Streamable HTTP）</DialogDescription>
          </DialogHeader>
          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">{renderForm()}</div>
          <div className="flex items-center gap-2 border-t px-4 py-3">
            <Button onClick={saveForm} disabled={saving}>
              {editing ? (
                "保存"
              ) : (
                <>
                  <PlusIcon /> 添加
                </>
              )}
            </Button>
            <Button variant="outline" onClick={() => setOpen(false)} disabled={saving}>
              取消
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!pendingDelete} onOpenChange={(o) => !o && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除「{pendingDelete?.name}」？</AlertDialogTitle>
            <AlertDialogDescription>
              删除后该服务器的工具从所有 Agent 的工具面消失，按 Agent 的可见性授权也一并清除。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const s = pendingDelete;
                setPendingDelete(null);
                if (s) void removeServer(s);
              }}
            >
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
