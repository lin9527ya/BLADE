"use client";

import * as React from "react";

import { ChevronRightIcon, Loader2Icon, PaperclipIcon, PlusIcon, SmartphoneIcon, UploadIcon } from "lucide-react";
import { toast } from "sonner";

import { AssetInterceptRulesEditor } from "@/components/asset-intercept-rules-editor";
import { StatusBadge } from "@/components/status-badge";
import { TaskLLMProfileChain } from "@/components/task-llm-profile-chain";
import { TaskTemplateControls } from "@/components/task-template-controls";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxValue,
} from "@/components/ui/combobox";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { formatSampleBytes, parseCandidateDomainInput } from "@/lib/mobile-utils";
import type {
  AssetInterceptRuleInput,
  Company,
  LLMProfile,
  MobileDevice,
  MobilePackageMetadata,
  Task,
  TaskCategory,
} from "@/lib/types";

import { CategoryPicker, CompanyPicker } from "./task-categories";
import { appendUploads, MAX_SOURCE_TASKS } from "./task-helpers";

// CreateTaskSheet is the 新建任务 drawer. Its form state lives HERE, not in TasksPage: with
// 描述/目标 held by the page component every keystroke re-rendered the whole task table
// behind the drawer (plus its sticky column and 20 AlertDialog trees), which showed up as
// input lag. Now typing only re-renders the drawer.
function SourceTaskPicker({
  tasks,
  value,
  onValueChange,
  portalContainer,
}: {
  tasks: Task[];
  value: string[];
  onValueChange: (value: string[]) => void;
  portalContainer?: React.RefObject<HTMLElement | null>;
}) {
  const tasksByID = React.useMemo(() => new Map(tasks.map((task) => [task.id, task])), [tasks]);
  const taskIDs = React.useMemo(() => tasks.map((task) => task.id), [tasks]);
  const atLimit = value.length >= MAX_SOURCE_TASKS;

  const handleValueChange = (next: string[]) => {
    onValueChange(next.slice(0, MAX_SOURCE_TASKS));
  };

  return (
    <Combobox
      items={taskIDs}
      itemToStringValue={(taskID) => {
        const task = tasksByID.get(taskID);
        return task ? `${task.id} ${task.description} ${task.goal}` : taskID;
      }}
      multiple
      value={value}
      onValueChange={handleValueChange}
    >
      <ComboboxChips>
        <ComboboxValue>
          {value.map((taskID) => (
            <ComboboxChip key={taskID}>#{taskID}</ComboboxChip>
          ))}
        </ComboboxValue>
        <ComboboxChipsInput
          id="source-tasks"
          placeholder={atLimit ? `最多关联 ${MAX_SOURCE_TASKS} 个任务` : "搜索任务 ID、描述或目标"}
          disabled={atLimit}
        />
      </ComboboxChips>
      <ComboboxContent portalContainer={portalContainer}>
        <ComboboxEmpty>没有匹配的任务</ComboboxEmpty>
        <ComboboxList>
          {(taskID) => {
            const task = tasksByID.get(taskID);
            return (
              <ComboboxItem key={taskID} value={taskID} disabled={atLimit && !value.includes(taskID)}>
                <div className="flex min-w-0 flex-1 items-center gap-2">
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-medium text-sm">
                      #{taskID} · {task?.description ?? "未知任务"}
                    </p>
                    {task?.goal && <p className="truncate text-muted-foreground text-xs">{task.goal}</p>}
                  </div>
                  {task && <StatusBadge domain="task" value={task.status} />}
                </div>
              </ComboboxItem>
            );
          }}
        </ComboboxList>
      </ComboboxContent>
    </Combobox>
  );
}

// CategoryPicker 是新建任务里的单选分类选择器：可搜索已有分类；输入库里没有的名称后
// 回车（或点下拉里的「创建」）即时新建分类并选中，选中项以可移除的 tag 展示。分类是
// 全局资源，这里即时创建与「分类管理」里手动新建等价。只允许一个分类。

