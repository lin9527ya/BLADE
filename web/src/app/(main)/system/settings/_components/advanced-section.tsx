"use client";

import * as React from "react";

import { Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { CHAT_SEND_MODE_OPTIONS, setChatSendMode, type ChatSendMode } from "@/lib/chat-send-mode";
import { FlaskConicalIcon, KeyboardIcon } from "lucide-react";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 高级与界面。

export function AdvancedSection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const { loaded, savingPython, pyInterp, setPyInterp, noaCompaction, sendMode, savePython, detectPython, toggleNoaCompaction } = s;

  return (
    <>

          <Section title="高级与界面" hint="实验性机制、脚本运行时与浏览器端偏好。">
            <SubBlock title="实验功能" icon={<FlaskConicalIcon className="text-muted-foreground size-4" />}>
              <SwitchRow
                id="noa-compaction"
                label={<>noa 上下文压缩{noaCompaction ? " · 已开启" : " · 已关闭"}</>}
                checked={noaCompaction}
                disabled={!loaded}
                onChange={toggleNoaCompaction}
                hint={
                  <>
                    尚在验证中的机制，默认关闭。可能改变 Agent 行为或影响稳定性，请在了解影响后启用。
                    <br />
                    <b>noa 上下文压缩</b>：由模型主动压缩长对话历史（norma v0.4.0）。开启后平台接入的四类 Agent（
                    <b>规划者 / 执行者 / 主 Agent / 对话</b>）改用 noa 接管上下文，取代内置压缩，压缩原文会归档到任务工作目录下便于回溯。
                    切换即时生效（对之后启动的运行生效），无需重建 Agent；关闭后立即恢复内置压缩。
                    <br />
                    <b>注意（与 P14 领域化压缩互斥）</b>：默认走的是「领域化压缩 + 常驻索引」（7
                    段渗透领域摘要、压缩前原文落盘可回读、每轮附目标/约束/活跃方向索引）。
                    开启 noa 后这一层整体让位——两者抢同一个上下文管理器槽位，不会叠加。
                  </>
                }
              />
            </SubBlock>

            <SubBlock title="自定义脚本 · Python 解释器">
              <Field
                label="解释器路径"
                htmlFor="python-interpreter"
                hint="自定义 script 类型工具用它跑 Python。开机会自动检测（python3 优先）；此处可手填 venv / 特定版本的绝对路径，留空则运行时自动检测。"
              >
                <div className="flex items-center gap-2">
                  <Input
                    id="python-interpreter"
                    className="max-w-xl font-mono text-sm"
                    placeholder="/usr/bin/python3（留空=自动检测）"
                    value={pyInterp}
                    disabled={!loaded || savingPython}
                    onChange={(e) => setPyInterp(e.target.value)}
                  />
                  <Button variant="outline" onClick={detectPython} disabled={!loaded || savingPython}>
                    重新检测
                  </Button>
                  <Button onClick={savePython} disabled={!loaded || savingPython}>
                    保存
                  </Button>
                </div>
              </Field>
            </SubBlock>

            <SubBlock title="会话输入框发送键位" icon={<KeyboardIcon className="text-muted-foreground size-4" />}>
              <Field
                label="发送方式"
                htmlFor="chat-send-mode"
                hint={
                  <>
                    对话页与任务详情的主 Agent 会话输入框共用此设置，选择后立即生效、无需保存。
                    <br />
                    该偏好<b>只存在本浏览器</b>，不随账号同步，换浏览器或清理站点数据后需重新设置。
                  </>
                }
              >
                <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
                  <SelectTrigger id="chat-send-mode" className="w-72">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {CHAT_SEND_MODE_OPTIONS.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </SubBlock>
          </Section>
        
    </>
  );
}
