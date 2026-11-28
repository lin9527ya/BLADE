"use client";

import { TasksWorkbench } from "../tasks/_components/tasks-workbench";

// 兼容别名：计划任务已整合进任务页「计划任务」页签，保留旧路由直达。
export default function SchedulesPage() {
  return <TasksWorkbench initialTab="schedules" />;
}
