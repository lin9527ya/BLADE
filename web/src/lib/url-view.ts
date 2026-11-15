"use client";

import * as React from "react";

// 列表页的筛选/排序状态同步到地址栏：刷新、复制链接、浏览器前进/后退都能回到同一视图。
// 这是一个**渐进增强**的 hook——页面仍以本地 state 为准，URL 只负责初始化与回写。
export type UrlViewValue = string | number | boolean | null | undefined;
export type UrlViewValues = Record<string, UrlViewValue>;
export type UrlViewDefaults = Record<string, UrlViewValue>;

// 文本/分页类变化用 replaceState（避免每敲一个字就多一条历史），
// 离散筛选/视图变化用 pushState（后退能回到上一个筛选）。
const REPLACE_KEYS = new Set(["q", "query", "search", "page", "size", "pageSize"]);

export function readUrlView(defaults: UrlViewDefaults): Partial<Record<string, string>> {
  if (typeof window === "undefined") return {};
  const params = new URLSearchParams(window.location.search);
  const out: Record<string, string> = {};
  for (const key of Object.keys(defaults)) {
    if (params.has(key)) out[key] = params.get(key) ?? "";
  }
  return out;
}

function viewKey(values: UrlViewValues) {
  return JSON.stringify(values);
}

// useUrlViewSync 把页面状态写回地址栏。hydrated=false 时不动 URL；首次水合只记录基线。
export function useUrlViewSync(values: UrlViewValues, defaults: UrlViewDefaults, hydrated: boolean) {
  const prev = React.useRef<UrlViewValues | null>(null);
  const valuesKey = viewKey(values);
  const defaultsKey = viewKey(defaults);

  React.useEffect(() => {
    if (!hydrated || typeof window === "undefined") return;
    const current = JSON.parse(valuesKey) as UrlViewValues;
    const base = JSON.parse(defaultsKey) as UrlViewDefaults;
    const before = prev.current;
    prev.current = current;
    if (before === null) return;

    const changed = Object.keys(base).filter((key) => String(before[key] ?? "") !== String(current[key] ?? ""));
    if (changed.length === 0) return;

    const params = new URLSearchParams(window.location.search);
    for (const key of Object.keys(base)) {
      const value = current[key];
      if (value === undefined || value === null || value === "" || String(value) === String(base[key])) {
        params.delete(key);
      } else {
        params.set(key, String(value));
      }
    }
    const qs = params.toString();
    const next = `${window.location.pathname}${qs ? `?${qs}` : ""}${window.location.hash}`;
    const now = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    if (next === now) return;
    const push = changed.some((key) => !REPLACE_KEYS.has(key));
    window.history[push ? "pushState" : "replaceState"](window.history.state, "", next);
  }, [defaultsKey, hydrated, valuesKey]);
}

// useUrlViewPopState 在浏览器前进/后退时重新读地址栏。回调应只做状态赋值。
export function useUrlViewPopState(onPop: () => void) {
  const handler = React.useRef(onPop);
  handler.current = onPop;
  React.useEffect(() => {
    const listener = () => handler.current();
    window.addEventListener("popstate", listener);
    return () => window.removeEventListener("popstate", listener);
  }, []);
}

export function urlViewString(raw: Partial<Record<string, string>>, key: string, fallback = ""): string {
  return raw[key] ?? fallback;
}

export function urlViewInt(raw: Partial<Record<string, string>>, key: string, fallback: number): number {
  const value = Number(raw[key]);
  return Number.isFinite(value) ? value : fallback;
}
