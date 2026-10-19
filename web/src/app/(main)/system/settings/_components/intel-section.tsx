"use client";

import * as React from "react";

import Link from "next/link";

import { ChevronRightIcon, RadarIcon } from "lucide-react";

import { Advanced, Field, Section, SubBlock } from "@/components/page-sections";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 情报来源。

export function IntelligenceSection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const {
    loaded,
    intelKeyInput,
    setIntelKeyInput,
    intelKeySet,
    intelEmail,
    setIntelEmail,
    intelBaseURL,
    setIntelBaseURL,
    intelProxy,
    setIntelProxy,
    intelQuota,
    setIntelQuota,
    intelUsed,
    intelBusy,
    intelTesting,
    intelTestMsg,
    intelRows,
    saveIntel,
    clearIntelKey,
    testIntel,
  } = s;

  return (
    <>
      <Section title="情报来源" hint="测绘引擎配置。每次调用现读，改完立即生效（不影响运行中的任务）。">
        <SubBlock
          title="情报源（FOFA）"
          icon={<RadarIcon className="text-muted-foreground size-4" />}
          actions={
            <>
              <Button type="button" size="sm" onClick={() => void saveIntel()} disabled={!loaded || intelBusy}>
                保存
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => void testIntel()}
                disabled={!loaded || intelTesting}
              >
                {intelTesting ? "测试中…" : "测试连接"}
              </Button>
              {intelKeySet && (
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button type="button" size="sm" variant="outline" disabled={intelBusy}>
                      清除 Key
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>清除 FOFA API Key？</AlertDialogTitle>
                      <AlertDialogDescription>
                        清除后情报查询工具（intel_search）会提示未配置，Agent 不再能自己查资产；
                        已经登记进资产库的资产与查询台账不受影响。
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>取消</AlertDialogCancel>
                      <AlertDialogAction onClick={() => void clearIntelKey()}>清除</AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              )}
            </>
          }
        >
          {intelTestMsg && <p className="text-muted-foreground text-xs">{intelTestMsg}</p>}

          <Field
            label={<>API Key{intelKeySet ? "（已配置）" : "（未配置）"}</>}
            htmlFor="fofa-key"
            hint={
              <>
                给 Agent 一个「自己去查互联网资产」的工具（当前接入 <b>FOFA</b>）。查到的东西
                <b>只进回执、不入库、不自动纳入测试范围</b>——要不要登记成资产（insert_assets）、要不要纳入本任务范围
                （add_task_scope）由 Agent 或人显式决定，资产拦截规则照常生效。
                <br />
                每次真实查询都会记入台账并占用<b>平台侧每日上限</b>（FOFA
                自己的额度之外再加一道闸，防模型一天把额度刷光）。 Key 只存不回显，这里只显示「是否已配置」；输入框留空
                = 不修改。
              </>
            }
          >
            <Input
              id="fofa-key"
              type="password"
              autoComplete="off"
              className="max-w-xl"
              placeholder={intelKeySet ? "已配置 · 留空表示不修改" : "填入 FOFA API Key"}
              value={intelKeyInput}
              onChange={(e) => setIntelKeyInput(e.target.value)}
              disabled={!loaded || intelBusy}
            />
          </Field>

          <Advanced title="高级：端点、代理与配额">
            <div className="grid gap-4 md:grid-cols-2">
              <Field label="FOFA 账号邮箱（老账号需要，新账号留空）" htmlFor="fofa-email">
                <Input
                  id="fofa-email"
                  autoComplete="off"
                  placeholder="可选"
                  value={intelEmail}
                  onChange={(e) => setIntelEmail(e.target.value)}
                  disabled={!loaded || intelBusy}
                />
              </Field>
              <Field label="API 端点（企业版/自建可改，留空用官方）" htmlFor="fofa-base">
                <Input
                  id="fofa-base"
                  autoComplete="off"
                  placeholder="https://fofa.info"
                  value={intelBaseURL}
                  onChange={(e) => setIntelBaseURL(e.target.value)}
                  disabled={!loaded || intelBusy}
                />
              </Field>
              <Field label="独立出口代理（留空跟随全局代理）" htmlFor="fofa-proxy">
                <Input
                  id="fofa-proxy"
                  autoComplete="off"
                  placeholder="http://127.0.0.1:7897"
                  value={intelProxy}
                  onChange={(e) => setIntelProxy(e.target.value)}
                  disabled={!loaded || intelBusy}
                />
              </Field>
              <Field label={<>平台侧每日上限（0 = 不限；今日已用 {intelUsed} 次）</>} htmlFor="intel-quota">
                <Input
                  id="intel-quota"
                  inputMode="numeric"
                  autoComplete="off"
                  value={intelQuota}
                  onChange={(e) => setIntelQuota(e.target.value.replace(/[^0-9]/g, ""))}
                  disabled={!loaded || intelBusy}
                />
              </Field>
            </div>
          </Advanced>

          {intelRows.length > 0 && (
            <Collapsible>
              <CollapsibleTrigger className="group text-muted-foreground hover:text-foreground flex items-center gap-1.5 text-xs font-medium">
                <ChevronRightIcon className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
                最近查询台账（{intelRows.length} 条，审计与排障）
              </CollapsibleTrigger>
              <CollapsibleContent className="mt-3 flex flex-col gap-1">
                {intelRows.slice(0, 5).map((q) => (
                  <div key={q.id} className="flex flex-wrap items-center gap-2 text-xs">
                    <span className="text-muted-foreground tabular-nums">
                      {new Date(q.created_at).toLocaleString("zh-CN", { hour12: false })}
                    </span>
                    <code className="bg-muted max-w-80 truncate rounded px-1">{q.expr}</code>
                    <span className={q.error ? "text-red-500" : "text-muted-foreground"}>
                      {q.error ? `失败：${q.error}` : `命中 ${q.total_hits} 条 · 取回 ${q.returned} 条`}
                    </span>
                    {q.task_id && (
                      <Link
                        href={`/function/tasks/detail?id=${encodeURIComponent(q.task_id)}`}
                        className="hover:underline"
                      >
                        任务 #{q.task_id}
                      </Link>
                    )}
                  </div>
                ))}
              </CollapsibleContent>
            </Collapsible>
          )}
        </SubBlock>
      </Section>
    </>
  );
}
