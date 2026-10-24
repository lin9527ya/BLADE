"use client";

import * as React from "react";

import type { Finding, FindingAssetNode, FindingGroup, FindingStats } from "@/lib/types";

import { UNASSIGNED_TASK } from "./findings-table";

// 发现页的类型与常量：视图档位、平铺/分组/资产树三种状态、空值哨兵。

export const FINDING_LIST_PREFERENCE_KEY = "blade_finding_list_preferences";

// 列表视图:flat = 跨任务平铺大表(默认);grouped = 按任务分组折叠;
// asset = 左侧资产树 + 右侧该子树下的发现。
export type FindingView = "flat" | "grouped" | "asset";

export const FINDING_VIEWS: FindingView[] = ["flat", "grouped", "asset"];

// 资产树的一次性快照。与另外两个视图不同,资产视图不轮询:进入视图、改筛选、
// 或本页改动了发现之后才重新查询。
export interface AssetTreeState {
  nodes: FindingAssetNode[];
  findingTotal: number;
  truncated: boolean;
  droppedKinds: string[];
  loaded: boolean;
  loading: boolean;
}

export const EMPTY_ASSET_TREE: AssetTreeState = {
  nodes: [],
  findingTotal: 0,
  truncated: false,
  droppedKinds: [],
  loaded: false,
  loading: false,
};

// 分组视图里每个已展开任务组自带一份分页状态,彼此独立。
export interface GroupFindingsState {
  items: Finding[];
  total: number;
  page: number;
  pageSize: number;
  loaded: boolean;
  loading: boolean;
}

// 平铺视图的页码单独放 state(而非塞进快照),筛选一变就能连带重置并触发重新加载。
export interface FlatFindingsState {
  items: Finding[];
  total: number;
  loaded: boolean;
  loading: boolean;
  /** 最近一次拉取失败的原因；成功后清空。用于把「加载失败」与「确实没有」分开。 */
  error?: string;
}

export const EMPTY_FLAT_STATE: FlatFindingsState = { items: [], total: 0, loaded: false, loading: false, error: "" };

export function findingGroupKey(group: FindingGroup) {
  return group.task_id === null ? UNASSIGNED_TASK : String(group.task_id);
}

export const EMPTY_STATS: FindingStats = {
  total: 0,
  pending: 0,
  critical: 0,
  high: 0,
  medium: 0,
  low: 0,
  vulnclasses: [],
  tasks: [],
};
