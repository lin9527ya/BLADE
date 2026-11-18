"use client";

import * as React from "react";

import {
  CheckIcon,
  ClockIcon,
  CpuIcon,
  PencilIcon,
  PlusIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { AssetInterceptKind, AssetInterceptRule, Task } from "@/lib/types";

// 任务级资产拦截规则卡片（自带编辑表单与保存逻辑）。

const TASK_RULE_KIND_OPTIONS: { value: AssetInterceptKind; label: string; placeholder: string }[] = [
  { value: "exact_domain", label: "域名(全等)", placeholder: "example.gov.cn" },
  { value: "exact_ip", label: "IP(全等)", placeholder: "203.0.113.10" },
  { value: "exact_url", label: "URL(全等)", placeholder: "https://example.com/login" },
  { value: "fuzzy_domain", label: "域名(模糊)", placeholder: ".gov.cn" },
  { value: "fuzzy_ip", label: "IP(模糊)", placeholder: "203.0.113." },
  { value: "fuzzy_url", label: "URL(模糊)", placeholder: "/admin" },
  { value: "cidr", label: "CIDR 网段", placeholder: "192.168.0.0/16" },
];

const TASK_RULE_KIND_LABEL: Record<AssetInterceptKind, string> = Object.fromEntries(
  TASK_RULE_KIND_OPTIONS.map((o) => [o.value, o.label]),
) as Record<AssetInterceptKind, string>;

// TaskInterceptRulesCard 在任务详情总览里管理「任务级资产拦截 / 允许规则」：
// 列表 + 新增 + 行内编辑 + 删除 + 启用开关。规则仅本任务生效，不进全局表。
export function TaskInterceptRulesCard({ taskId }: { taskId: string }) {
  const [rules, setRules] = React.useState<AssetInterceptRule[]>([]);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState("");
  const [newAction, setNewAction] = React.useState<"block" | "allow">("block");
  const [newKind, setNewKind] = React.useState<AssetInterceptKind>("fuzzy_domain");
  const [newPattern, setNewPattern] = React.useState("");
  const [newNote, setNewNote] = React.useState("");
  const [editId, setEditId] = React.useState<number | null>(null);
  const [editAction, setEditAction] = React.useState<"block" | "allow">("block");
  const [editKind, setEditKind] = React.useState<AssetInterceptKind>("fuzzy_domain");
  const [editPattern, setEditPattern] = React.useState("");
  const [editNote, setEditNote] = React.useState("");

  const load = React.useCallback(async () => {
    try {
      setRules(await api.taskInterceptRules(taskId));
    } catch {
      // 忽略瞬时错误
    }
  }, [taskId]);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function add() {
    if (!newPattern.trim()) return;
    setBusy(true);
    setErr("");
    try {
      await api.createTaskInterceptRule(taskId, {
        action: newAction,
        kind: newKind,
        pattern: newPattern.trim(),
        note: newNote.trim(),
        enabled: true,
      });
      setNewPattern("");
      setNewNote("");
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function startEdit(r: AssetInterceptRule) {
    setEditId(r.id);
    setEditAction(r.action ?? "block");
    setEditKind(r.kind);
    setEditPattern(r.pattern);
    setEditNote(r.note);
    setErr("");
  }

  async function saveEdit(r: AssetInterceptRule) {
    if (!editPattern.trim()) return;
    setBusy(true);
    setErr("");
    try {
      await api.updateTaskInterceptRule(taskId, r.id, {
        action: editAction,
        kind: editKind,
        pattern: editPattern.trim(),
        note: editNote.trim(),
        enabled: r.enabled,
      });
      setEditId(null);
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function remove(r: AssetInterceptRule) {
    setRules((prev) => prev.filter((x) => x.id !== r.id));
    try {
      await api.deleteTaskInterceptRule(taskId, r.id);
    } catch {
      await load();
    }
  }

  async function toggle(r: AssetInterceptRule) {
    setRules((prev) => prev.map((x) => (x.id === r.id ? { ...x, enabled: !x.enabled } : x)));
    try {
      await api.toggleTaskInterceptRule(taskId, r.id, !r.enabled);
    } catch {
      await load();
    }
  }

  const placeholder = TASK_RULE_KIND_OPTIONS.find((o) => o.value === newKind)?.placeholder ?? "";

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldCheckIcon className="size-4 text-sky-500" /> 任务级资产拦截 / 允许
          <span className="font-normal text-muted-foreground text-xs">
            （仅本任务生效，不进全局；先拦截后允许，共 {rules.length} 条）
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {/* 新增表单 */}
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect size="sm" value={newAction} onChange={(e) => setNewAction(e.target.value as "block" | "allow")}>
            <NativeSelectOption value="block">拦截</NativeSelectOption>
            <NativeSelectOption value="allow">允许</NativeSelectOption>
          </NativeSelect>
          <NativeSelect size="sm" value={newKind} onChange={(e) => setNewKind(e.target.value as AssetInterceptKind)}>
            {TASK_RULE_KIND_OPTIONS.map((o) => (
              <NativeSelectOption key={o.value} value={o.value}>
                {o.label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          <Input
            className="h-7 min-w-56 flex-1 text-sm"
            placeholder={placeholder}
            value={newPattern}
            onChange={(e) => setNewPattern(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void add();
            }}
            disabled={busy}
          />
          <Input
            className="h-7 w-36 text-sm"
            placeholder="备注(可选)"
            value={newNote}
            onChange={(e) => setNewNote(e.target.value)}
            disabled={busy}
          />
          <Button size="sm" variant="outline" disabled={busy || !newPattern.trim()} onClick={() => void add()}>
            <PlusIcon className="size-3.5" /> 添加
          </Button>
          {err && <span className="text-red-500 text-xs">{err}</span>}
        </div>
        {/* 规则列表 */}
        {rules.length > 0 ? (
          <div className="flex flex-col gap-1.5">
            {rules.map((r) =>
              editId === r.id ? (
                <div key={r.id} className="flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
                  <NativeSelect
                    size="sm"
                    value={editAction}
                    onChange={(e) => setEditAction(e.target.value as "block" | "allow")}
                  >
                    <NativeSelectOption value="block">拦截</NativeSelectOption>
                    <NativeSelectOption value="allow">允许</NativeSelectOption>
                  </NativeSelect>
                  <NativeSelect
                    size="sm"
                    value={editKind}
                    onChange={(e) => setEditKind(e.target.value as AssetInterceptKind)}
                  >
                    {TASK_RULE_KIND_OPTIONS.map((o) => (
                      <NativeSelectOption key={o.value} value={o.value}>
                        {o.label}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                  <Input
                    className="h-7 min-w-56 flex-1 text-sm"
                    value={editPattern}
                    onChange={(e) => setEditPattern(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") void saveEdit(r);
                      if (e.key === "Escape") setEditId(null);
                    }}
                    disabled={busy}
                    autoFocus
                  />
                  <Input
                    className="h-7 w-36 text-sm"
                    placeholder="备注(可选)"
                    value={editNote}
                    onChange={(e) => setEditNote(e.target.value)}
                    disabled={busy}
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy || !editPattern.trim()}
                    onClick={() => void saveEdit(r)}
                  >
                    <CheckIcon className="size-3.5 text-emerald-500" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy}
                    onClick={() => setEditId(null)}
                  >
                    <XIcon className="size-3.5" />
                  </Button>
                </div>
              ) : (
                <div key={r.id} className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm">
                  <span
                    className={`shrink-0 rounded px-1.5 py-0.5 text-xs ${
                      r.action === "allow"
                        ? "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
                        : "bg-red-500/15 text-red-600 dark:text-red-400"
                    }`}
                  >
                    {r.action === "allow" ? "允许" : "拦截"}
                  </span>
                  <span className="shrink-0 text-muted-foreground text-xs">{TASK_RULE_KIND_LABEL[r.kind]}</span>
                  <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-0.5 text-xs">{r.pattern}</code>
                  {r.note && (
                    <span className="max-w-[120px] shrink-0 truncate text-muted-foreground text-xs">{r.note}</span>
                  )}
                  <Switch checked={r.enabled} onCheckedChange={() => void toggle(r)} />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy}
                    onClick={() => startEdit(r)}
                  >
                    <PencilIcon className="size-3.5" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    disabled={busy}
                    onClick={() => void remove(r)}
                  >
                    <Trash2Icon className="size-3.5 text-red-500" />
                  </Button>
                </div>
              ),
            )}
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">
            暂无任务级规则。「拦截」命中即禁止测试；「允许」为白名单——配置后本任务只允许命中允许规则的资产（未配置则不启用白名单）。
          </p>
        )}
      </CardContent>
    </Card>
  );
}

// TaskRuntimeCard 展示任务的并发与预算……这些值在创建任务时确定：
// worker 槽位首次启动即固化；任务超时/心跳影响 coordinator 与 planner 节律。
export function TaskRuntimeCard({ task }: { task?: Task | null }) {
  if (!task) return null;
  const workers = task.worker_count && task.worker_count > 0 ? String(task.worker_count) : "跟随全局";
  const timeout =
    task.timeout_seconds && task.timeout_seconds > 0 ? `${Math.round(task.timeout_seconds / 60)} 分钟` : "不限时";
  const heartbeat =
    task.plan_heartbeat_seconds && task.plan_heartbeat_seconds > 0
      ? `${Math.round(task.plan_heartbeat_seconds / 60)} 分钟`
      : "默认";
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <CpuIcon className="size-4 text-sky-500" /> 运行并发与预算
          <span className="font-normal text-muted-foreground text-xs">（创建时确定）</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-2 text-sm sm:grid-cols-3">
        <div className="rounded-md border px-2.5 py-1.5">
          <div className="text-muted-foreground text-xs">Worker 槽位</div>
          <div className="font-medium">{workers}</div>
        </div>
        <div className="rounded-md border px-2.5 py-1.5">
          <div className="flex items-center gap-1 text-muted-foreground text-xs">
            <ClockIcon className="size-3" /> 任务超时
          </div>
          <div className="font-medium">{timeout}</div>
        </div>
        <div className="rounded-md border px-2.5 py-1.5">
          <div className="text-muted-foreground text-xs">规划心跳</div>
          <div className="font-medium">{heartbeat}</div>
        </div>
        <p className="text-muted-foreground text-xs sm:col-span-3">
          并发槽位只在任务首次启动时固化；全局默认在「系统设置 →
          执行与并发」调整。需要不同的并发/预算请在新建任务时设置。
        </p>
      </CardContent>
    </Card>
  );
}

// TaskAssetScopeCard 在任务详情总览里展示并切换「资产范围策略」。
// target = 目标所属全资产（同根域、同公司跨根域、本任务发现/锚定资产）自动可测；
// exact = 仅明确登记的主机。切回 exact 只移除策略自动写入的范围行（source=policy）。
export function TaskAssetScopeCard({ taskId, policy }: { taskId: string; policy?: "exact" | "target" }) {
  const current = policy ?? "exact";
  const [value, setValue] = React.useState<"exact" | "target">(current);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => setValue(current), [current]);

  async function change(next: "exact" | "target") {
    if (next === value) return;
    if (
      next === "exact" &&
      !window.confirm("切回“仅精确目标主机”会移除策略自动扩出的同根/同公司范围行（手工/agent 登记的范围保留）。确定？")
    ) {
      return;
    }
    const prev = value;
    setValue(next);
    setBusy(true);
    try {
      await api.updateTask(taskId, { asset_scope_policy: next });
      toast.success(next === "target" ? "已切换为目标所属全部资产" : "已切换为仅精确目标主机");
    } catch (e) {
      setValue(prev);
      toast.error(`切换失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldCheckIcon className="size-4 text-emerald-500" /> 测试范围（资产策略）
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <NativeSelect
          size="sm"
          className="max-w-80"
          value={value}
          disabled={busy}
          onChange={(e) => void change(e.target.value as "exact" | "target")}
        >
          <NativeSelectOption value="target">目标所属全部资产（同根/同公司跨根/任务发现）</NativeSelectOption>
          <NativeSelectOption value="exact">仅精确目标主机</NativeSelectOption>
        </NativeSelect>
        <p className="text-muted-foreground text-xs">
          target：通过目标获取且属于目标的资产都可自动测试——同根域子域、同公司跨根域资产、本任务发现/锚定的资产；不会扩到未归因的第三方根域。
          切换立即生效，RoE 停机/时间窗/拦截规则/裁判仍照常生效。
        </p>
      </CardContent>
    </Card>
  );
}

// TaskExplorationModeCard 在任务详情总览里展示并切换探索模式。
// divergent 只影响覆盖目标/黑板视图/no_output 语义，不放宽 RoE/审批/证据底线。
export function TaskExplorationModeCard({ taskId, mode }: { taskId: string; mode?: "balanced" | "divergent" }) {
  const current = mode ?? "balanced";
  const [value, setValue] = React.useState<"balanced" | "divergent">(current);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => setValue(current), [current]);

  async function change(next: "balanced" | "divergent") {
    if (next === value) return;
    const prev = value;
    setValue(next);
    setBusy(true);
    try {
      await api.updateTask(taskId, { exploration_mode: next });
      toast.success(next === "divergent" ? "已切换到发散模式" : "已切换到均衡模式");
    } catch (e) {
      setValue(prev);
      toast.error(`切换失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldCheckIcon className="size-4 text-sky-500" /> 探索模式
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <NativeSelect
          size="sm"
          className="max-w-80"
          value={value}
          disabled={busy}
          onChange={(e) => void change(e.target.value as "balanced" | "divergent")}
        >
          <NativeSelectOption value="balanced">均衡（现有行为）</NativeSelectOption>
          <NativeSelectOption value="divergent">发散（覆盖仅参考 / 完整态势）</NativeSelectOption>
        </NativeSelect>
        <p className="text-muted-foreground text-xs">
          发散模式不会因“全面测试/全资产”自动追加 100% 覆盖目标，planner 使用完整黑板视图， no_output 可按 worker
          执行轨迹续派。RoE、时间窗、停机、审批/拦截和证据底线不变。
        </p>
      </CardContent>
    </Card>
  );
}

// TaskLootExecCard controls the task-level post-exploitation opt-in. Enabling it
// only adds the tools to this task's agent surface; RoE/approval/evidence gates
// still apply. The global agent_loot_exec remains a separate master/default.
export function TaskLootExecCard({ taskId, enabled }: { taskId: string; enabled?: boolean }) {
  const [value, setValue] = React.useState(Boolean(enabled));
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => setValue(Boolean(enabled)), [enabled]);

  async function change(next: boolean) {
    if (next === value) return;
    if (
      next &&
      !window.confirm(
        "允许本任务执行后渗透动作（loot_exec / foothold_exec / tunnel_open / 文件传输 / 提权枚举）？\n\n" +
          "这些工具会把已登记的凭据/据点变成对目标的真实动作。开启后仍受 RoE、时间窗、停机、拦截规则、审批和证据底线约束。",
      )
    ) {
      return;
    }
    const prev = value;
    setValue(next);
    setBusy(true);
    try {
      await api.updateTask(taskId, { loot_exec_enabled: next });
      toast.success(next ? "已允许本任务后渗透执行" : "已关闭本任务后渗透执行");
    } catch (e) {
      setValue(prev);
      toast.error(`切换失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldAlertIcon className="size-4 text-amber-500" /> 后渗透执行
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <label htmlFor="task-loot-exec" className="flex items-center gap-2 text-sm">
          <Switch id="task-loot-exec" checked={value} disabled={busy} onCheckedChange={(v) => void change(v)} />
          允许本任务使用后渗透执行工具
        </label>
        <p className="text-muted-foreground text-xs">
          仅对本任务生效；默认关闭。开启后可用 loot_exec、foothold_exec、tunnel_open、loot_upload/download、
          enum_privilege，但仍走 RoE、时间窗、停机、拦截/审批与证据底线。全局 agent_loot_exec 仍可作为总开关。
        </p>
      </CardContent>
    </Card>
  );
}

// TaskCredentialBudgetCard overrides the global credential-attempt budget for this
// task. -1/empty = inherit; 0 = unlimited (still recorded); >0 = attempts per 30 min.
export function TaskCredentialBudgetCard({ taskId, budget }: { taskId: string; budget?: number }) {
  const current = budget ?? -1;
  const [value, setValue] = React.useState(current >= 0 ? String(current) : "");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => setValue(current >= 0 ? String(current) : ""), [current]);

  async function save() {
    const n = value.trim() === "" ? -1 : Number(value);
    if (!Number.isInteger(n) || n < -1) {
      toast.error("凭据尝试预算必须是不小于 -1 的整数（留空=继承全局，0=不限）");
      return;
    }
    setBusy(true);
    try {
      await api.updateTask(taskId, { credential_attempt_budget: n });
      toast.success(n < 0 ? "已改为继承全局凭据预算" : n === 0 ? "本任务凭据尝试已不限次数（仍记账）" : `本任务凭据预算：${n} 次/30 分钟`);
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ClockIcon className="size-4 text-sky-500" /> 凭据尝试预算
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            id="task-credential-budget"
            inputMode="numeric"
            autoComplete="off"
            className="h-8 w-48"
            placeholder="留空=继承全局；0=不限"
            value={value}
            disabled={busy}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void save();
            }}
          />
          <Button size="sm" onClick={() => void save()} disabled={busy}>
            保存
          </Button>
        </div>
        <p className="text-muted-foreground text-xs">
          仅对本任务生效，按目标 + 账号在 30 分钟窗口内累计；留空继承系统设置（默认 3 次）。0 = 不限但仍记账。
          超预算后的 ask/deny 仍由全局凭据策略决定，不会绕过 RoE、审批或证据底线。
        </p>
      </CardContent>
    </Card>
  );
}
