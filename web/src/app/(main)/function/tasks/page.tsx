"use client";

import * as React from "react";

import Link from "next/link";

import { ArchiveIcon, PauseIcon, PlayIcon, SearchIcon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { CustomRulesEditor } from "@/components/custom-rules-editor";
import { ScheduleTasksDialog } from "@/components/schedule-tasks-dialog";
import { TablePagination } from "@/components/table-pagination";
/* removed: @/components/ui/badge */
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Item, ItemActions, ItemContent, ItemDescription, ItemGroup, ItemMedia, ItemTitle } from "@/components/ui/item";
import { Label } from "@/components/ui/label";
/* removed: @/components/ui/progress */
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { usePolling } from "@/hooks/use-polling";
/* removed: @/components/ui/tooltip */
import { api } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { type SortDirection, useStoredSortPreference } from "@/lib/sort-preference";
import {
  AssetInterceptRuleInput,
  ChatAttachment,
  Company,
  CustomRule,
  type DeleteTaskOptions,
  DeleteTaskResult,
  ExplorationPolicyDefault,
  LLMProfile,
  type Task,
  type TaskCategory,
  type TaskStatus,
} from "@/lib/types";
import { cn } from "@/lib/utils";

import { CreateTaskSheet } from "./_components/create-task-sheet";
import { ARCHIVABLE_STATUSES, ArchiveConfirmDialog, TaskArchivesPanel } from "./_components/task-archives";
import { CategoryManagementDialog } from "./_components/task-categories";
import { BulkDeleteTasksDialog, MoveTasksCategoryDialog } from "./_components/task-dialogs";
import {
  compareTasks,
  type DeleteCounts,
  deleteDetails,
  deleteSummary,
  fmtBytes,
  POLL_MS,
  STATUS_OPTIONS,
  TASK_FILTER_PREFERENCE_KEY,
  TASK_SORT_FIELDS,
  TASK_SORT_PREFERENCE_KEY,
  type TaskSortField,
  taskControlAction,
  taskIsPinned,
  UNCATEGORIZED_VALUE,
} from "./_components/task-helpers";
import { ConcurrencySettingsDialog, SortableTaskHead, TaskRow } from "./_components/task-row";

// fmtBytes renders a human file size for the upload manifest.

