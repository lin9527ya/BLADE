"use client";

import * as React from "react";

import { Agent, Conversation } from "@/lib/types";

// 对话页的纯函数、常量与类型。

export function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

// fmtDuration renders an elapsed milliseconds span compactly (90s → 1m30s).
export function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

// HISTORY_PAGE is how many steps one history page loads: the latest page on open,
// then one more page each time the user scrolls to the top. Kept modest so a long
// thread stays snappy (only ~a page of rows is in the DOM until you scroll up).
export const HISTORY_PAGE = 200;
export const CONVERSATION_LIST_PAGE = 100;

// Which agent groups the user has collapsed in the left rail. Persisted so the
// rail looks the same after a reload; unknown keys are harmless (a deleted agent
// simply never renders a group again).
export const COLLAPSED_AGENTS_KEY = "blade.chat.collapsed-agents";

export function conversationIsPinned(conversation: Conversation): boolean {
  return conversation.pinned ?? Boolean(conversation.pinned_at);
}

// AgentGroup is one collapsible section of the left rail: all unpinned
// conversations of a single agent, newest activity first.
export interface AgentGroup {
  key: string;
  name: string;
  conversations: Conversation[];
  runningCount: number;
}

// groupByAgent buckets conversations by agent, preserving the incoming order
// both inside a group and across groups. The server already sorts by updated_at
// DESC, so first-appearance order == most-recently-active group first.
export function groupByAgent(conversations: Conversation[], agentByKey: Map<string, Agent>): AgentGroup[] {
  const groups = new Map<string, AgentGroup>();
  for (const conversation of conversations) {
    let group = groups.get(conversation.agent_key);
    if (!group) {
      group = {
        key: conversation.agent_key,
        name: agentByKey.get(conversation.agent_key)?.name || conversation.agent_key,
        conversations: [],
        runningCount: 0,
      };
      groups.set(conversation.agent_key, group);
    }
    group.conversations.push(conversation);
    if (conversation.running) group.runningCount++;
  }
  return [...groups.values()];
}

// LiveBadge is the small pulsing "实时" chip reused from the task's main-agent
// console — shown while a turn is streaming.
