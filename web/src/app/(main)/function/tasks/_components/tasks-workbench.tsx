"use client";

import * as React from "react";

import { useSearchParams } from "next/navigation";

import { ArchiveIcon, CalendarClockIcon, DownloadIcon, PauseIcon, PlayIcon, SearchIcon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { CopyLinkButton } from "@/components/copy-link-button";
import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { remainingSelection } from "@/lib/batch-selection";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { createLatestRequest, runLatestRequest } from "@/lib/request-lifecycle";
import { useStoredSortPreference } from "@/lib/sort-preference";
import type { DeleteTaskOptions, Task, TaskCategory, TaskStatus } from "@/lib/types";
import { readUrlView, useUrlViewPopState, useUrlViewSync } from "@/lib/url-view";

import { CreateTaskSheet } from "./create-task-sheet";
import { SchedulesPanel } from "./schedules-panel";
import { ArchiveConfirmDialog, TaskArchivesPanel } from "./task-archives";
import { CategoryManagementDialog } from "./task-categories";
import { TaskCompareDialog } from "./task-compare-dialog";
import { BulkDeleteTasksDialog, MoveTasksCategoryDialog } from "./task-dialogs";
import {
  type DeleteCounts,
  deleteDetails,
  deleteSummary,
  POLL_MS,
  STATUS_OPTIONS,
  TASK_FILTER_PREFERENCE_KEY,
  TASK_SORT_FIELDS,
  TASK_SORT_PREFERENCE_KEY,
  type TaskSortField,
  taskIsPinned,
  UNCATEGORIZED_VALUE,
} from "./task-helpers";
import { ConcurrencySettingsDialog, SortableTaskHead, TaskRow } from "./task-row";

export type TasksTab = "current" | "schedules" | "archived";

function isTasksTab(v: string | undefined): v is TasksTab {
  return v === "current" || v === "schedules" || v === "archived";
}

function TasksWorkbenchInner({ initialTab = "current" }: { initialTab?: TasksTab }) {
  const searchParams = useSearchParams();
  const tabParam = searchParams.get("tab") ?? undefined;
  const prevTabParamRef = React.useRef<string | undefined>(tabParam);

  const urlDefaults = React.useMemo(
    () => ({
      tab: initialTab,
      q: "",
      status: "all",
      category: "all",
      sort: "id",
      dir: "desc",
    }),
    [initialTab],
  );

  const [activeTab, setActiveTab] = React.useState<TasksTab>(initialTab);
  const [tasks, setTasks] = React.useState<Task[]>([]);
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
  const [totalTasks, setTotalTasks] = React.useState(0);
  const [filteredTasks, setFilteredTasks] = React.useState(0);
  const [listLoading, setListLoading] = React.useState(false);
  const [listError, setListError] = React.useState<string | null>(null);
  const [nowSec, setNowSec] = React.useState(() => Math.floor(Date.now() / 1000));
  const [batchControlling, setBatchControlling] = React.useState<"pause" | "resume" | null>(null);
  const [movingCategory, setMovingCategory] = React.useState(false);
  const [compareOpen, setCompareOpen] = React.useState(false);

  React.useEffect(() => {
    const url = readUrlView(urlDefaults);
    let parsed: { status?: unknown; category?: unknown } = {};
    const raw = getLocalStorageValue(TASK_FILTER_PREFERENCE_KEY);
    if (raw) {
      try {
        parsed = JSON.parse(raw) as typeof parsed;
      } catch {
        // Ignore malformed preferences
      }
    }
    const statusValue = url.status ?? parsed.status;
    if (statusValue === "all" || STATUS_OPTIONS.some((option) => option.value === statusValue)) {
      setStatusFilter(statusValue as TaskStatus | "all");
    }
    const categoryValue = url.category ?? parsed.category;
    if (
      typeof categoryValue === "string" &&
      (categoryValue === "all" || categoryValue === UNCATEGORIZED_VALUE || /^\d+$/.test(categoryValue))
    ) {
      setCategoryFilter(categoryValue);
    }
    if (isTasksTab(url.tab)) setActiveTab(url.tab);
    if (url.q !== undefined) setQuery(url.q);
    if (TASK_SORT_FIELDS.includes(url.sort as TaskSortField)) {
      const direction = url.dir === "asc" ? "asc" : "desc";
      setSortPreference({ field: url.sort as TaskSortField, direction });
    }
    setFiltersHydrated(true);
  }, [setSortPreference, urlDefaults]);

  React.useEffect(() => {
    if (!filtersHydrated || initialTab !== "current") return;
    if (tabParam === prevTabParamRef.current) return;
    prevTabParamRef.current = tabParam;
    if (isTasksTab(tabParam)) {
      setActiveTab(tabParam);
    } else if (!tabParam) {
      setActiveTab("current");
    }
  }, [filtersHydrated, initialTab, tabParam]);

  React.useEffect(() => {
    if (!filtersHydrated) return;
    setLocalStorageValue(
      TASK_FILTER_PREFERENCE_KEY,
      JSON.stringify({ status: statusFilter, category: categoryFilter }),
    );
  }, [categoryFilter, filtersHydrated, statusFilter]);

  const [debouncedQuery, setDebouncedQuery] = React.useState("");
  React.useEffect(() => {
    const timer = setTimeout(() => setDebouncedQuery(query), 250);
    return () => clearTimeout(timer);
  }, [query]);

  useUrlViewSync(
    {
      tab: activeTab,
      q: debouncedQuery,
      status: statusFilter,
      category: categoryFilter,
      sort: sortField,
      dir: sortDirection,
    },
    urlDefaults,
    filtersHydrated,
  );

  useUrlViewPopState(() => {
    const url = readUrlView(urlDefaults);
    if (isTasksTab(url.tab)) setActiveTab(url.tab);
    else setActiveTab(initialTab);
    setQuery(url.q ?? "");
    setDebouncedQuery(url.q ?? "");
    setStatusFilter(
      url.status === "all" || STATUS_OPTIONS.some((option) => option.value === url.status)
        ? (url.status as TaskStatus | "all")
        : "all",
    );
    setCategoryFilter(url.category || "all");
    if (TASK_SORT_FIELDS.includes(url.sort as TaskSortField)) {
      setSortPreference({
        field: url.sort as TaskSortField,
        direction: url.dir === "asc" ? "asc" : "desc",
      });
    }
  });

  // biome-ignore lint/correctness/useExhaustiveDependencies: filters intentionally reset pagination.
  React.useEffect(() => {
    setPage(1);
  }, [debouncedQuery, statusFilter, categoryFilter, sortField, sortDirection]);

  const paginated = tasks;
  const sortTasksBy = React.useCallback(
    (field: TaskSortField) => {
      setSortPreference((current) => {
        if (current.field !== field) return { field, direction: "desc" };
        return {
          field,
          direction: current.direction === "asc" ? "desc" : "asc",
        };
      });
    },
    [setSortPreference],
  );

  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(() => new Set());

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

  const lastRef = React.useRef<string>("");
  const [requestOwner] = React.useState(() => createLatestRequest());
  const queryKey = JSON.stringify([
    page,
    pageSize,
    debouncedQuery,
    statusFilter,
    categoryFilter,
    sortField,
    sortDirection,
  ]);
  const queryKeyRef = React.useRef(queryKey);
  queryKeyRef.current = queryKey;
  React.useEffect(() => () => requestOwner.cancel(), [requestOwner]);

  const fetchTasks = React.useCallback(() => {
    const requestedKey = queryKeyRef.current;
    setListLoading(true);
    return runLatestRequest(
      requestOwner,
      (signal) =>
        api.tasks(
          {
            page,
            size: pageSize,
            q: debouncedQuery.trim(),
            status: statusFilter === "all" ? "" : statusFilter,
            category:
              categoryFilter === "all" ? "" : categoryFilter === UNCATEGORIZED_VALUE ? "uncategorized" : categoryFilter,
            sort: sortField,
            dir: sortDirection,
          },
          signal,
        ),
      {
        success: (r) => {
          if (queryKeyRef.current !== requestedKey) return;
          setListError(null);
          const next = r.tasks.map((t) => (t.id === r.active ? { ...t, active: true } : t));
          const total = r.total ?? next.length;
          const filtered = r.filtered ?? next.length;
          const sig = JSON.stringify([next, total, filtered]);
          if (sig === lastRef.current) return;
          lastRef.current = sig;
          setTasks(next);
          setTotalTasks(total);
          setFilteredTasks(filtered);
        },
        error: (error) => {
          if (queryKeyRef.current === requestedKey) {
            setListError(error instanceof Error ? error.message : "加载任务失败");
          }
        },
        settled: () => {
          if (queryKeyRef.current === requestedKey) setListLoading(false);
        },
      },
    );
  }, [requestOwner, page, pageSize, debouncedQuery, statusFilter, categoryFilter, sortField, sortDirection]);

  const load = React.useCallback(() => fetchTasks().catch(() => undefined), [fetchTasks]);

  const loadCategories = React.useCallback(
    () =>
      api
        .taskCategories()
        .then((next) => {
          setCategories(next);
          setCategoriesLoaded(true);
        })
        .catch(() => {}),
    [],
  );

  usePolling(
    async () => {
      await Promise.all([fetchTasks(), loadCategories()]);
    },
    POLL_MS,
    [fetchTasks, loadCategories],
  );

  const refreshCategoriesAndTasks = React.useCallback(() => {
    lastRef.current = "";
    void loadCategories();
    void load();
  }, [load, loadCategories]);

  const applyTaskCategoryMove = React.useCallback(
    (taskID: string, category: TaskCategory | null) => {
      setTasks((current) => {
        const next = current.map((task) =>
          task.id === taskID
            ? {
                ...task,
                category_id: category?.id,
                category_name: category?.name,
              }
            : task,
        );
        lastRef.current = JSON.stringify(next);
        return next;
      });
      void loadCategories();
    },
    [loadCategories],
  );

  React.useEffect(() => {
    if (!categoriesLoaded) return;
    if (categoryFilter === "all" || categoryFilter === UNCATEGORIZED_VALUE) return;
    if (!categories.some((category) => String(category.id) === categoryFilter)) setCategoryFilter("all");
  }, [categories, categoriesLoaded, categoryFilter]);

  const hasRunning = React.useMemo(() => tasks.some((t) => t.status === "running"), [tasks]);

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
        void load();
      } catch (e) {
        toast.error(`删除失败：${(e as Error).message}`);
        throw e;
      }
    },
    [load],
  );

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
        lastRef.current = "";
        void load();
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
        void load();
      } catch (error) {
        toast.error(`重命名失败：${(error as Error).message}`);
        throw error;
      }
    },
    [load],
  );

  const remarkTask = React.useCallback(
    async (task: Task, remark: string) => {
      try {
        await api.updateTask(task.id, { remark });
        toast.success(`任务 #${task.id} 备注已保存`);
        lastRef.current = "";
        void load();
      } catch (error) {
        toast.error(`备注保存失败：${(error as Error).message}`);
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
        void load();
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
        void load();
      } catch (error) {
        toast.error(`归档失败：${(error as Error).message}`);
        throw error;
      }
    },
    [load],
  );

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
        const summary = `已删除 ${deleted.length} 个任务${details.length > 0 ? `（${details.join("，")}）` : ""}`;
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
      void load();
    },
    [load],
  );

  const selectedTaskIDs = React.useMemo(() => [...selectedIds], [selectedIds]);

  const cloneTask = React.useCallback(
    async (task: Task) => {
      try {
        const created = await api.cloneTask(task.id);
        toast.success(`已克隆为任务 #${created.id}`);
        lastRef.current = "";
        void load();
      } catch (e) {
        toast.error(`克隆失败：${(e as Error).message}`);
      }
    },
    [load],
  );

  const saveTaskTemplate = React.useCallback(async (task: Task) => {
    try {
      const template = await api.saveTaskTemplate(task.id);
      toast.success(`已保存为模板「${template.name}」`);
    } catch (e) {
      toast.error(`保存模板失败：${(e as Error).message}`);
    }
  }, []);

  const archiveSelectedTasks = React.useCallback(async () => {
    if (selectedTaskIDs.length === 0) return;
    if (selectedTaskIDs.length > 100) {
      toast.error("一次最多归档 100 个任务");
      return;
    }
    try {
      const result = await api.archiveTasks(selectedTaskIDs);
      const succeeded = result.items.filter((item) => item.ok);
      const failed = result.items.filter((item) => !item.ok);
      if (succeeded.length > 0) toast.success(`已将 ${succeeded.length} 个任务加入归档队列`);
      if (failed.length > 0) {
        toast.error(
          `${failed.length} 个任务未能归档：${failed
            .slice(0, 3)
            .map((item) => `#${item.id}（${item.error || "状态已变化"}）`)
            .join("；")}`,
        );
      }
      setSelectedIds((current) => remainingSelection(current, result.items));
      if (failed.length === 0) setActiveTab("archived");
      lastRef.current = "";
      void load();
    } catch (error) {
      toast.error(`批量归档失败：${(error as Error).message}`);
    }
  }, [selectedTaskIDs, load]);

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
        setSelectedIds((current) => remainingSelection(current, result.items));
        lastRef.current = "";
        void load();
      } catch (error) {
        toast.error(`${action === "pause" ? "批量暂停" : "批量继续"}失败：${(error as Error).message}`);
      } finally {
        setBatchControlling(null);
      }
    },
    [batchControlling, load],
  );

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
          setSelectedIds((current) => remainingSelection(current, result.items));
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
        <h1 className="font-semibold text-xl tracking-tight">任务</h1>
        <p className="text-muted-foreground text-sm">每个任务是一次独立的探索：一个目标、一棵探索图、一份交付物。</p>
      </div>
      <Tabs value={activeTab} onValueChange={(v) => setActiveTab(v as TasksTab)}>
        <TabsList>
          <TabsTrigger value="current">当前任务</TabsTrigger>
          <TabsTrigger value="schedules">
            <CalendarClockIcon className="size-3.5" /> 计划任务
          </TabsTrigger>
          <TabsTrigger value="archived">已归档</TabsTrigger>
        </TabsList>
        <TabsContent value="current">
          <Card>
            <CardContent className="flex flex-col gap-4 px-0 pt-6">
              <div className="flex flex-wrap items-center gap-2 px-4 lg:px-6">
                <div className="relative w-full sm:max-w-xs">
                  <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
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
                      className="absolute top-1/2 right-2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
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
                  {filteredTasks}/{totalTasks} 条
                </span>
                {listError && (
                  <span role="alert" className="text-destructive text-xs">
                    刷新失败，显示上次数据：{listError}
                    <Button size="sm" variant="ghost" onClick={() => void load()}>
                      重试
                    </Button>
                  </span>
                )}
                {selectedIds.size > 0 && (
                  <>
                    <span className="text-xs tabular-nums">已选 {selectedIds.size} 个（服务端逐项校验状态）</span>
                    <Button size="sm" variant="ghost" onClick={() => setSelectedIds(new Set())}>
                      取消选择
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={batchControlling !== null}
                      onClick={() => void controlSelectedTasks("pause", selectedTaskIDs)}
                    >
                      {batchControlling === "pause" ? (
                        <Spinner data-icon="inline-start" />
                      ) : (
                        <PauseIcon data-icon="inline-start" />
                      )}
                      暂停 {selectedTaskIDs.length}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={batchControlling !== null}
                      onClick={() => void controlSelectedTasks("resume", selectedTaskIDs)}
                    >
                      {batchControlling === "resume" ? (
                        <Spinner data-icon="inline-start" />
                      ) : (
                        <PlayIcon data-icon="inline-start" />
                      )}
                      继续 {selectedTaskIDs.length}
                    </Button>
                    <ArchiveConfirmDialog
                      count={selectedTaskIDs.length}
                      onConfirm={archiveSelectedTasks}
                      trigger={
                        <Button size="sm" variant="outline">
                          <ArchiveIcon data-icon="inline-start" />
                          归档 {selectedTaskIDs.length}
                        </Button>
                      }
                    />
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={selectedTaskIDs.length < 2}
                      onClick={() => setCompareOpen(true)}
                    >
                      对比 {selectedTaskIDs.length}
                    </Button>
                    <MoveTasksCategoryDialog
                      categories={categories}
                      count={selectedIds.size}
                      moving={movingCategory}
                      onMove={moveSelectedTasksCategory}
                    />
                    <BulkDeleteTasksDialog ids={[...selectedIds]} onDelete={deleteTasks} />
                  </>
                )}
                <CopyLinkButton />
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    void api
                      .downloadTasksCSV()
                      .then(() => toast.success("已导出任务列表 CSV"))
                      .catch((e: unknown) => toast.error(`导出失败：${e instanceof Error ? e.message : "未知错误"}`))
                  }
                >
                  <DownloadIcon data-icon="inline-start" />
                  导出全部 CSV
                </Button>
                <ConcurrencySettingsDialog />
                <CategoryManagementDialog
                  categories={categories}
                  tasks={tasks}
                  onChanged={refreshCategoriesAndTasks}
                  onTaskMoved={applyTaskCategoryMove}
                />
                <CreateTaskSheet
                  categories={categories}
                  onCreated={refreshCategoriesAndTasks}
                  onCategoriesChanged={refreshCategoriesAndTasks}
                />
              </div>

              {totalTasks === 0 ? (
                <div className="mx-4 flex items-center justify-center rounded-lg border border-dashed py-20 text-muted-foreground text-sm lg:mx-6">
                  暂无任务，点击右上角「新建任务」开始。
                </div>
              ) : filteredTasks === 0 ? (
                <div className="mx-4 flex items-center justify-center rounded-lg border border-dashed py-20 text-muted-foreground text-sm lg:mx-6">
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
                      <TableHead>备注</TableHead>
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
                        漏洞 <span className="font-normal text-muted-foreground">严/高/中/低</span>
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
                        nowSec={task.status === "running" ? nowSec : 0}
                        onDelete={deleteTask}
                        onControl={controlTask}
                        onRename={renameTask}
                        onRemark={remarkTask}
                        onTogglePinned={toggleTaskPinned}
                        onArchive={queueTaskArchive}
                        onClone={cloneTask}
                        onSaveTemplate={saveTaskTemplate}
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
                total={filteredTasks}
                busy={listLoading}
                onPageChange={setPage}
                onPageSizeChange={(nextPageSize) => {
                  setPageSize(nextPageSize);
                  setPage(1);
                }}
              />
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="schedules" className="pt-2">
          <SchedulesPanel onTaskTriggered={load} />
        </TabsContent>
        <TabsContent value="archived">
          <TaskArchivesPanel onChanged={load} />
        </TabsContent>
      </Tabs>
      <TaskCompareDialog ids={selectedTaskIDs} open={compareOpen} onOpenChange={setCompareOpen} />
    </div>
  );
}

export function TasksWorkbench({ initialTab = "current" }: { initialTab?: TasksTab }) {
  return (
    <React.Suspense fallback={null}>
      <TasksWorkbenchInner initialTab={initialTab} />
    </React.Suspense>
  );
}
