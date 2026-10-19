"use client";

import * as React from "react";

import Link from "next/link";

import { OctagonXIcon, ShieldAlertIcon } from "lucide-react";

import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { ScopeEnforcement } from "@/lib/types";

// 平台级"停"的状态条（P35）。
//
// 为什么要有它：停机开关与作业时间窗到点都会把任务全停掉，而"任务都不动了"从界面上看
// 和"平台坏了"长得一模一样。这一条把原因摆在最显眼处，并直接给出去哪解开。
//
// 不可关闭：它是状态，不是通知。轮询间隔取 30 秒——停机是人的动作，不需要秒级同步。

const POLL_MS = 30000;

export function SafetyBanner() {
  const [st, setSt] = React.useState<ScopeEnforcement | null>(null);

  usePolling(
    async (alive) => {
      const load = () => {
        api
          .scopeEnforcement()
          .then((d) => {
            if (alive()) setSt(d);
          })
          .catch(() => {
            /* 读不到就不显示：这条是提示，不该自己变成错误源 */
          });
      };
      load();
    },
    POLL_MS,
    [],
  );

  if (!st) return null;

  const killed = st.kill_switch.on;
  const outsideWindow = !!st.window.window && !st.window.in_window;
  if (!killed && !outsideWindow && !st.window.parse_error) return null;

  const tone = killed
    ? "border-destructive/40 bg-destructive/10 text-destructive"
    : "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400";

  return (
    <div className={`flex items-start gap-2 border-b px-4 py-2 text-xs lg:px-6 ${tone}`}>
      {killed ? (
        <OctagonXIcon className="mt-0.5 size-3.5 shrink-0" />
      ) : (
        <ShieldAlertIcon className="mt-0.5 size-3.5 shrink-0" />
      )}
      <div className="min-w-0 flex-1">
        {killed ? (
          <span>
            <b>停机开关已启用</b>
            {st.kill_switch.reason ? `（理由：${st.kill_switch.reason}）` : ""}：所有任务已暂停、动作一律拒绝
            {st.kill_switch_forced_by_env ? "；环境变量 BLADE_KILL_SWITCH 强制生效中，页面解除不了" : ""}。
          </span>
        ) : st.window.parse_error ? (
          <span>
            <b>作业时间窗设置解析失败</b>（{st.window.parse_error}）：每次动作都会转人工审批，请到设置里改对。
          </span>
        ) : (
          <span>
            <b>当前不在授权作业时间窗内</b>（允许 {st.window.window}）：平台已停止发起动作
            {st.window.paused_now > 0 ? `，本轮暂停了 ${st.window.paused_now} 个任务` : ""}。
          </span>
        )}
        {st.paused_tasks > 0 && <span className="ml-1 opacity-80">当前暂停中：{st.paused_tasks} 个任务。</span>}
      </div>
      <Link href="/system/settings" className="shrink-0 font-medium underline underline-offset-2">
        去处理
      </Link>
    </div>
  );
}