export function CreateTaskSheet({
  categories,
  onCreated,
  onCategoriesChanged,
  open: controlledOpen,
  onOpenChange,
  hideTrigger = false,
}: {
  categories: TaskCategory[];
  onCreated: () => void;
  onCategoriesChanged: () => void;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  hideTrigger?: boolean;
}) {
  const [internalOpen, setInternalOpen] = React.useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = React.useCallback(
    (next: boolean) => {
      if (controlledOpen === undefined) setInternalOpen(next);
      onOpenChange?.(next);
    },
    [controlledOpen, onOpenChange],
  );
  // 关联任务选择器需要**全量**任务，而列表页现在是服务端分页、只持有当页数据。
  // 打开表单时单独拉一次全量（不带 page/size），只用于这个选择器。
  const [tasks, setTasks] = React.useState<Task[]>([]);
  React.useEffect(() => {
    if (!open) return;
    let alive = true;
    api
      .tasks()
      .then((r) => {
        if (alive) setTasks(r.tasks);
      })
      .catch(() => {
        // 选择器不可用不影响新建任务本身。
      });
    return () => {
      alive = false;
    };
  }, [open]);
  const [name, setName] = React.useState("");
  const [categoryID, setCategoryID] = React.useState<number | undefined>(undefined);
  const [description, setDescription] = React.useState("");
  const [goal, setGoal] = React.useState("");
  const [selectedTemplateID, setSelectedTemplateID] = React.useState<number | null>(null);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [sourceTaskIDs, setSourceTaskIDs] = React.useState<string[]>([]);
  const [companyIDs, setCompanyIDs] = React.useState<number[]>([]);
  const [llmProfileIDs, setLLMProfileIDs] = React.useState<string[]>([]);
  const [creating, setCreating] = React.useState(false);
  const [timeoutMin, setTimeoutMin] = React.useState(""); // 任务级超时(分钟);空/0 = 不限时
  const [heartbeatMin, setHeartbeatMin] = React.useState("10"); // planner 心跳(分钟);默认10,下限10(与后端一致)
  const [workerCount, setWorkerCount] = React.useState(""); // 每任务并发覆盖;空/0 = 跟随全局
  const [remark, setRemark] = React.useState(""); // 任务备注
  const [seedFirstIntent, setSeedFirstIntent] = React.useState(false); // 创建时下发种子意图,worker 免等首轮 planner 直接开跑;默认关闭,走标准先规划再执行
  const [coverageEnabled, setCoverageEnabled] = React.useState(true); // 资产覆盖度功能;默认开。关闭=不计算/展示覆盖度、不累积范围、隐藏范围类工具(company 关联不受影响)
  const [assetScopePolicy, setAssetScopePolicy] = React.useState<"exact" | "target">("target"); // 资产范围策略;新任务默认 target=目标所属全资产自动可测
  const [lootExecEnabled, setLootExecEnabled] = React.useState(false); // 任务级后渗透执行 opt-in;默认关
  const [credentialBudget, setCredentialBudget] = React.useState(""); // 任务级凭据预算;-1/空=继承全局,0=不限
  // 场景预设：选中分类后带出默认值（仍可手动覆盖）。
  const [presetAppliedFor, setPresetAppliedFor] = React.useState<number | undefined>(undefined);
  const [interceptRules, setInterceptRules] = React.useState<AssetInterceptRuleInput[]>([]); // 任务级资产拦截规则(仅本任务生效,不进全局表)
  // 方式1 文件上传:建任务前把文件暂存到 drafts/<draftId>/uploads/,拿回绝对路径追加进描述。
  const [uploading, setUploading] = React.useState(false);
  const [uploadCount, setUploadCount] = React.useState(0);
  // 移动 APP 模式：安装包流式上传 (<=1024 MiB)、预解析、候选域名与设备绑定
  const [appMode, setAppMode] = React.useState<"standard" | "mobile">("standard");
  const [appSamplePaths, setAppSamplePaths] = React.useState<string[]>([]);
  const [appManifest, setAppManifest] = React.useState<MobilePackageMetadata | null>(null);
  const [appUploadPct, setAppUploadPct] = React.useState<number | null>(null);
  const [appExtraHostsInput, setAppExtraHostsInput] = React.useState("");
  const [mobileDevices, setMobileDevices] = React.useState<MobileDevice[]>([]);
  const [mobileDeviceID, setMobileDeviceID] = React.useState("");
  const [miniAppID, setMiniAppID] = React.useState("");
  const appInputRef = React.useRef<HTMLInputElement>(null);
  const draftIdRef = React.useRef<string>("");
  const fileInputRef = React.useRef<HTMLInputElement>(null);
  const sheetContentRef = React.useRef<HTMLDivElement>(null);

  // load LLM profiles once for the create-task profile picker.
  React.useEffect(() => {
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => setProfiles([]));
    api
      .companies()
      .then(setCompanies)
      .catch(() => setCompanies([]));
  }, []);

  React.useEffect(() => {
    if (!open || appMode !== "mobile") return;
    api
      .mobileDevices()
      .then((r) => setMobileDevices(r.devices ?? []))
      .catch(() => setMobileDevices([]));
  }, [open, appMode]);

  async function pickAppPackage(files: FileList | null) {
    const file = files?.[0];
    if (!file) return;
    if (!draftIdRef.current) {
      draftIdRef.current =
        globalThis.crypto?.randomUUID?.() ?? `d${Date.now().toString(36)}${Math.random().toString(36).slice(2, 10)}`;
    }
    setAppUploadPct(0);
    try {
      const res = await api.uploadAppPackage(
        "staging",
        draftIdRef.current,
        file,
        (pct) => setAppUploadPct(pct),
        miniAppID,
      );
      const path = res.primary?.sample_path;
      if (path) setAppSamplePaths((prev) => [...new Set([...prev, path])]);
      const man = res.samples?.[0]?.manifest;
      if (man) {
        setAppManifest(man);
        if (!name.trim() && (man.app_name || man.package_name)) {
          setName(`${man.app_name || man.package_name} 安全测试`);
        }
        if (!description.trim()) {
          setDescription(
            `移动应用样本：${man.package_name} (${man.platform} v${man.version_name || "?"})，路径 ${path}`,
          );
        }
        if (man.candidate_domains?.length) {
          setAppExtraHostsInput((prev) => (prev.trim() ? prev : (man.candidate_domains ?? []).slice(0, 6).join(", ")));
        }
      }
      toast.success(`已完成上传与预解析：${file.name}`);
    } catch (e) {
      toast.error(`安装包上传失败：${(e as Error).message}`);
    } finally {
      setAppUploadPct(null);
      if (appInputRef.current) appInputRef.current.value = "";
    }
  }

  // pickFiles uploads the chosen files into this draft's staging dir and appends their
  // absolute paths to the description; the task's agents open them by path via Read/Bash.
  async function pickFiles(files: FileList | null) {
    if (!files || files.length === 0) return;
    // crypto.randomUUID 仅在安全上下文可用(https/localhost);经 IP+http 访问时降级。
    if (!draftIdRef.current) {
      draftIdRef.current =
        globalThis.crypto?.randomUUID?.() ?? `d${Date.now().toString(36)}${Math.random().toString(36).slice(2, 10)}`;
    }
    setUploading(true);
    try {
      const r = await api.chatUpload("staging", draftIdRef.current, Array.from(files));
      setDescription((prev) => appendUploads(prev, r.attachments));
      setUploadCount((n) => n + r.attachments.length);
    } catch (e) {
      toast.error(`上传失败：${(e as Error).message}`);
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = ""; // allow re-picking the same file
    }
  }

  // 全局默认策略：建任务时展示"将继承什么"，勾了自定义就用它当编辑起点。
  // 选中分类时套用其场景预设；同一个分类只自动套用一次，之后用户手改不被覆盖。
  React.useEffect(() => {
    if (!open || categoryID == null || presetAppliedFor === categoryID) return;
    const category = categories.find((c) => c.id === categoryID);
    if (!category) return;
    setPresetAppliedFor(categoryID);
    setSeedFirstIntent(category.seed_first_intent);
    setCoverageEnabled(category.default_coverage_enabled);
  }, [open, categoryID, categories, presetAppliedFor]);

  async function createTask() {
    if (!description.trim() || !goal.trim()) {
      toast.error("请填写描述与目标");
      return;
    }
    if (sourceTaskIDs.length > MAX_SOURCE_TASKS) {
      toast.error(`最多关联 ${MAX_SOURCE_TASKS} 个来源任务`);
      return;
    }
    setCreating(true);
    try {
      const credBudget = credentialBudget.trim() === "" ? undefined : Number(credentialBudget);
      if (credBudget !== undefined && (!Number.isInteger(credBudget) || credBudget < -1)) {
        toast.error("凭据尝试预算必须是不小于 -1 的整数（留空=继承全局，0=不限）");
        setCreating(false);
        return;
      }
      const timeoutSec = Math.max(0, Math.floor(Number(timeoutMin) || 0)) * 60;
      const heartbeatSec = Math.max(10, Math.floor(Number(heartbeatMin) || 10)) * 60; // 下限 10min，与后端归一一致
      await api.createTask({
        name: name.trim(),
        categoryId: categoryID,
        description: description.trim(),
        goal: goal.trim(),
        llmProfileIds: llmProfileIDs.map(Number),
        sourceTaskIds: sourceTaskIDs,
        companyIds: companyIDs,
        timeoutSeconds: timeoutSec,
        seedFirstIntent,
        planHeartbeatSeconds: heartbeatSec,
        coverageEnabled,
        assetScopePolicy,
        lootExecEnabled,
        credentialAttemptBudget: credBudget,
        workerCount: Math.max(0, Math.min(50, Math.floor(Number(workerCount) || 0))),
        remark: remark.trim(),
        appMode,
        appSamplePaths: appMode === "mobile" ? appSamplePaths : [],
        appExtraHosts: appMode === "mobile" ? parseCandidateDomainInput(appExtraHostsInput) : [],
        mobileDeviceId: appMode === "mobile" ? mobileDeviceID : "",
        interceptRules: interceptRules
          .map((r) => ({ ...r, pattern: r.pattern.trim() }))
          .filter((r) => r.pattern !== ""),
      });
      toast.success("任务已创建");
      setName("");
      setCategoryID(undefined);
      setDescription("");
      setGoal("");
      setSelectedTemplateID(null);
      setSourceTaskIDs([]);
      setCompanyIDs([]);
      setLLMProfileIDs([]);
      setLootExecEnabled(false);
      setCredentialBudget("");
      setTimeoutMin("");
      setHeartbeatMin("10");
      setSeedFirstIntent(false);
      setCoverageEnabled(true);
      setAssetScopePolicy("target");
      setPresetAppliedFor(undefined);
      setInterceptRules([]);
      setUploadCount(0);
      setAppMode("standard");
      setAppSamplePaths([]);
      setAppManifest(null);
      setAppExtraHostsInput("");
      setMobileDeviceID("");
      setMiniAppID("");
      draftIdRef.current = "";
      setOpen(false);
      onCreated();
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setCreating(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {!hideTrigger && (
        <DialogTrigger asChild>
          <Button size="sm">
            <PlusIcon /> 新建任务
          </Button>
        </DialogTrigger>
      )}
      {/* 居中弹窗:内容为 flex 列——头/脚固定,中间字段区 flex-1 独立滚动,高度上限 88vh。 */}
      <DialogContent
        ref={sheetContentRef}
        className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-4xl"
      >
        <DialogHeader className="border-b p-6">
          <DialogTitle>新建任务</DialogTitle>
          <DialogDescription>填写测试对象与目标，高级参数可按需展开。</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto p-6">
          <div className="grid gap-5">
            {/* 目标形态切换：常规目标 vs 移动 APP */}
            <div className="flex items-center gap-2 rounded-md border bg-muted/30 p-1.5">
              <Button
                type="button"
                size="sm"
                variant={appMode === "standard" ? "default" : "ghost"}
                onClick={() => setAppMode("standard")}
              >
                常规目标 (Web / 内网)
              </Button>
              <Button
                type="button"
                size="sm"
                variant={appMode === "mobile" ? "default" : "ghost"}
                onClick={() => setAppMode("mobile")}
              >
                <SmartphoneIcon className="size-3.5" />
                移动 APP / 小程序（APK / IPA / WXAPKG）
              </Button>
            </div>
            {appMode === "mobile" && (
              <div className="grid gap-3 rounded-md border border-sky-500/30 bg-sky-500/5 p-4 text-xs">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <div className="font-medium text-sm">移动应用与微信小程序样本解析</div>
                    <div className="text-muted-foreground">
                      流式上传支持最大 1024 MiB（APK/AAB/XAPK/IPA/WXAPKG/ZIP），即时提取包信息、路由、云开发与后端域名
                    </div>
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Input
                      placeholder="可选 wxAppID"
                      aria-label="可选微信小程序 AppID"
                      value={miniAppID}
                      onChange={(e) => setMiniAppID(e.target.value)}
                      className="h-8 w-28 bg-background font-mono text-xs"
                    />
                    <input
                      ref={appInputRef}
                      type="file"
                      accept=".apk,.aab,.xapk,.apkm,.apks,.ipa,.wxapkg,.zip"
                      className="hidden"
                      onChange={(e) => void pickAppPackage(e.target.files)}
                    />
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={appUploadPct !== null}
                      onClick={() => appInputRef.current?.click()}
                    >
                      <UploadIcon className="size-3.5" />
                      {appUploadPct !== null ? `上传解析中 ${appUploadPct}%` : "上传应用 / 小程序包"}
                    </Button>
                  </div>
                </div>
                {appManifest && (
                  <div className="grid gap-1.5 rounded border bg-background p-2.5 font-mono">
                    <div>
                      包名 / AppID: <strong>{appManifest.package_name}</strong> · 平台: {appManifest.platform} · 版本:{" "}
                      {appManifest.version_name || "-"} · 大小: {formatSampleBytes(appManifest.size_bytes)}
                    </div>
                    {appManifest.platform === "miniapp" && appManifest.miniapp ? (
                      <div className="text-muted-foreground">
                        容器: {appManifest.miniapp.container_type || "plain"} · 路由数:{" "}
                        {appManifest.miniapp.routes?.length ?? 0} · CloudBase Env:{" "}
                        {appManifest.miniapp.cloud_envs?.join(", ") || "未检测到"} · 云函数:{" "}
                        {appManifest.miniapp.cloud_functions?.length ?? 0} 个
                      </div>
                    ) : (
                      <div className="text-muted-foreground">
                        框架: {appManifest.frameworks?.join(", ") || "原生"} · 加壳:{" "}
                        {appManifest.packers?.join(", ") || "未见明显加壳"} · Schemes:{" "}
                        {appManifest.url_schemes?.join(", ") || "无"}
                      </div>
                    )}
                  </div>
                )}
                <div className="grid gap-1.5">
                  <Label htmlFor="mobile-extra-hosts" className="text-xs">
                    关联后端 API 域名 / IP（逗号分隔，自动纳入任务授权范围）
                  </Label>
                  <Input
                    id="mobile-extra-hosts"
                    placeholder="如 api.acme-shop.cn, auth.acme-shop.cn"
                    value={appExtraHostsInput}
                    onChange={(e) => setAppExtraHostsInput(e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="mobile-device-select" className="text-xs">
                    绑定移动测试设备（可选，绑定后将动态激活 Frida / UI 探索 / 代理控制工具）
                  </Label>
                  <NativeSelect
                    id="mobile-device-select"
                    size="sm"
                    value={mobileDeviceID}
                    onChange={(e) => setMobileDeviceID(e.target.value)}
                  >
                    <NativeSelectOption value="">暂不绑定（仅做静态逆向 + 后端 API 评估）</NativeSelectOption>
                    {mobileDevices.map((d) => (
                      <NativeSelectOption key={d.serial} value={d.serial}>
                        {d.model || d.serial} ({d.serial}) · {d.state} {d.rooted ? "[Root]" : ""}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>
              </div>
            )}
            <TaskTemplateControls
              description={description}
              goal={goal}
              categoryID={categoryID}
              interceptRules={interceptRules}
              selectedTemplateID={selectedTemplateID}
              onSelectedTemplateIDChange={setSelectedTemplateID}
              onApply={(template) => {
                setDescription(template.description);
                setGoal(template.goal);
                setCategoryID(template.category_id ?? undefined);
                setInterceptRules(template.intercept_rules ?? []);
                setUploadCount(0);
              }}
              portalContainer={sheetContentRef}
            />
            <div className="grid gap-2">
              <Label htmlFor="name">名称（可选）</Label>
              <Input
                id="name"
                placeholder="给任务起个便于识别的名字，例如：Acme 官网渗透"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <Field>
              <FieldLabel htmlFor="task-category">任务分类</FieldLabel>
              <CategoryPicker
                categories={categories}
                value={categoryID}
                onValueChange={setCategoryID}
                onCategoryCreated={onCategoriesChanged}
                portalContainer={sheetContentRef}
              />
              <FieldDescription>
                可选，单个分类；用于任务列表筛选和归档，不影响 Agent 执行。分类配置了场景预设时会带出种子意图 / 覆盖度。
              </FieldDescription>
            </Field>
            <div className="grid gap-2">
              <Label htmlFor="description">描述</Label>
              <Textarea
                id="description"
                className="min-h-32"
                placeholder="测试对象与背景，例如：测试 example.com 这个站点"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
              {/* 上传文件(可多选):暂存到 drafts/,把绝对路径追加进上方描述,worker 据此 Read/Bash 打开。 */}
              <div className="flex flex-wrap items-center gap-2">
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={(e) => void pickFiles(e.target.files)}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={uploading}
                >
                  {uploading ? <Loader2Icon className="animate-spin" /> : <PaperclipIcon />}
                  上传文件
                </Button>
                <span className="text-muted-foreground text-xs">
                  {uploadCount > 0
                    ? `已上传 ${uploadCount} 个文件，绝对路径已追加到描述末尾（可编辑）`
                    : "可多选；上传后把文件的绝对路径追加到描述，供 worker 用 Read/Bash 打开"}
                </span>
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="goal">目标</Label>
              <Textarea
                id="goal"
                className="min-h-32"
                placeholder="要达成什么，例如：拿下后台管理权限、获取服务器权限"
                value={goal}
                onChange={(e) => setGoal(e.target.value)}
              />
            </div>
            <Field>
              <FieldLabel htmlFor="source-tasks">关联任务</FieldLabel>
              <SourceTaskPicker
                tasks={tasks}
                value={sourceTaskIDs}
                onValueChange={setSourceTaskIDs}
                portalContainer={sheetContentRef}
              />
              <FieldDescription>
                最多关联 {MAX_SOURCE_TASKS}{" "}
                个任务。实时只读继承所选任务的持久化黑板、资产范围及相关流量；新任务写入独立黑板。
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="task-companies">关联企业资产范围</FieldLabel>
              <CompanyPicker
                companies={companies}
                value={companyIDs}
                onValueChange={setCompanyIDs}
                portalContainer={sheetContentRef}
              />
              <FieldDescription>
                创建任务时会将所选企业当前已有资产加入“测试资产”，并将域名、IP、CIDR、ICP 和企业关键词提供给 Agent
                作为范围上下文；不会自动生成意图或强制改变执行目标。
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="task-intercept-rules">任务级资产拦截 / 允许规则（可选）</FieldLabel>
              <AssetInterceptRulesEditor value={interceptRules} onChange={setInterceptRules} />
              <FieldDescription>
                仅对本任务生效，不写入全局规则。判定顺序：先按「拦截」规则（含全局）匹配，命中即禁止测试；未命中且本任务配置了「允许」规则时，须命中某条允许规则才放行，否则同样不允许测试；未配置任何允许规则则不启用白名单。
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="llm-profiles">LLM 配置链</FieldLabel>
              <TaskLLMProfileChain
                profiles={profiles}
                value={llmProfileIDs}
                onValueChange={setLLMProfileIDs}
                inputId="llm-profiles"
                portalContainer={sheetContentRef}
              />
              <FieldDescription>按列表顺序故障转移；第一项为当前配置，仅在明确额度不足时切换下一项。</FieldDescription>
            </Field>

            {/* 高级参数默认折叠:超时/心跳/首个意图,展开才占空间,常用路径保持清爽。 */}
            <Collapsible>
              <CollapsibleTrigger className="group flex w-full items-center gap-2 border-t pt-4 font-medium text-sm">
                <ChevronRightIcon className="size-4 text-muted-foreground transition-transform group-data-[state=open]:rotate-90" />
                高级设置
                <span className="ml-auto font-normal text-muted-foreground text-xs">超时 · 心跳 · 首个意图</span>
              </CollapsibleTrigger>
              <CollapsibleContent className="grid gap-5 pt-5">
                <div className="grid gap-2">
                  <Label htmlFor="timeout-min">任务超时（分钟，可选）</Label>
                  <Input
                    id="timeout-min"
                    type="number"
                    min={0}
                    className="w-40"
                    placeholder="留空 = 不限时"
                    value={timeoutMin}
                    onChange={(e) => setTimeoutMin(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    到点后触发优雅收尾（各 agent 写回 + planner 终局判定），任务进入 timeout 终态。
                  </p>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="heartbeat-min">planner 心跳（分钟）</Label>
                  <Input
                    id="heartbeat-min"
                    type="number"
                    min={10}
                    className="w-40"
                    placeholder="默认 10"
                    value={heartbeatMin}
                    onChange={(e) => setHeartbeatMin(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    距上轮规划结束/任务开始满该时长且期间无触发，自动触发一轮规划（兜底卡死 + 唤醒去监督在跑的
                    worker）。下限 10 分钟。
                  </p>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="worker-count">并发 Worker 数（可选）</Label>
                  <Input
                    id="worker-count"
                    type="number"
                    min={0}
                    max={50}
                    className="w-40"
                    placeholder="留空 = 跟随全局"
                    value={workerCount}
                    onChange={(e) => setWorkerCount(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    本任务的最大并发 Worker 数（留空则跟随全局设置）；每个意图都会创建各自独立的 Worker
                    与会话，互不复用上下文。
                  </p>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="task-remark">备注（可选）</Label>
                  <Input
                    id="task-remark"
                    className="w-full"
                    maxLength={2000}
                    placeholder="给自己记的上下文，例如：客户只允许夜间作业"
                    value={remark}
                    onChange={(e) => setRemark(e.target.value)}
                  />
                </div>
                <div className="grid gap-2">
                  <label htmlFor="seed-first-intent" className="flex items-center gap-2 text-sm">
                    <Checkbox
                      id="seed-first-intent"
                      checked={seedFirstIntent}
                      onCheckedChange={(v) => setSeedFirstIntent(!!v)}
                    />
                    直接下发首个意图（描述+目标）
                  </label>
                  <p className="text-muted-foreground text-xs">
                    开启后创建即把「描述+目标」作为一条意图下发，worker 免等首轮规划直接开跑，跑完再由 planner
                    接手判定/补充。CTF 等常一个 work 直接解决的场景推荐开启；关闭则走标准的先规划再执行。
                  </p>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="asset-scope-policy" className="text-sm">
                    资产范围策略
                  </Label>
                  <NativeSelect
                    id="asset-scope-policy"
                    size="sm"
                    value={assetScopePolicy}
                    onChange={(e) => setAssetScopePolicy(e.target.value as "exact" | "target")}
                  >
                    <NativeSelectOption value="target">目标所属全部资产（推荐）</NativeSelectOption>
                    <NativeSelectOption value="exact">仅精确目标主机</NativeSelectOption>
                  </NativeSelect>
                  <p className="text-muted-foreground text-xs">
                    target：通过目标获取到的、且属于目标的所有资产都可自动测试——同根域子域、同公司跨根域资产、本任务发现/锚定的云或第三方资产；
                    不会扩到未归因的第三方根域。exact：只测明确登记的主机（旧版行为）。
                  </p>
                </div>
                <div className="grid gap-2">
                  <label htmlFor="loot-exec-enabled" className="flex items-center gap-2 text-sm">
                    <Checkbox
                      id="loot-exec-enabled"
                      checked={lootExecEnabled}
                      onCheckedChange={(v) => setLootExecEnabled(!!v)}
                    />
                    允许后渗透执行（任务级）
                  </label>
                  <p className="text-muted-foreground text-xs">
                    开启后本任务可使用 loot_exec / foothold_exec / tunnel_open / loot_upload / loot_download /
                    enum_privilege；仍受 RoE、时间窗、停机、拦截/审批和证据底线约束。默认关闭。
                  </p>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="credential-budget" className="text-sm">
                    凭据尝试预算（次 / 30 分钟）
                  </Label>
                  <Input
                    id="credential-budget"
                    inputMode="numeric"
                    autoComplete="off"
                    className="max-w-xs"
                    placeholder="留空=继承全局；0=不限"
                    value={credentialBudget}
                    onChange={(e) => setCredentialBudget(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    仅对本任务生效。留空继承系统设置（默认 3 次/30 分钟），0 = 不限但仍记账；超预算仍按全局策略
                    ask/deny。用于密码喷洒或凭据复用验证时放宽，注意账号锁定风险。
                  </p>
                </div>
                <div className="grid gap-2">
                  <label htmlFor="coverage-enabled" className="flex items-center gap-2 text-sm">
                    <Checkbox
                      id="coverage-enabled"
                      checked={coverageEnabled}
                      onCheckedChange={(v) => setCoverageEnabled(!!v)}
                    />
                    资产覆盖度功能
                  </label>
                  <p className="text-muted-foreground text-xs">
                    默认开启：计算并展示测试覆盖度、态势图显示测试进度、自动累积测试范围。关闭后不再计算/展示覆盖度，
                    态势图仅展示资产不显示进度，agent 也不再获得范围类工具。关闭不影响「关联企业资产范围」。
                  </p>
                </div>
              </CollapsibleContent>
            </Collapsible>
          </div>
        </div>

        <DialogFooter className="flex-row justify-end gap-2 border-t p-4">
          <DialogClose asChild>
            <Button variant="outline">取消</Button>
          </DialogClose>
          <Button onClick={createTask} disabled={creating || uploading}>
            {creating && <Spinner data-icon="inline-start" />}
            {creating ? "创建中" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
