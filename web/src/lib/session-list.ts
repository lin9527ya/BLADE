import type { Session, SessionRole, TaskNode } from "./types";

// 左侧会话列表纯函数：将已假删除 / 规划丢弃（state='deleted'）的 Worker 排除出
// 左侧栏展示，避免大量历史废弃意图刷屏影响观感；同时允许深链直接打开右栏详情。

export function isDeletedIntent(node: Pick<TaskNode, "state">): boolean {
  return node.state === "deleted";
}

export function visibleWorkerIntents(intents: readonly TaskNode[]): TaskNode[] {
  return intents.filter((node) => !isDeletedIntent(node));
}

export function buildSessionList({
  mainSessions,
  plannerSession,
  workerSessions,
  systemSession,
  locatedSession,
}: {
  mainSessions: readonly Session[];
  plannerSession: Session;
  workerSessions: readonly Session[];
  systemSession: Session;
  locatedSession?: Session;
}): {
  sessions: Session[];
  grouped: Record<SessionRole, Session[]>;
} {
  const visibleWorkers = workerSessions.filter((s) => s.status !== "deleted");
  const sessions: Session[] = [...mainSessions, plannerSession, ...visibleWorkers, systemSession];
  if (locatedSession && !sessions.some((s) => s.id === locatedSession.id)) {
    sessions.push(locatedSession);
  }

  // 右栏可通过 ?session=intent:<id> 深链定位到已删除或未分页到的 Worker，
  // 但左侧 Workers 列表只展示未删除项（未分页到的非删除项仍保留在左侧便于切换）。
  const workerList =
    locatedSession &&
    locatedSession.role === "worker" &&
    locatedSession.status !== "deleted" &&
    !visibleWorkers.some((s) => s.id === locatedSession.id)
      ? [...visibleWorkers, locatedSession]
      : visibleWorkers;

  const grouped: Record<SessionRole, Session[]> = {
    mainagent: [...mainSessions],
    planner: [plannerSession],
    worker: workerList,
    system: [systemSession],
  };

  return { sessions, grouped };
}
