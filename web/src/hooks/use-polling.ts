"use client";

import { useEffect, useRef } from "react";

// 统一的轮询。收敛此前散在 20 多处、各写各的 `load(); setInterval(load, N); clearInterval`。
//
// 相比手写 setInterval，这里有四处实质差别：
//
//  1. **不可见时暂停**：标签页在后台时不再每 3 秒敲一次后端。前台轮询里最不值当的
//     开销就是这个——用户根本看不见结果。
//  2. **回到前台立刻补一次**：不等一个完整间隔，用户切回来第一眼就是最新数据。
//  3. **不重叠**：上一次还没回来就不发下一次。setInterval 在接口变慢时会堆积请求，
//     越慢堆得越多。
//  4. **失败退避**：连续失败按 2× 翻倍到上限，后端挂了也不会被每 3 秒敲一次；一次
//     成功就回到正常间隔。
//
// 回调收到一个 `alive()`：组件卸载（或依赖变化重建轮询）后返回 false，用它拦住
// 迟到的响应去 setState——这正是各页面原先各自维护的 `let cancelled` 标志。

/** 连续失败时的最大退避倍数（相对基础间隔）。 */
export const POLL_MAX_BACKOFF = 8;

/** nextDelay 算下一次该等多久：成功回到基础间隔，失败翻倍退避到上限。 */
export function nextDelay(base: number, current: number, ok: boolean): number {
  return ok ? base : Math.min(current * 2, base * POLL_MAX_BACKOFF);
}

export type PollRun = (alive: () => boolean) => void | Promise<void>;

function hidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

/**
 * usePolling 按 intervalMs 反复调用 run，首次立刻执行。
 * deps 与 useEffect 同义，**长度必须稳定**（调用方传字面量数组即可）。
 *
 * intervalMs <= 0 = **只跑一次不重复**（参数变化时拉一次的场景，如"非实时模式"）。
 *
 * 注意：回调是**每次渲染新建**的（hook 内部用 ref 取最新），所以跨轮询要留存的状态
 * 不能写成回调里的 `let x`——那会随渲染重置。这类状态请放组件级 useRef。
 */
export function usePolling(run: PollRun, intervalMs: number, deps: unknown[] = []): void {
  // 用 ref 转发回调：调用方不必为了稳定引用而包 useCallback。
  const runRef = useRef(run);
  runRef.current = run;

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let delay = intervalMs;
    let inFlight = false;

    const alive = () => !stopped;

    const once = intervalMs <= 0;

    const schedule = () => {
      if (stopped || once || hidden()) return; // 一次性 / 隐藏：都不排期
      timer = setTimeout(tick, delay);
    };

    const tick = async () => {
      if (stopped || inFlight) return;
      inFlight = true;
      let ok = true;
      try {
        await runRef.current(alive);
      } catch {
        ok = false; // 失败不往外抛：轮询错一次不该把页面打崩，交给退避处理
      } finally {
        inFlight = false;
      }
      delay = nextDelay(intervalMs, delay, ok);
      schedule();
    };

    const onVisibility = () => {
      if (stopped || once || inFlight || hidden()) return;
      if (timer) clearTimeout(timer);
      delay = intervalMs; // 回来时先清掉退避状态
      void tick();
    };

    void tick();
    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", onVisibility);
    }
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      if (typeof document !== "undefined") {
        document.removeEventListener("visibilitychange", onVisibility);
      }
    };
  }, [intervalMs, ...deps]);
}
