"use client";

import * as React from "react";

import {
  ChevronRightIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SearchIcon,
  ServerIcon,
  SparklesIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { SubBlock } from "@/components/page-sections";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { MCP_CATALOG } from "@/lib/mcp-catalog";
import type { Agent, MCPGuardStatus, MCPServer, MCPTool } from "@/lib/types";
import { cn } from "@/lib/utils";

import { MCPDialogs } from "./_components/mcp-dialogs";
import { bareToolName, emptyForm, type FormState, InfoRow, ToolParams } from "./_components/mcp-parts";

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
  const [catalogOpen, setCatalogOpen] = React.useState(false); // 推荐目录弹窗
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
        ss.forEach((s) => {
          void api
            .resourceVisibility("mcp", s.id)
            .then((ids) => setVisibility((v) => ({ ...v, [s.id]: ids })))
            .catch(() => {});
        });
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
  // 服务端回显的凭据是掩码（__masked__:…尾号）。原样填进编辑框，保存时服务端认得出
  // 「没改」并换回库中原值；这里不要自作主张清空——那会让"只改个名字"的保存把
  // token 抹掉。掩码带尾号提示，人也能认出是哪一个。
  function envToText(env: Record<string, string> | undefined): string {
    return Object.entries(env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n");
  }

  // addFromCatalog 把推荐项**预填**进「添加 MCP」弹窗：仍然由人确认后保存，
  // 不会自动创建任何东西（"由我决定是否默认安装"）。
  function addFromCatalog(item: (typeof MCP_CATALOG)[number]) {
    setEditing(null);
    setForm({
      name: item.name,
      transport: item.transport,
      command: item.command ?? "",
      args: item.args ?? "",
      url: item.url ?? "",
      env: item.env ?? "",
      insecure: false,
    });
    setTools([]);
    setCatalogOpen(false);
    setOpen(true);
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
          <Button size="sm" variant="outline" onClick={() => setCatalogOpen(true)}>
            <SparklesIcon className="size-3.5" /> 推荐
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

      <MCPDialogs
        catalogOpen={catalogOpen}
        setCatalogOpen={setCatalogOpen}
        open={open}
        setOpen={setOpen}
        editing={editing}
        saving={saving}
        pendingDelete={pendingDelete}
        setPendingDelete={setPendingDelete}
        addFromCatalog={addFromCatalog}
        renderForm={renderForm}
        saveForm={saveForm}
        removeServer={removeServer}
      />
    </div>
  );
}