export default function TasksPage() {
  const [activeTab, setActiveTab] = React.useState("current");
  const [tasks, setTasks] = React.useState<Task[]>([]);
  // 任务级 token 预算（全局设置；0=关闭）：给列表的 Token 列做用量/预算对照，只取一次。
  const [tokenBudget, setTokenBudget] = React.useState(0);
  React.useEffect(() => {
    api
      .settings()
      .then((s) => setTokenBudget(s.task_token_budget ?? 0))
      .catch(() => undefined);
  }, []);
  const [categories, setCategories] = React.useState<TaskCategory[]>([]);
  const [categoriesLoaded, setCategoriesLoaded] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const [statusFilter, setStatusFilter] = React.useState<TaskStatus | "all">("all");
  const [categoryFilter, setCategoryFilter] = React.useState("all");
  const [filtersHydrated, setFiltersHydrated] = React.useState(false);
  const [sortPreference, setSortPreference] = useStoredSortPreference(
    TASK_SORT_PREFERENCE_KEY,
    TASK_SORT_FIELDS,
    "id",
    "desc",
  );
  const { field: sortField, direction: sortDirection } = sortPreference;
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [nowSec, setNowSec] = React.useState(() => Math.floor(Date.now() / 1000));
  const [batchControlling, setBatchControlling] = React.useState<"pause" | "resume" | null>(null);
  const [movingCategory, setMovingCategory] = React.useState(false);

  React.useEffect(() => {
    const raw = getLocalStorageValue(TASK_FILTER_PREFERENCE_KEY);
    if (raw) {
      try {
        const parsed = JSON.parse(raw) as { status?: unknown; category?: unknown };
        if (parsed.status === "all" || STATUS_OPTIONS.some((option) => option.value === parsed.status)) {
          setStatusFilter(parsed.status as TaskStatus | "all");
        }
        if (
          typeof parsed.category === "string" &&
          (parsed.category === "all" || parsed.category === UNCATEGORIZED_VALUE || /^\d+$/.test(parsed.category))
        ) {
          setCategoryFilter(parsed.category);
        }
      } catch {
        // Ignore malformed or legacy preferences and retain the defaults.
      }
    }
    setFiltersHydrated(true);
  }, []);

  React.useEffect(() => {
    if (!filtersHydrated) return;
    setLocalStorageValue(
      TASK_FILTER_PREFERENCE_KEY,
      JSON.stringify({ status: statusFilter, category: categoryFilter }),
    );
  }, [categoryFilter, filtersHydrated, statusFilter]);

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return tasks.filter((t) => {
      if (statusFilter !== "all" && t.status !== statusFilter) return false;
      if (categoryFilter === UNCATEGORIZED_VALUE && t.category_id != null) return false;
      if (
        categoryFilter !== "all" &&
        categoryFilter !== UNCATEGORIZED_VALUE &&
        String(t.category_id) !== categoryFilter
      )
        return false;
      if (!q) return true;
      return (
        (t.name ?? "").toLowerCase().includes(q) ||
        (t.category_name ?? "").toLowerCase().includes(q) ||
        t.description.toLowerCase().includes(q) ||
        t.goal.toLowerCase().includes(q) ||
        t.id.toLowerCase().includes(q)
      );
    });
  }, [tasks, query, statusFilter, categoryFilter]);

  const sortNowSec = sortField === "duration" ? nowSec : 0;
  const ordered = React.useMemo(() => {
    return [...filtered].sort((left, right) => compareTasks(left, right, sortField, sortDirection, sortNowSec));
  }, [filtered, sortDirection, sortField, sortNowSec]);

  const sortTasksBy = React.useCallback(
    (field: TaskSortField) => {
      setSortPreference((current) => {
        if (current.field !== field) return { field, direction: "desc" };
        return { field, direction: current.direction === "asc" ? "desc" : "asc" };
      });
    },
    [setSortPreference],
  );

  // reset to page 1 whenever filters or ordering change
  // biome-ignore lint/correctness/useExhaustiveDependencies: both filters intentionally reset pagination.
  React.useEffect(() => {
    setPage(1);
  }, [query, statusFilter, categoryFilter, sortField, sortDirection]);

  const paginated = React.useMemo(
    () => ordered.slice((page - 1) * pageSize, page * pageSize),
    [ordered, page, pageSize],
  );

  // 多选删除:选择跨翻页/筛选保留,只在任务真的消失(被删或后端不再返回)时收敛。
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(() => new Set());

  React.useEffect(() => {
    setSelectedIds((prev) => {
      if (prev.size === 0) return prev;
      const live = new Set(tasks.map((t) => t.id));
      const next = new Set([...prev].filter((id) => live.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [tasks]);

  const toggleSelected = React.useCallback((id: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  const pageIds = React.useMemo(() => paginated.map((t) => t.id), [paginated]);
  const pageSelectedCount = React.useMemo(
    () => pageIds.filter((id) => selectedIds.has(id)).length,
    [pageIds, selectedIds],
  );
  let headerChecked: boolean | "indeterminate" = false;
  if (pageIds.length > 0 && pageSelectedCount === pageIds.length) {
    headerChecked = true;
  } else if (pageSelectedCount > 0) {
    headerChecked = "indeterminate";
  }

  const toggleSelectedPage = React.useCallback(
    (checked: boolean) => {
      setSelectedIds((prev) => {
        const next = new Set(prev);
        for (const id of pageIds) {
          if (checked) next.add(id);
          else next.delete(id);
        }
        return next;
      });
    },
    [pageIds],
  );

  // lastRef holds the previous poll's serialized payload: the list is re-fetched every
  // POLL_MS but usually comes back unchanged, and setTasks on an identical payload would
  // re-render the whole page for nothing. Bail out when it matches.
  const lastRef = React.useRef<string>("");

  const load = React.useCallback(() => {
    api
      .tasks()
      .then((r) => {
        const next = r.tasks.map((t) => (t.id === r.active ? { ...t, active: true } : t));
        const sig = JSON.stringify(next);
        if (sig === lastRef.current) return;
        lastRef.current = sig;
        setTasks(next);
      })
      .catch(() => {
        // Polling is best-effort; the next interval retries automatically.
      });
  }, []);

  const loadCategories = React.useCallback(() => {
    api
      .taskCategories()
      .then((next) => {
        setCategories(next);
        setCategoriesLoaded(true);
      })
      .catch(() => {
        // Category management remains retryable without blocking the task list.
      });
  }, []);

  usePolling(
    () => {
      load();
      loadCategories();
    },
    POLL_MS,
    [load, loadCategories],
  );

  const refreshCategoriesAndTasks = React.useCallback(() => {
    lastRef.current = "";
    loadCategories();
    load();
  }, [load, loadCategories]);

  const applyTaskCategoryMove = React.useCallback(
    (taskID: string, category: TaskCategory | null) => {
      setTasks((current) => {
        const next = current.map((task) =>
          task.id === taskID ? { ...task, category_id: category?.id, category_name: category?.name } : task,
        );
        lastRef.current = JSON.stringify(next);
        return next;
      });
      loadCategories();
    },
    [loadCategories],
  );

  React.useEffect(() => {
    if (!categoriesLoaded) return;
    if (categoryFilter === "all" || categoryFilter === UNCATEGORIZED_VALUE) return;
    if (!categories.some((category) => String(category.id) === categoryFilter)) setCategoryFilter("all");
  }, [categories, categoriesLoaded, categoryFilter]);

  // 只在确有 running 任务时才每秒 tick——其余情况「运行时长」是静态值,空转的 tick 会白白
  // 重渲染整张表。
  const hasRunning = React.useMemo(() => tasks.some((t) => t.status === "running"), [tasks]);

  // tick every second so running tasks' 运行时长 counts up live.
  usePolling(
    () => {
      if (!hasRunning) return;
      setNowSec(Math.floor(Date.now() / 1000));
    },
    hasRunning ? 1000 : 0,
    [hasRunning],
  );

  const deleteTask = React.useCallback(
    async (id: string, options: DeleteTaskOptions) => {
      try {
        const result = await api.deleteTask(id, options);
        if (result.cleanup_warning) {
          toast.warning(`${deleteSummary(result)}；部分外部数据清理未完成：${result.cleanup_warning}`);
        } else {
          toast.success(deleteSummary(result));
        }
        load();
      } catch (e) {
        toast.error("删除失败：" + (e as Error).message);
        throw e;
      }
    },
    [load],
  );

  // controlTask 是行内暂停/继续:批量走 controlTasksBatch,单行走单任务接口,省去
  // 「先勾选再点批量」。清空 lastRef 让下一次轮询即使负载相同也照单接收,否则状态
  // 回写会被去重挡掉、按钮看起来没反应。
  const controlTask = React.useCallback(
    async (id: string, action: "pause" | "resume") => {
      try {
        const result = await api.controlTask(id, action);
        toast.success(
          action === "pause" ? `任务 #${id} 已暂停` : `任务 #${id} 已继续${result.queued ? "，已进入队列" : ""}`,
        );
      } catch (e) {
        toast.error(`${action === "pause" ? "暂停" : "继续"}失败：${(e as Error).message}`);
      } finally {
        // 无论成败都刷新:失败多半是状态已变化,重新拉取才能让按钮回到正确形态。
        lastRef.current = "";
        load();
      }
    },
    [load],
  );

  const renameTask = React.useCallback(
    async (task: Task, name: string) => {
      try {
        await api.renameTask(task.id, name);
        toast.success(`任务 #${task.id} 已重命名`);
        lastRef.current = "";
        load();
      } catch (error) {
        toast.error(`重命名失败：${(error as Error).message}`);
        throw error;
      }
    },
    [load],
  );

  const toggleTaskPinned = React.useCallback(
    async (task: Task) => {
      const pinned = taskIsPinned(task);
      try {
        await api.pinTask(task.id, !pinned);
        toast.success(pinned ? `任务 #${task.id} 已取消置顶` : `任务 #${task.id} 已置顶`);
        lastRef.current = "";
        load();
      } catch (error) {
        toast.error(`${pinned ? "取消置顶" : "置顶"}失败：${(error as Error).message}`);
        throw error;
      }
    },
    [load],
  );

  const queueTaskArchive = React.useCallback(
    async (task: Task) => {
      try {
        await api.archiveTask(task.id);
        toast.success(`任务 #${task.id} 已进入归档队列`);
        setActiveTab("archived");
        lastRef.current = "";
        load();
      } catch (error) {
        toast.error(`归档失败：${(error as Error).message}`);
        throw error;
      }
    },
    [load],
  );

  // deleteTasks 逐个删除所选任务:后端没有批量接口,且单次删除会连带清理资产/流量/文件,
  // 串行执行以免一次性打爆后端;成功的从选中集移除,失败的保留以便重试。
  const deleteTasks = React.useCallback(
    async (ids: string[], options: DeleteTaskOptions, onProgress: (done: number) => void) => {
      const total: DeleteCounts = {
        assets_deleted: 0,
        assets_detached: 0,
        traffic_deleted: 0,
        files_deleted: false,
        findings_deleted: 0,
        llm_records_deleted: 0,
      };
      const deleted: string[] = [];
      const failed: { id: string; message: string }[] = [];
      const warnings: string[] = [];

      for (const id of ids) {
        try {
          const r = await api.deleteTask(id, options);
          total.assets_deleted += r.assets_deleted;
          total.assets_detached += r.assets_detached;
          total.traffic_deleted += r.traffic_deleted;
          total.files_deleted = total.files_deleted || r.files_deleted;
          total.findings_deleted += r.findings_deleted;
          total.llm_records_deleted += r.llm_records_deleted;
          if (r.cleanup_warning) warnings.push(`#${id}：${r.cleanup_warning}`);
          deleted.push(id);
        } catch (e) {
          failed.push({ id, message: (e as Error).message });
        }
        onProgress(deleted.length + failed.length);
      }

      if (deleted.length > 0) {
        setSelectedIds((prev) => {
          const next = new Set(prev);
          for (const id of deleted) next.delete(id);
          return next;
        });
        const details = deleteDetails(total);
        const summary = `已删除 ${deleted.length} 个任务` + (details.length > 0 ? `（${details.join("，")}）` : "");
        if (warnings.length > 0) {
          toast.warning(`${summary}；部分外部数据清理未完成：${warnings.join("；")}`);
        } else {
          toast.success(summary);
        }
      }
      if (failed.length > 0) {
        const head = failed
          .slice(0, 3)
          .map((f) => `#${f.id}（${f.message}）`)
          .join("；");
        toast.error(`${failed.length} 个任务删除失败：${head}${failed.length > 3 ? " 等" : ""}`);
      }
      load();
    },
    [load],
  );

  const selectedTasks = React.useMemo(() => tasks.filter((task) => selectedIds.has(task.id)), [tasks, selectedIds]);
  const pausableTaskIDs = React.useMemo(
    () => selectedTasks.filter((task) => taskControlAction(task.status) === "pause").map((task) => task.id),
    [selectedTasks],
  );
  const resumableTaskIDs = React.useMemo(
    () => selectedTasks.filter((task) => taskControlAction(task.status) === "resume").map((task) => task.id),
    [selectedTasks],
  );
  const archivableTaskIDs = React.useMemo(
    () => selectedTasks.filter((task) => ARCHIVABLE_STATUSES.has(task.status) && !task.queued).map((task) => task.id),
    [selectedTasks],
  );

  const archiveSelectedTasks = React.useCallback(async () => {
    if (archivableTaskIDs.length === 0) return;
    const result = await api.archiveTasks(archivableTaskIDs);
    const succeeded = result.items.filter((item) => item.ok);
    const failed = result.items.filter((item) => !item.ok);
    if (succeeded.length > 0) toast.success(`已将 ${succeeded.length} 个任务加入归档队列`);
    if (failed.length > 0) {
      toast.error(
        `${failed.length} 个任务无法归档：${failed
          .slice(0, 3)
          .map((item) => `#${item.id}（${item.error || "状态已变化"}）`)
          .join("；")}`,
      );
    }
    setSelectedIds(new Set());
    setActiveTab("archived");
    lastRef.current = "";
    load();
  }, [archivableTaskIDs, load]);

  const controlSelectedTasks = React.useCallback(
    async (action: "pause" | "resume", ids: string[]) => {
      if (ids.length === 0 || batchControlling) return;
      if (ids.length > 100) {
        toast.error("一次最多控制 100 个任务");
        return;
      }
      setBatchControlling(action);
      try {
        const result = await api.controlTasksBatch(ids, action);
        const succeeded = result.items.filter((item) => item.ok);
        const failed = result.items.filter((item) => !item.ok);
        if (succeeded.length > 0) {
          toast.success(
            action === "pause"
              ? `已暂停 ${succeeded.length} 个任务`
              : `已继续 ${succeeded.length} 个任务${succeeded.some((item) => item.queued) ? "，部分任务已进入队列" : ""}`,
          );
        }
        if (failed.length > 0) {
          const details = failed
            .slice(0, 3)
            .map((item) => `#${item.id}（${item.error || "状态已变化"}）`)
            .join("；");
          toast.error(`${failed.length} 个任务操作失败：${details}${failed.length > 3 ? " 等" : ""}`);
        }
        lastRef.current = "";
        load();
      } catch (error) {
        toast.error(`${action === "pause" ? "批量暂停" : "批量继续"}失败：${(error as Error).message}`);
      } finally {
        setBatchControlling(null);
      }
    },
    [batchControlling, load],
  );

  // 后端把整批分类写入放在一个事务里，所以失败项只可能是勾选后又被删掉的任务。
  const moveSelectedTasksCategory = React.useCallback(
    async (categoryID?: number) => {
      const ids = [...selectedIds];
      if (ids.length === 0 || movingCategory) return;
      if (ids.length > 100) {
        toast.error("一次最多修改 100 个任务的分类");
        return;
      }
      setMovingCategory(true);
      try {
        const result = await api.updateTasksCategory(ids, categoryID);
        const succeeded = result.items.filter((item) => item.ok);
        const failed = result.items.filter((item) => !item.ok);
        const target = result.category?.name ?? "未分类";
        if (succeeded.length > 0) {
          toast.success(`已将 ${succeeded.length} 个任务移动到「${target}」`);
          setSelectedIds(new Set());
        }
        if (failed.length > 0) {
          const details = failed
            .slice(0, 3)
            .map((item) => `#${item.id}（${item.error || "任务已不存在"}）`)
            .join("；");
          toast.error(`${failed.length} 个任务未能移动：${details}${failed.length > 3 ? " 等" : ""}`);
        }
        refreshCategoriesAndTasks();
      } catch (error) {
        toast.error(`修改分类失败：${(error as Error).message}`);
      } finally {
        setMovingCategory(false);
      }
    },
    [movingCategory, refreshCategoriesAndTasks, selectedIds],
  );

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">任务</h1>
        <p className="text-muted-foreground text-sm">每个任务是一次独立的探索：一个目标、一棵探索图、一份交付物。</p>
      </div>
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="current">当前任务</TabsTrigger>
          <TabsTrigger value="archived">已归档</TabsTrigger>
        </TabsList>
        <TabsContent value="current">
          <Card>
            <CardContent className="flex flex-col gap-4 px-0 pt-6">
              <div className="flex flex-wrap items-center gap-2 px-4 lg:px-6">
                <div className="relative w-full sm:max-w-xs">
                  <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                  <Input
                    placeholder="搜索描述 / 目标 / ID"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    className="pl-8"
                  />
                  {query && (
                    <button
                      type="button"
                      onClick={() => setQuery("")}
                      aria-label="清除搜索"
                      className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
                    >
                      <XIcon className="size-4" />
                    </button>
                  )}
                </div>
                <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v as TaskStatus | "all")}>
                  <SelectTrigger className="w-36">
                    <SelectValue placeholder="状态" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="all">全部状态</SelectItem>
                      {STATUS_OPTIONS.map((s) => (
                        <SelectItem key={s.value} value={s.value}>
                          {s.label}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Select value={categoryFilter} onValueChange={setCategoryFilter}>
                  <SelectTrigger className="w-40">
                    <SelectValue placeholder="任务分类" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="all">全部分类</SelectItem>
                      <SelectItem value={UNCATEGORIZED_VALUE}>未分类</SelectItem>
                      {categories.map((category) => (
                        <SelectItem key={category.id} value={String(category.id)}>
                          {category.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {filtered.length}/{tasks.length} 条
                </span>
                {selectedIds.size > 0 && (
                  <>
                    <span className="text-xs tabular-nums">已选 {selectedIds.size} 个</span>
                    <Button size="sm" variant="ghost" onClick={() => setSelectedIds(new Set())}>
                      取消选择
                    </Button>
                    {pausableTaskIDs.length > 0 && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={batchControlling !== null}
                        onClick={() => void controlSelectedTasks("pause", pausableTaskIDs)}
                      >
                        {batchControlling === "pause" ? (
                          <Spinner data-icon="inline-start" />
                        ) : (
                          <PauseIcon data-icon="inline-start" />
                        )}
                        暂停 {pausableTaskIDs.length}
                      </Button>
                    )}
                    {resumableTaskIDs.length > 0 && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={batchControlling !== null}
                        onClick={() => void controlSelectedTasks("resume", resumableTaskIDs)}
                      >
                        {batchControlling === "resume" ? (
                          <Spinner data-icon="inline-start" />
                        ) : (
                          <PlayIcon data-icon="inline-start" />
                        )}
                        继续 {resumableTaskIDs.length}
                      </Button>
                    )}
                    {archivableTaskIDs.length > 0 && (
                      <ArchiveConfirmDialog
                        count={archivableTaskIDs.length}
                        onConfirm={archiveSelectedTasks}
                        trigger={
                          <Button size="sm" variant="outline">
                            <ArchiveIcon data-icon="inline-start" />
                            归档 {archivableTaskIDs.length}
                          </Button>
                        }
                      />
                    )}
                    <MoveTasksCategoryDialog
                      categories={categories}
                      count={selectedIds.size}
                      moving={movingCategory}
                      onMove={moveSelectedTasksCategory}
                    />
                    <BulkDeleteTasksDialog ids={[...selectedIds]} onDelete={deleteTasks} />
                  </>
                )}
                <ConcurrencySettingsDialog />
                <CategoryManagementDialog
                  categories={categories}
                  tasks={tasks}
                  onChanged={refreshCategoriesAndTasks}
                  onTaskMoved={applyTaskCategoryMove}
                />
                <ScheduleTasksDialog categories={categories} onCreated={refreshCategoriesAndTasks} />
                <CreateTaskSheet
                  tasks={tasks}
                  categories={categories}
                  onCreated={refreshCategoriesAndTasks}
                  onCategoriesChanged={refreshCategoriesAndTasks}
                />
              </div>

              {tasks.length === 0 ? (
                <div className="text-muted-foreground mx-4 flex items-center justify-center rounded-lg border border-dashed py-20 text-sm lg:mx-6">
                  暂无任务，点击右上角「新建任务」开始。
                </div>
              ) : filtered.length === 0 ? (
                <div className="text-muted-foreground mx-4 flex items-center justify-center rounded-lg border border-dashed py-20 text-sm lg:mx-6">
                  没有匹配的任务。
                </div>
              ) : (
                <Table className="**:data-[slot='table-cell']:px-4 **:data-[slot='table-head']:px-4">
                  <TableHeader className="[&_tr]:border-t">
                    <TableRow>
                      <TableHead className="w-10">
                        <Checkbox
                          checked={headerChecked}
                          onCheckedChange={(checked) => toggleSelectedPage(checked === true)}
                          aria-label="选择本页全部任务"
                        />
                      </TableHead>
                      <SortableTaskHead
                        field="id"
                        label="ID"
                        activeField={sortField}
                        direction={sortDirection}
                        className="font-mono"
                        onSort={sortTasksBy}
                      />
                      <TableHead>名称</TableHead>
                      <TableHead>描述</TableHead>
                      <TableHead>目标</TableHead>
                      <SortableTaskHead
                        field="status"
                        label="状态"
                        activeField={sortField}
                        direction={sortDirection}
                        onSort={sortTasksBy}
                      />
                      <TableHead className="text-center">目标进度</TableHead>
                      <TableHead className="text-center" title="严重 / 高 / 中 / 低">
                        漏洞 <span className="text-muted-foreground font-normal">严/高/中/低</span>
                      </TableHead>
                      <TableHead className="text-center">运行中 Worker</TableHead>
                      <SortableTaskHead
                        field="created"
                        label="创建时间"
                        activeField={sortField}
                        direction={sortDirection}
                        align="right"
                        onSort={sortTasksBy}
                      />
                      <SortableTaskHead
                        field="duration"
                        label="运行时长"
                        activeField={sortField}
                        direction={sortDirection}
                        align="right"
                        onSort={sortTasksBy}
                      />
                      <TableHead className="text-right">Token</TableHead>
                      <TableHead className="sticky right-0 z-10 bg-card text-right shadow-[-1px_0_0_0_hsl(var(--border))]">
                        操作
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {paginated.map((task) => (
                      <TaskRow
                        key={task.id}
                        task={task}
                        // running 任务才吃 nowSec;其余行传 0 —— props 不变,memo 就能拦下每秒 tick
                        // 带来的整表重渲染,只让在跑的那几行走时长。
                        nowSec={task.status === "running" ? nowSec : 0}
                        onDelete={deleteTask}
                        onControl={controlTask}
                        onRename={renameTask}
                        onTogglePinned={toggleTaskPinned}
                        onArchive={queueTaskArchive}
                        selected={selectedIds.has(task.id)}
                        onSelectedChange={toggleSelected}
                        tokenBudget={tokenBudget}
                      />
                    ))}
                  </TableBody>
                </Table>
              )}
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={filtered.length}
                onPageChange={setPage}
                onPageSizeChange={(nextPageSize) => {
                  setPageSize(nextPageSize);
                  setPage(1);
                }}
              />
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="archived">
          <TaskArchivesPanel onChanged={load} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
