"use client";

import * as React from "react";

import { toast } from "sonner";

import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type {
  ConstraintCompileResult,
  Finding,
  ModelTokenStat,
  Stats,
  Task,
  TaskConstraint,
  TaskGoal,
  TaskHint,
  TaskArtifact,
  TaskNode,
  TaskScopeRow,
} from "@/lib/types";

// 总览页签的状态与取数：任务/统计/意图/令牌/目标/约束/覆盖等。

export function useOverviewState(taskId: string) {
  const [task, setTask] = React.useState<Task | null>(null);
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [intents, setIntents] = React.useState<TaskNode[]>([]);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [coverage, setCoverage] = React.useState<{
    enabled: boolean;
    scope_rows: number;
    denominator: number;
    tested: number;
    pct: number | null;
    by_type: { type: string; total: number; tested: number }[];
  } | null>(null);
  // 正在重跑的意图 id（含 "__all__" 表示批量），用于禁用按钮 + 转圈。
  const [rerunning, setRerunning] = React.useState<Set<string>>(new Set());
  // 终态任务「继续运行」进行中。
  const [resuming, setResuming] = React.useState(false);
  // 测试范围列表 + 新增表单状态。
  const [scope, setScope] = React.useState<TaskScopeRow[]>([]);
  const [scopeKind, setScopeKind] = React.useState<TaskScopeRow["kind"]>("root_domain");
  const [scopeValueInput, setScopeValueInput] = React.useState("");
  const [scopeBusy, setScopeBusy] = React.useState(false);
  const [scopeErr, setScopeErr] = React.useState("");
  // 按模型的 token 用量（来自常开的 llm_usage 计量账本，逐次精确）。
  const [modelTokens, setModelTokens] = React.useState<ModelTokenStat[]>([]);
  // 任务级 token 预算（全局设置 task_token_budget；0=关闭）。与用量同屏对照，只取一次。
  const [tokenBudget, setTokenBudget] = React.useState(0);
  React.useEffect(() => {
    api
      .settings()
      .then((s) => setTokenBudget(s.task_token_budget ?? 0))
      .catch(() => undefined);
  }, []);
  // 目标管理：目标列表 + 新增表单 + 行内编辑状态。
  const [goals, setGoals] = React.useState<TaskGoal[]>([]);
  const [goalText, setGoalText] = React.useState("");
  const [goalVuln, setGoalVuln] = React.useState("");
  const [goalBusy, setGoalBusy] = React.useState(false);
  const [goalErr, setGoalErr] = React.useState("");
  const [editingGoalId, setEditingGoalId] = React.useState<string | null>(null);
  const [editText, setEditText] = React.useState("");
  const [editVuln, setEditVuln] = React.useState("");
  // 战略提示：active（待采纳）+ consumed（已采纳，最近一窗）。新增会立即唤醒 planner。
  const [hints, setHints] = React.useState<TaskHint[]>([]);
  const [hintText, setHintText] = React.useState("");
  const [hintBusy, setHintBusy] = React.useState(false);
  const [hintErr, setHintErr] = React.useState("");
  const [editingHintId, setEditingHintId] = React.useState<string | null>(null);
  const [editHintText, setEditHintText] = React.useState("");
  // 遗留物台账：agent 忘了收尾时，操作者可以直接销账/接受风险。
  const [artifacts, setArtifacts] = React.useState<TaskArtifact[]>([]);
  const [artifactRiskAccepted, setArtifactRiskAccepted] = React.useState(false);
  // 约束管理：约束列表 + 新增表单 + 行内编辑状态。
  const [constraints, setConstraints] = React.useState<TaskConstraint[]>([]);
  const [conText, setConText] = React.useState("");
  const [conKind, setConKind] = React.useState<TaskConstraint["kind"]>("deny");
  const [conBusy, setConBusy] = React.useState(false);
  const [conErr, setConErr] = React.useState("");
  const [editingConId, setEditingConId] = React.useState<string | null>(null);
  const [editConText, setEditConText] = React.useState("");
  const [editConKind, setEditConKind] = React.useState<TaskConstraint["kind"]>("deny");
  // P18 约束可执行化：把约束编译成任务级拦截规则前的预览状态。
  const [compileFor, setCompileFor] = React.useState<string | null>(null);
  const [compileResult, setCompileResult] = React.useState<ConstraintCompileResult | null>(null);
  const [compileBusy, setCompileBusy] = React.useState(false);

  const loadTokens = React.useCallback(async () => {
    try {
      const resp = await api.tokensByModel(taskId);
      setModelTokens(resp.models);
    } catch {
      // 忽略：无 PG 时接口报错，卡片自然为空
    }
  }, [taskId]);

  const loadScope = React.useCallback(async () => {
    try {
      const resp = await api.taskScope(taskId);
      setScope(resp.scope);
    } catch {
      // 忽略：无 asset store 时接口 503，范围卡片自然为空
    }
  }, [taskId]);

  const loadGoals = React.useCallback(async () => {
    try {
      const resp = await api.taskGoals(taskId);
      setGoals(resp.goals);
    } catch {
      // 忽略：瞬时错误，下次轮询重试
    }
  }, [taskId]);

  const addGoal = async () => {
    const text = goalText.trim();
    if (!text) return;
    setGoalBusy(true);
    setGoalErr("");
    try {
      await api.addGoal(taskId, text, goalVuln.trim() || undefined);
      setGoalText("");
      setGoalVuln("");
      await loadGoals();
    } catch (e) {
      setGoalErr(e instanceof Error ? e.message : "添加失败");
    } finally {
      setGoalBusy(false);
    }
  };

  const startEditGoal = (g: TaskGoal) => {
    setEditingGoalId(g.id);
    setEditText(g.text);
    setEditVuln(g.vulnclass ?? "");
  };

  const cancelEditGoal = () => {
    setEditingGoalId(null);
    setEditText("");
    setEditVuln("");
  };

  const saveEditGoal = async (g: TaskGoal) => {
    const text = editText.trim();
    if (!text) return;
    setGoalBusy(true);
    setGoalErr("");
    try {
      await api.updateGoal(taskId, g.id, text, editVuln.trim() || undefined);
      cancelEditGoal();
      await loadGoals();
    } catch (e) {
      setGoalErr(e instanceof Error ? e.message : "保存失败");
    } finally {
      setGoalBusy(false);
    }
  };

  const removeGoal = async (g: TaskGoal) => {
    setGoals((prev) => prev.filter((x) => x.id !== g.id));
    try {
      await api.deleteGoal(taskId, g.id);
    } catch (e) {
      await loadGoals(); // 删除失败：重新拉取还原
      toast.error(`删除目标失败：${e instanceof Error ? e.message : "未知错误"}`);
    }
  };

  const loadHints = React.useCallback(async () => {
    try {
      const resp = await api.taskHints(taskId);
      setHints(resp.hints);
    } catch {
      // 忽略：瞬时错误，下次轮询重试
    }
  }, [taskId]);

  const addHint = async () => {
    const text = hintText.trim();
    if (!text) return;
    setHintBusy(true);
    setHintErr("");
    try {
      await api.addTaskHint(taskId, text);
      setHintText("");
      await loadHints();
    } catch (e) {
      // 保留输入：校验失败（过长/重复/上限）时人改一下就能重发。
      setHintErr(e instanceof Error ? e.message : "发送失败");
    } finally {
      setHintBusy(false);
    }
  };

  const removeHint = async (h: TaskHint) => {
    setHints((prev) => prev.filter((x) => x.id !== h.id));
    try {
      await api.deleteTaskHint(taskId, h.id);
    } catch (e) {
      await loadHints(); // 删除失败：重新拉取还原
      toast.error(`删除提示失败：${e instanceof Error ? e.message : "未知错误"}`);
    }
  };

  const startEditHint = (h: TaskHint) => {
    setEditingHintId(h.id);
    setEditHintText(h.text);
  };

  const cancelEditHint = () => {
    setEditingHintId(null);
    setEditHintText("");
  };

  const saveEditHint = async (h: TaskHint) => {
    const text = editHintText.trim();
    if (!text || text === h.text) {
      cancelEditHint();
      return;
    }
    setHintBusy(true);
    setHintErr("");
    try {
      await api.updateTaskHint(taskId, h.id, { text });
      cancelEditHint();
      await loadHints();
    } catch (e) {
      setHintErr(e instanceof Error ? e.message : "保存失败");
    } finally {
      setHintBusy(false);
    }
  };

  // 撤销采纳：误采纳的提示回到待采纳列表，规划者下一轮会重新看到它。
  const restoreHint = async (h: TaskHint) => {
    setHintBusy(true);
    try {
      await api.updateTaskHint(taskId, h.id, { state: "active" });
      toast.success("已撤销采纳，提示回到待采纳列表");
      await loadHints();
    } catch (e) {
      toast.error(`撤销采纳失败：${e instanceof Error ? e.message : "未知错误"}`);
    } finally {
      setHintBusy(false);
    }
  };

  const remindHint = async (h: TaskHint) => {
    setHintBusy(true);
    try {
      await api.remindTaskHint(taskId, h.id);
      toast.success("已再次提醒规划者");
    } catch (e) {
      toast.error(`提醒失败：${e instanceof Error ? e.message : "未知错误"}`);
    } finally {
      setHintBusy(false);
    }
  };

  const loadArtifacts = React.useCallback(async () => {
    try {
      const resp = await api.taskArtifacts(taskId);
      setArtifacts(resp.artifacts);
      setArtifactRiskAccepted(resp.riskAccepted);
    } catch {
      // 忽略：瞬时错误，下次轮询重试
    }
  }, [taskId]);

  // 销账/接受风险由调用方 catch 并显示错误；成功在这里刷新台账。
  const clearArtifact = async (a: TaskArtifact, evidence: string, note?: string) => {
    await api.clearTaskArtifact(taskId, a.id, evidence, note);
    toast.success("已标记为已清理");
    await loadArtifacts();
  };

  const acceptArtifactRisk = async (reason: string) => {
    await api.acceptTaskArtifactRisk(taskId, reason);
    setArtifactRiskAccepted(true);
    toast.success("已接受遗留物风险，结论会进报告语境");
    await loadArtifacts();
  };

  const loadConstraints = React.useCallback(async () => {
    try {
      const resp = await api.taskConstraints(taskId);
      setConstraints(resp.constraints);
    } catch {
      // 忽略：瞬时错误，下次轮询重试
    }
  }, [taskId]);

  const addConstraint = async () => {
    const text = conText.trim();
    if (!text) return;
    setConBusy(true);
    setConErr("");
    try {
      await api.addConstraint(taskId, text, conKind);
      setConText("");
      await loadConstraints();
    } catch (e) {
      setConErr(e instanceof Error ? e.message : "添加失败");
    } finally {
      setConBusy(false);
    }
  };

  const startEditConstraint = (c: TaskConstraint) => {
    setEditingConId(c.id);
    setEditConText(c.text);
    setEditConKind(c.kind);
  };

  const cancelEditConstraint = () => {
    setEditingConId(null);
    setEditConText("");
    setEditConKind("deny");
  };

  const saveEditConstraint = async (c: TaskConstraint) => {
    const text = editConText.trim();
    if (!text) return;
    setConBusy(true);
    setConErr("");
    try {
      const res = await api.updateConstraint(taskId, c.id, text, editConKind);
      if (res.dropped_rules > 0) {
        toast.info(`约束已改，撤掉了它此前生效的 ${res.dropped_rules} 条拦截规则；需要重新「编译为规则」才会再生效`);
      }
      cancelEditConstraint();
      await loadConstraints();
    } catch (e) {
      setConErr(e instanceof Error ? e.message : "保存失败");
    } finally {
      setConBusy(false);
    }
  };

  const removeConstraint = async (c: TaskConstraint) => {
    setConstraints((prev) => prev.filter((x) => x.id !== c.id));
    try {
      await api.deleteConstraint(taskId, c.id);
    } catch (e) {
      await loadConstraints(); // 删除失败：重新拉取还原
      toast.error(`删除约束失败：${e instanceof Error ? e.message : "未知错误"}`);
    }
  };

  // openCompile 先编译（只读），让人看到"会生成哪些规则、会命中最近哪些真实调用"再决定。
  const openCompile = async (c: TaskConstraint) => {
    setCompileFor(c.id);
    setCompileResult(null);
    setCompileBusy(true);
    setConErr("");
    try {
      setCompileResult(await api.compileConstraint(taskId, c.id));
    } catch (e) {
      setConErr(e instanceof Error ? e.message : "编译失败");
      setCompileFor(null);
    } finally {
      setCompileBusy(false);
    }
  };

  const applyCompile = async (c: TaskConstraint) => {
    setCompileBusy(true);
    setConErr("");
    try {
      const res = await api.applyConstraint(taskId, c.id);
      const n = res.rules?.length ?? 0;
      const sc = res.scopes?.length ?? 0;
      toast.success(
        n + sc > 0
          ? `已生效：${n > 0 ? `${n} 条拦截规则` : ""}${n > 0 && sc > 0 ? " + " : ""}${sc > 0 ? `${sc} 条授权范围` : ""}（只对本任务生效）`
          : "没有可编译的内容（见说明）",
      );
      setCompileFor(null);
      setCompileResult(null);
      await loadConstraints();
    } catch (e) {
      setConErr(e instanceof Error ? e.message : "应用失败");
    } finally {
      setCompileBusy(false);
    }
  };

  const addScope = async () => {
    const value = scopeValueInput.trim();
    if (!value) return;
    setScopeBusy(true);
    setScopeErr("");
    try {
      await api.addTaskScope(taskId, scopeKind, value);
      setScopeValueInput("");
      await loadScope();
    } catch (e) {
      setScopeErr(e instanceof Error ? e.message : "添加失败");
    } finally {
      setScopeBusy(false);
    }
  };

  const removeScope = async (row: TaskScopeRow) => {
    setScope((prev) => prev.filter((s) => s.id !== row.id));
    try {
      await api.deleteTaskScope(taskId, row.id);
    } catch (e) {
      await loadScope(); // 删除失败：重新拉取还原
      toast.error(`删除范围失败：${e instanceof Error ? e.message : "未知错误"}`);
    }
  };

  const markRerun = (key: string, on: boolean) =>
    setRerunning((prev) => {
      const next = new Set(prev);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });

  // 重跑单条：置回 open（乐观更新本地 state，3s 轮询兜底），worker 会重新认领、从头再跑。
  const rerunOne = async (id: string) => {
    markRerun(id, true);
    try {
      await api.rerunIntent(taskId, id);
      setIntents((prev) => prev.map((i) => (i.id === id ? { ...i, state: "open" } : i)));
    } catch (e) {
      // 失败时提示：下次轮询仍显示 blocked，用户可再点——但静默失败会让人以为已经重跑了。
      toast.error(`重跑失败：${e instanceof Error ? e.message : "未知错误"}`);
    } finally {
      markRerun(id, false);
    }
  };

  // 批量重跑本任务全部 blocked。
  const rerunAll = async () => {
    markRerun("__all__", true);
    try {
      await api.rerunBlocked(taskId);
      setIntents((prev) => prev.map((i) => (i.state === "blocked" ? { ...i, state: "open" } : i)));
    } catch (e) {
      toast.error(`批量重跑失败：${e instanceof Error ? e.message : "未知错误"}`);
    } finally {
      markRerun("__all__", false);
    }
  };

  // 终态任务的「继续运行」：后端复活任务（仅当 frontier 里确有从未执行的 open 意图，
  // 否则明确报错并指出替代路径），worker 重新领取它们。乐观更新任务态，轮询兜底。
  const resumeTerminal = async () => {
    if (resuming) return;
    setResuming(true);
    try {
      await api.controlTask(taskId, "resume");
      toast.success("已继续运行：worker 将重新领取未执行的意图");
      setTask((prev) => (prev ? { ...prev, status: "running", completed_unix: 0 } : prev));
    } catch (e) {
      toast.error(`继续运行失败：${(e as Error).message}`);
    } finally {
      setResuming(false);
    }
  };

  usePolling(
    async (alive) => {
      let loading = false;

      const load = async () => {
        if (loading) return;
        loading = true;
        try {
          const [taskResp, statsResp, intentsResp, findingsResp] = await Promise.all([
            api.task(taskId),
            api.stats(taskId),
            api.intents(taskId),
            api.findings(taskId),
          ]);
          if (!alive()) return;
          const activeTask = statsResp.active_task;
          setTask(
            activeTask
              ? {
                  ...taskResp,
                  in_flight: activeTask.in_flight,
                  goals_total: activeTask.goals_total,
                  goals_met: activeTask.goals_met,
                  engine_mode: statsResp.engine_mode ?? activeTask.engine_mode,
                  paused: activeTask.paused,
                }
              : taskResp,
          );
          setStats(statsResp);
          setIntents(intentsResp);
          setFindings(findingsResp);
          // coverage is independent + may 503 when no asset store — fetch separately so
          // its failure never blocks the others.
          api
            .taskCoverage(taskId)
            .then((c) => {
              if (alive()) setCoverage(c);
            })
            .catch(() => {
              // A transient coverage failure is retried by the next poll.
            });
        } catch {
          // transient errors are ignored; the next poll will retry
        } finally {
          loading = false;
        }
      };

      await Promise.all([
        load(),
        loadScope(),
        loadTokens(),
        loadGoals(),
        loadHints(),
        loadArtifacts(),
        loadConstraints(),
      ]);
    },
    3000,
    [taskId, loadScope, loadTokens, loadGoals, loadHints, loadArtifacts, loadConstraints],
  );

  const running = intents.filter((i) => i.state === "running");
  const open = intents.filter((i) => i.state === "open");
  const blocked = intents.filter((i) => i.state === "blocked");
  const taskFindings = findings.filter((f) => f.task_id === taskId);
  const goalsPct = task?.goals_total ? Math.round(((task.goals_met ?? 0) / task.goals_total) * 100) : 0;
  // token 合计（跨全部模型），用于卡片头部总览。
  const tokenTotals = modelTokens.reduce(
    (acc, m) => {
      acc.input += m.input_tokens;
      acc.output += m.output_tokens;
      acc.cacheRead += m.cache_read_tokens;
      acc.cacheWrite += m.cache_write_tokens;
      acc.calls += m.calls;
      return acc;
    },
    { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, calls: 0 },
  );

  // 预算对照：用量 = 输入+输出（与后端 BudgetStage 的计量口径一致）。
  const budgetUsed = tokenTotals.input + tokenTotals.output;
  const budgetPct = tokenBudget > 0 ? (budgetUsed / tokenBudget) * 100 : 0;

  return {
    task,
    setTask,
    stats,
    setStats,
    intents,
    setIntents,
    findings,
    setFindings,
    coverage,
    setCoverage,
    rerunning,
    setRerunning,
    resuming,
    setResuming,
    scope,
    setScope,
    scopeKind,
    setScopeKind,
    scopeValueInput,
    setScopeValueInput,
    scopeBusy,
    setScopeBusy,
    scopeErr,
    setScopeErr,
    modelTokens,
    setModelTokens,
    tokenBudget,
    setTokenBudget,
    goals,
    setGoals,
    goalText,
    setGoalText,
    goalVuln,
    setGoalVuln,
    goalBusy,
    setGoalBusy,
    goalErr,
    setGoalErr,
    editingGoalId,
    setEditingGoalId,
    editText,
    setEditText,
    editVuln,
    setEditVuln,
    hints,
    setHints,
    hintText,
    setHintText,
    hintBusy,
    setHintBusy,
    hintErr,
    setHintErr,
    editingHintId,
    setEditingHintId,
    editHintText,
    setEditHintText,
    artifacts,
    setArtifacts,
    artifactRiskAccepted,
    setArtifactRiskAccepted,
    constraints,
    setConstraints,
    conText,
    setConText,
    conKind,
    setConKind,
    conBusy,
    setConBusy,
    conErr,
    setConErr,
    editingConId,
    setEditingConId,
    editConText,
    setEditConText,
    editConKind,
    setEditConKind,
    compileFor,
    setCompileFor,
    compileResult,
    setCompileResult,
    compileBusy,
    setCompileBusy,
    loadTokens,
    loadScope,
    loadGoals,
    addGoal,
    startEditGoal,
    cancelEditGoal,
    saveEditGoal,
    removeGoal,
    loadHints,
    addHint,
    removeHint,
    startEditHint,
    cancelEditHint,
    saveEditHint,
    restoreHint,
    remindHint,
    loadArtifacts,
    clearArtifact,
    acceptArtifactRisk,
    loadConstraints,
    addConstraint,
    startEditConstraint,
    cancelEditConstraint,
    saveEditConstraint,
    removeConstraint,
    openCompile,
    applyCompile,
    addScope,
    removeScope,
    markRerun,
    rerunOne,
    rerunAll,
    resumeTerminal,
    running,
    open,
    blocked,
    taskFindings,
    goalsPct,
    tokenTotals,
    budgetUsed,
    budgetPct,
  };
}
