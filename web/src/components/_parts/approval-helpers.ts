"use client";

import * as React from "react";

import type { InterceptApprovalRow } from "@/lib/types";

// 审批页的共享小工具：时间/来源/命名格式化。
// 列表、详情与展示基件都要用，独立成模块以避免循环导入。

export function fmtTime(value?: string) {
  if (!value) return "—";
  return new Date(value).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function source(row: InterceptApprovalRow) {
  if (row.decision_source) return row.decision_source;
  if (row.rule_id) return "rule";
  return row.reason?.startsWith("[模型]") ? "model" : "unknown";
}

export function originLabel(row: InterceptApprovalRow) {
  if (row.task_id) return row.task_id;
  if (row.conversation_id) return `对话 #${row.conversation_id}`;
  return "—";
}
