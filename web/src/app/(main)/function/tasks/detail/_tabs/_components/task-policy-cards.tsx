"use client";

import * as React from "react";

import { CustomRulesEditor } from "@/components/custom-rules-editor";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import {
  AssetInterceptKind,
  AssetInterceptRule,
  ConstraintCompilePreview,
  CustomRule,
  ExplorationPolicyDefault,
  PolicyRuleDraft,
  TaskPolicy,
} from "@/lib/types";
import {
  CheckIcon,
  PencilIcon,
  PlusIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

// 任务级资产拦截规则与探索策略两张卡片（各带自己的编辑表单与保存逻辑）。

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
          <span className="text-muted-foreground text-xs font-normal">
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
          {err && <span className="text-xs text-red-500">{err}</span>}
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
                  <span className="text-muted-foreground shrink-0 text-xs">{TASK_RULE_KIND_LABEL[r.kind]}</span>
                  <code className="bg-muted min-w-0 flex-1 truncate rounded px-1.5 py-0.5 text-xs">{r.pattern}</code>
                  {r.note && (
                    <span className="text-muted-foreground max-w-[120px] shrink-0 truncate text-xs">{r.note}</span>
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

// TaskExplorationPolicyCard 是 P20「探索策略」的任务级入口：一份结构化的边界声明，
// 同时在两层生效——
//   · 意图层：不符合策略的探索方向**不会进探索图**（硬拒，理由写清命中什么、往哪改）；
//   · 命令层：同一份策略编译成任务级拦截规则（只对本任务生效），换措辞也绕不过。
// 全局默认在系统设置里配；本任务可在创建时覆盖，也要在这里改（改动下一轮规划生效，
// 不回溯已存在的意图——要收掉存量用「清理未领取意图」）。
export function TaskExplorationPolicyCard({ taskId }: { taskId: string }) {
  const [pol, setPol] = React.useState<TaskPolicy | null>(null);
  const [vocab, setVocab] = React.useState<ExplorationPolicyDefault | null>(null);
  const [tech, setTech] = React.useState<string[]>([]);
  const [writes, setWrites] = React.useState<string[]>([]);
  const [custom, setCustom] = React.useState<CustomRule[]>([]);
  const [maxRound, setMaxRound] = React.useState("0");
  const [maxOpen, setMaxOpen] = React.useState("0");
  const [maxTotal, setMaxTotal] = React.useState("0");
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState("");
  const [preview, setPreview] = React.useState<{
    rules: PolicyRuleDraft[];
    preview: ConstraintCompilePreview[];
  } | null>(null);

  const load = React.useCallback(async () => {
    try {
      const p = await api.taskPolicy(taskId);
      setPol(p);
      // 编辑态从"当前生效"起步（继承时显示继承来的值；保存后即成显式覆盖）。
      setTech([...p.effective.techniques]);
      setWrites([...p.effective.forbidden_writes]);
      setCustom((p.effective.custom ?? []).map((r) => ({ ...r })));
      setMaxRound(String(p.effective.max_intents_per_round));
      setMaxOpen(String(p.effective.max_open_intents));
      setMaxTotal(String(p.effective.max_intents_total));
    } catch {
      // 忽略瞬时错误
    }
  }, [taskId]);

  React.useEffect(() => {
    void load();
    void api
      .explorationPolicyDefault()
      .then(setVocab)
      .catch(() => {});
  }, [load]);

  function toggle(list: string[], set: (v: string[]) => void, id: string) {
    set(list.includes(id) ? list.filter((x) => x !== id) : [...list, id]);
  }

  async function save() {
    setBusy(true);
    setErr("");
    try {
      // 五轴全部显式提交：这里的勾选就是本任务的最终策略（不再依赖"继承了什么"）。
      const next = await api.saveTaskPolicy(taskId, {
        techniques: tech,
        forbidden_writes: writes,
        custom: custom.filter((r) => r.pattern.trim() !== ""),
        max_intents_per_round: Number(maxRound) || 0,
        max_open_intents: Number(maxOpen) || 0,
        max_intents_total: Number(maxTotal) || 0,
      });
      setPol(next);
      setPreview(null);
      toast.success("探索策略已保存：意图层下一轮生效，命令门已重建（含自定义条目）");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function reset() {
    setBusy(true);
    setErr("");
    try {
      const next = await api.resetTaskPolicy(taskId);
      setPol(next);
      setTech([]);
      setWrites([]);
      setCustom([]);
      setMaxRound("0");
      setMaxOpen("0");
      setMaxTotal("0");
      setPreview(null);
      toast.success("已清空本任务策略（回到无策略）");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function doPreview() {
    setBusy(true);
    setErr("");
    try {
      setPreview(await api.previewTaskPolicy(taskId));
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function prune() {
    setBusy(true);
    setErr("");
    try {
      const r = await api.pruneTaskPolicy(taskId);
      toast.success(r.note ?? `已清理 ${r.stopped} 条被新策略禁止的未领取意图`);
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const chip = (on: boolean) =>
    `rounded-full border px-2 py-0.5 text-xs transition-colors ${
      on
        ? "border-red-500/40 bg-red-500/15 text-red-600 dark:text-red-400"
        : "border-border text-muted-foreground hover:bg-muted"
    }`;
  const rejects = pol?.recent_rejects ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          <ShieldAlertIcon className="size-4 text-red-500" /> 探索策略
          <span className="text-muted-foreground text-xs font-normal">
            （意图层：不符合策略的方向直接不进探索图并说明理由；命令层：编译成任务级拦截规则，只对本任务
            生效、换措辞也绕不过。词表覆盖不到时可手写自定义条目（关键词或正则，「仅命令」层只拦工具调用）。
            改动下一轮规划生效，不回溯已有意图。「禁止」是平台的边界，不会解除系统内置的破坏性拦截）
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {pol && (
          <div className="text-muted-foreground flex flex-wrap items-center gap-3 text-xs">
            <span
              className={`rounded px-1.5 py-0.5 ${
                pol.custom ? "bg-sky-500/15 text-sky-600 dark:text-sky-400" : "bg-muted text-muted-foreground"
              }`}
              title={
                pol.custom
                  ? "本任务自己配过策略（不再跟随全局默认）"
                  : `创建时抄的全局默认快照（第 ${pol.global_version} 版）——改全局默认不影响本任务`
              }
            >
              {pol.custom ? "已自定义" : "继承自全局默认"}
            </span>
            <span>
              意图额度：{pol.intents_used}
              {pol.intents_total > 0 ? ` / ${pol.intents_total}` : "（不限）"}
            </span>
            <span>命令门规则：{pol.rule_count} 条</span>
          </div>
        )}

        <div className="flex flex-col gap-1.5">
          <span className="text-sm font-medium">手段面（这些技术方向不派、也不执行）</span>
          <div className="flex flex-wrap gap-1.5">
            {(vocab?.techniques ?? []).map((t) => (
              <button
                key={t.id}
                type="button"
                title={t.hint}
                className={chip(tech.includes(t.id))}
                onClick={() => toggle(tech, setTech, t.id)}
              >
                {tech.includes(t.id) ? "禁止 · " : ""}
                {t.label}
              </button>
            ))}
          </div>
        </div>

        <div className="flex flex-col gap-1.5">
          <span className="text-sm font-medium">敏感写（SRC 常态是只读验证）</span>
          <div className="flex flex-wrap gap-1.5">
            {(vocab?.writes ?? []).map((w) => (
              <button
                key={w.id}
                type="button"
                title={w.hint}
                className={chip(writes.includes(w.id))}
                onClick={() => toggle(writes, setWrites, w.id)}
              >
                {writes.includes(w.id) ? "禁止 · " : ""}
                {w.label}
              </button>
            ))}
          </div>
        </div>

        <div className="flex flex-col gap-1.5">
          <span className="text-sm font-medium">自定义禁止条目（词表覆盖不到时手写）</span>
          <CustomRulesEditor rules={custom} onChange={setCustom} disabled={busy} limit={pol?.custom_limit ?? 16} />
        </div>

        <div className="flex flex-col gap-1.5">
          <span className="text-sm font-medium">节奏面（0 = 不限）</span>
          <div className="flex flex-wrap items-center gap-3 text-xs">
            <label className="flex items-center gap-1.5">
              每轮意图上限
              <Input
                className="h-7 w-16 text-sm"
                value={maxRound}
                onChange={(e) => setMaxRound(e.target.value.replace(/[^0-9]/g, ""))}
                disabled={busy}
              />
            </label>
            <label className="flex items-center gap-1.5">
              在途意图上限
              <Input
                className="h-7 w-16 text-sm"
                value={maxOpen}
                onChange={(e) => setMaxOpen(e.target.value.replace(/[^0-9]/g, ""))}
                disabled={busy}
              />
            </label>
            <label className="flex items-center gap-1.5">
              意图总上限
              <Input
                className="h-7 w-16 text-sm"
                value={maxTotal}
                onChange={(e) => setMaxTotal(e.target.value.replace(/[^0-9]/g, ""))}
                disabled={busy}
              />
            </label>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" disabled={busy} onClick={() => void save()}>
            <CheckIcon className="size-3.5" /> 保存
          </Button>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void doPreview()}>
            <ShieldCheckIcon className="size-3.5" /> 预览命中
          </Button>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void prune()}>
            <Trash2Icon className="size-3.5" /> 清理未领取意图
          </Button>
          {pol?.custom && (
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => void reset()}>
              <XIcon className="size-3.5" /> 清空（回无策略）
            </Button>
          )}
          {err && <span className="text-xs text-red-500">{err}</span>}
        </div>

        {preview && (
          <div className="flex flex-col gap-1.5 rounded-md border border-dashed bg-muted/30 p-2.5 text-xs">
            {preview.rules.length === 0 ? (
              <span className="text-muted-foreground">当前策略不产生命令门规则（未禁任何手段/写操作）。</span>
            ) : (
              <>
                <span className="font-medium">将生成本任务拦截规则 {preview.rules.length} 条：</span>
                {preview.rules.map((r) => {
                  const pv = preview.preview.find((x) => x.pattern === r.pattern);
                  return (
                    <div key={r.pattern} className="flex flex-col gap-0.5 rounded border bg-background px-2 py-1">
                      <span className="break-all font-mono">
                        {r.action} · {r.item_label} · {r.pattern}
                      </span>
                      {pv ? (
                        <span className="text-muted-foreground">
                          会命中本任务最近 {pv.total} 次工具调用中的 {pv.matched} 次
                          {pv.samples.length > 0 ? `（如：${pv.samples[0]}）` : ""}
                        </span>
                      ) : (
                        <span className="text-muted-foreground">本任务还没有可对照的历史调用</span>
                      )}
                    </div>
                  );
                })}
              </>
            )}
          </div>
        )}

        {rejects.length > 0 && (
          <div className="flex flex-col gap-1">
            <span className="text-sm font-medium">近期被策略拒绝的意图（未进探索图）</span>
            {rejects.slice(0, 5).map((r, i) => (
              <span key={`${r.item_id}-${i}`} className="text-muted-foreground text-xs">
                「{r.summary}」—— 命中 {r.item_label}
                {r.matched ? `（命中词「${r.matched}」）` : ""}
              </span>
            ))}
          </div>
        )}

        <p className="text-muted-foreground text-xs">
          提示：策略只做确定性判定（封闭词表 + 形状化正则），被拒时理由里会给出命中片段——若判断是策略太糙，
          在这里改词表/关掉该条即可；被拒的意图不落图，但会在时间线上留痕。
        </p>
      </CardContent>
    </Card>
  );
}
