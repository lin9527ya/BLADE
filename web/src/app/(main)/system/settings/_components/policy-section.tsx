"use client";

import * as React from "react";

import Link from "next/link";

import { EyeOffIcon, LanguagesIcon, OctagonXIcon, RadarIcon, ScanSearchIcon, ShieldAlertIcon } from "lucide-react";

import { CustomRulesEditor } from "@/components/custom-rules-editor";
import { Advanced, Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { InterceptRule } from "@/lib/types";

import type { useSettingsState } from "./use-settings-state";
import { exposureWeight } from "./use-settings-state";

// 系统配置 · 安全与策略。

export function PolicySection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const {
    loaded,
    polDefault,
    polTech,
    setPolTech,
    polWrites,
    setPolWrites,
    polCustom,
    setPolCustom,
    savingPol,
    injectPlanner,
    injectWorker,
    scopeMode,
    setScopeMode,
    scopeModeEnv,
    scopeHours,
    setScopeHours,
    savingScope,
    artifactLang,
    savingArtifactLang,
    credBudget,
    setCredBudget,
    credBudgetAction,
    setCredBudgetAction,
    savingCredBudget,
    siteName,
    setSiteName,
    savingSiteName,
    corsOrigins,
    setCorsOrigins,
    savingCors,
    saveCors,
    agentLootExec,
    savingLootExec,
    toggleAgentLootExec,
    agentAutoRetest,
    savingAutoRetest,
    toggleAgentAutoRetest,
    oastEnabled,
    oastTokenSet,
    noiseLevel,
    saveNoiseLevel,
    oastTokenInput,
    setOastTokenInput,
    oastDomain,
    setOastDomain,
    savingOast,
    toggleOast,
    saveOast,
    clearOastToken,
    enforce,
    killReason,
    setKillReason,
    killBusy,
    opsecKB,
    resumeBusy,
    savePolicyDefault,
    togglePolicyChip,
    toggleInjectPlanner,
    saveScope,
    saveArtifactLang,
    saveCredBudget,
    saveSiteName,
    doKillSwitch,
    doResumeAll,
    toggleInjectWorker,
  } = s;

  return (
    <>
      <Section
        title="安全与策略"
        hint="授权范围改完即时生效；探索策略只影响之后新建的任务（创建时抄一份快照）；操作约束注入下一轮读取即生效。"
      >
        <SafetyBoundarySummary s={s} />
        <SubBlock
          title="授权范围（RoE）"
          icon={<ShieldAlertIcon className="text-muted-foreground size-4" />}
          actions={
            <Button type="button" size="sm" onClick={saveScope} disabled={!loaded || savingScope}>
              保存
            </Button>
          }
        >
          <p className="text-muted-foreground text-xs leading-relaxed">
            授权边界的两个维度：<b>能打哪些目标</b>（目标越出任务的 task_scope 时怎么处置）与
            <b>什么时候能打</b>（作业时间窗）。两者都由拦截层在每次工具调用前判定，<b>改完即时生效</b>
            ；时间检查排在目标检查<b>之前</b>——窗口之外根本不该发起动作，与目标在不在范围内无关。
          </p>
          <div className="grid gap-4 md:grid-cols-2">
            <Field label="目标越界时" htmlFor="scope-mode">
              <Select value={scopeMode} onValueChange={setScopeMode} disabled={!loaded || savingScope || scopeModeEnv}>
                <SelectTrigger id="scope-mode" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="off">off —— 不检查，越界静默放行</SelectItem>
                  <SelectItem value="ask">ask —— 越界先交给人审批（默认）</SelectItem>
                  <SelectItem value="deny">deny —— 越界直接拒绝</SelectItem>
                </SelectContent>
              </Select>
              {scopeModeEnv && (
                <p className="text-muted-foreground text-xs">
                  环境变量 BLADE_SCOPE_ENFORCEMENT 正压着这项设置，页面改不动它。
                </p>
              )}
            </Field>
            <Field label="作业时间窗（留空 = 不限时）" htmlFor="scope-hours">
              <Input
                id="scope-hours"
                autoComplete="off"
                placeholder="09:00-18:00；跨夜 22:00-06:00；多段用逗号"
                value={scopeHours}
                onChange={(e) => setScopeHours(e.target.value)}
                disabled={!loaded || savingScope}
              />
            </Field>
          </div>
          <p className="text-muted-foreground text-xs leading-relaxed">
            时间窗支持多段（<code>09:00-12:00,14:00-18:00</code>）与跨夜（<code>22:00-06:00</code>= 当天 22:00 到次日
            06:00）；区间左闭右开，"到 18:00"不含 18:00 那一刻起的动作。
            <b>写错会被拒绝保存</b>——一条解析不了的窗口会让之后每次调用都落到人工确认上，
            不如当场退回。时区取平台所在机器的本地时间。
          </p>
          <p className="text-muted-foreground text-xs leading-relaxed">
            配了窗口就是<b>硬边界</b>：窗口外不发动作（与上面那个模式无关，off 也照样拦）， 到点平台会
            <b>暂停所有在跑任务</b>并取消在跑的长任务；窗口重开<b>不会自动恢复</b>，
            要在这里点「继续全部暂停任务」。没配窗口 = 不限时。
          </p>
          {enforce && enforce.window.window !== "" && (
            <div className="flex flex-wrap items-center gap-2 rounded-md border p-2 text-xs">
              <Badge variant={enforce.window.in_window ? "secondary" : "destructive"}>
                {enforce.window.in_window ? "当前在窗口内" : "当前在窗口外"}
              </Badge>
              <span className="text-muted-foreground font-mono">{enforce.window.window}</span>
              {enforce.window.last_closed_at && (
                <span className="text-muted-foreground">
                  上次关闭：{new Date(enforce.window.last_closed_at).toLocaleString("zh-CN")}
                </span>
              )}
              {enforce.window.paused_now > 0 && (
                <span className="text-muted-foreground">本轮因窗口暂停 {enforce.window.paused_now} 个任务</span>
              )}
            </div>
          )}
          {enforce?.window.parse_error && (
            <p className="text-destructive text-xs leading-relaxed">窗口设置解析失败：{enforce.window.parse_error}</p>
          )}
        </SubBlock>

        <SubBlock
          title="停机开关（紧急停止）"
          icon={<OctagonXIcon className="text-muted-foreground size-4" />}
          actions={
            enforce?.kill_switch.on ? (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => doKillSwitch(false)}
                disabled={killBusy || enforce.kill_switch_forced_by_env}
              >
                解除停机
              </Button>
            ) : (
              <Button
                type="button"
                size="sm"
                variant="destructive"
                onClick={() => doKillSwitch(true)}
                disabled={killBusy || enforce?.kill_switch_forced_by_env}
              >
                <OctagonXIcon data-icon="inline-start" /> 立即停机
              </Button>
            )
          }
        >
          <p className="text-muted-foreground text-xs leading-relaxed">
            一句话：<b>现在全停</b>。启用后立刻暂停所有在跑任务、终止在跑的长任务、拒绝一切动作类工具调用，
            并停止补位排队任务（排队任务保持排队，位置不丢）。<b>解除后不自动恢复任务</b>——恢复意味着平台
            重新对目标动手，那一下该由人按下。
          </p>
          {enforce?.kill_switch_forced_by_env && (
            <p className="text-destructive text-xs leading-relaxed">
              环境变量 BLADE_KILL_SWITCH 正强制停机，页面解除不了：要恢复请去掉该变量并重启平台。
            </p>
          )}
          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-64 flex-1">
              <Field label="停机理由（记进回执）" htmlFor="kill-reason">
                <Input
                  id="kill-reason"
                  autoComplete="off"
                  placeholder="例：客户来电叫停 / 打错了目标 / 平台维护"
                  value={killReason}
                  onChange={(e) => setKillReason(e.target.value)}
                  disabled={killBusy || !!enforce?.kill_switch.on}
                />
              </Field>
            </div>
            <Button
              type="button"
              variant="secondary"
              onClick={doResumeAll}
              disabled={resumeBusy || !enforce || enforce.paused_tasks === 0}
            >
              继续全部暂停任务{enforce && enforce.paused_tasks > 0 ? `（${enforce.paused_tasks}）` : ""}
            </Button>
          </div>
          {enforce?.kill_switch.at && (
            <div className="text-muted-foreground space-y-1 text-xs">
              <p>
                最近一次动作：
                <b>{enforce.kill_switch.on ? "启用停机" : "解除停机"}</b>
                {` · ${new Date(enforce.kill_switch.at).toLocaleString("zh-CN")}`}
                {enforce.kill_switch.by ? ` · ${enforce.kill_switch.by}` : ""}
                {enforce.kill_switch.reason ? ` · 理由：${enforce.kill_switch.reason}` : ""}
                {enforce.kill_switch.tasks_paused ? ` · 暂停 ${enforce.kill_switch.tasks_paused} 个任务` : ""}
              </p>
              {(enforce.kill_switch.history?.length ?? 0) > 1 && (
                <ul className="space-y-0.5">
                  {enforce.kill_switch.history?.slice(1, 6).map((h) => (
                    <li key={h.at + String(h.on)}>
                      {h.on ? "启用" : "解除"} · {new Date(h.at).toLocaleString("zh-CN")}
                      {h.by ? ` · ${h.by}` : ""}
                      {h.reason ? ` · ${h.reason}` : ""}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </SubBlock>

        <SubBlock
          title="对外显示名"
          icon={<EyeOffIcon className="text-muted-foreground size-4" />}
          actions={
            <Button type="button" size="sm" onClick={saveSiteName} disabled={!loaded || savingSiteName}>
              保存
            </Button>
          }
        >
          <p className="text-muted-foreground text-xs leading-relaxed">
            界面与浏览器标签上显示的名字（留空 = 内置名）。名字本身是**低成本指纹**——
            被扫到页面时，一个暴露用途的名字会让对方立刻知道这是什么、值不值得继续。 在意隐身就填个中性名。
          </p>
          <div className="max-w-md">
            <Field label="显示名" htmlFor="site-name" hint="留空 = 内置名">
              <Input
                id="site-name"
                autoComplete="off"
                placeholder="例如 Monitor / Console（留空用内置名）"
                value={siteName}
                onChange={(e) => setSiteName(e.target.value)}
                disabled={!loaded || savingSiteName}
              />
            </Field>
          </div>
          <p className="text-muted-foreground text-xs leading-relaxed">
            只管**运行时**标题；爬虫抓到的静态 HTML 里仍是构建期的名字（
            <code className="text-[11px]">web/src/config/app-config.ts</code>），
            要连它一起换掉得改那里重新构建——部署文档里有写。
          </p>
        </SubBlock>

        <SubBlock
          title="跨源访问白名单"
          icon={<RadarIcon className="text-muted-foreground size-4" />}
          actions={
            <Button type="button" size="sm" onClick={saveCors} disabled={!loaded || savingCors}>
              保存
            </Button>
          }
        >
          <p className="text-muted-foreground text-xs leading-relaxed">
            默认不返回 CORS 头：只有本站在浏览器里访问 API。需要从别的来源（内部面板、独立脚本页）直接调
            API 时，把来源列在这里（逗号分隔，例如{" "}
            <code className="text-[11px]">https://ops.example.com,http://127.0.0.1:3000</code>）。
            <br />
            <b>填 * 等于对任何网站开放</b>——浏览器里的任意页面都能带着你的登录态调接口，
            除非确实必要，不要这么填。配置有 60 秒缓存，保存后不一定立刻生效。
          </p>
          <div className="max-w-xl">
            <Field label="来源白名单（逗号分隔，空 = 不开放）" htmlFor="cors-origins">
              <Input
                id="cors-origins"
                autoComplete="off"
                placeholder="https://ops.example.com"
                value={corsOrigins}
                onChange={(e) => setCorsOrigins(e.target.value)}
                disabled={!loaded || savingCors}
              />
            </Field>
          </div>
        </SubBlock>

        <SubBlock title="产物语言（面向目标）" icon={<LanguagesIcon className="text-muted-foreground size-4" />}>
          <p className="text-muted-foreground text-xs leading-relaxed">
            **发给目标的东西**用什么语言：命令与参数、payload、落地文件名、脚本注释。
            它和「界面语言」是两件事——界面是你自己看的，产物是<strong>目标与它的日志</strong>能看到的，
            所以刻意分开设置（否则切个界面会顺手改掉出站内容）。
          </p>
          <div className="flex flex-wrap items-end gap-3">
            <Field label="产物语言" htmlFor="artifact-language">
              <Select
                value={artifactLang}
                onValueChange={(v) => {
                  if (!loaded || savingArtifactLang) return;
                  if (v === "zh" || v === "en") saveArtifactLang(v);
                }}
                disabled={!loaded || savingArtifactLang}
              >
                <SelectTrigger id="artifact-language" className="w-80">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="zh">中文（不约束，默认）</SelectItem>
                  <SelectItem value="en">英文（面向目标的产物一律 ASCII）</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <p className="text-muted-foreground text-xs leading-relaxed">
            <strong>中文不是错</strong>：打中文站点时，中文的探针参数、中文用户名枚举本来就该用中文。 但中文是
            <strong>最省事的归类线索</strong>——一条带汉字参数的命令在日志里很容易被读成"外部测试方"。
            默认不约束；切到英文后，规约随系统提示词下发（只能改设置，改不掉那段规约），
            模型确需中文时会照用并在意图里说明理由。
            出站命令里出现汉字时，审批卡上会多一条「语言痕迹」的可检出性笔记（只标注，不拦）。
          </p>
        </SubBlock>

        <SubBlock title="凭据尝试预算（后渗透）" icon={<ShieldAlertIcon className="size-4 text-muted-foreground" />}>
          <p className="text-muted-foreground text-xs leading-relaxed">
            后渗透阶段最容易伤到客户的一件事是<strong>账号锁定</strong>：对着 AD 试几百个口令，
            锁掉的是客户的业务账号。平台在动作发起前识别凭据尝试（hydra / nxc / sshpass / mysql / curl -u / impacket /
            evil-winrm / xfreerdp 等），按
            <strong>目标 + 账号</strong>在 30 分钟窗口内计数。
          </p>
          <p className="text-muted-foreground text-xs leading-relaxed">
            两条口径要看清：<strong>计的是尝试次数，不是失败次数</strong>——shell 层分不出成败，
            宁可多问一句（误报的代价是人工点一下，漏报的代价是账号被锁）；
            <strong>成批尝试不计数</strong>——`hydra -P rockyou.txt` 是一次调用、几百次真实尝试，
            按次数预估没有意义，它单独就构成越界，直接进裁决。
          </p>
          <div className="flex flex-wrap items-end gap-3">
            <Field label="窗口内允许的尝试次数" htmlFor="cred-budget" hint="0 = 不限（靶场/本地自用）。默认 3。">
              <Input
                id="cred-budget"
                type="number"
                min={0}
                max={1000}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={credBudget}
                disabled={!loaded || savingCredBudget}
                onChange={(e) => setCredBudget(e.target.value)}
              />
            </Field>
            <Field label="超预算后" htmlFor="cred-budget-action">
              <Select
                value={credBudgetAction}
                onValueChange={(v) => {
                  if (!loaded || savingCredBudget) return;
                  if (v === "allow" || v === "ask" || v === "deny") setCredBudgetAction(v);
                }}
                disabled={!loaded || savingCredBudget}
              >
                <SelectTrigger id="cred-budget-action" className="w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ask">转人工审批（默认）</SelectItem>
                  <SelectItem value="deny">直接拒绝</SelectItem>
                  <SelectItem value="allow">放行（只记账不拦）</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Button onClick={saveCredBudget} disabled={!loaded || savingCredBudget}>
              保存
            </Button>
          </div>
          <p className="text-muted-foreground text-xs leading-relaxed">
            默认<strong>转人工审批</strong>而不是直接拒绝：人一看就知道"这是靶场"或"这个账号没有锁定策略"，
            而平台不知道。每次尝试都会进台账（无论拦不拦），报告里会列出<strong>动过哪个账号、试了几次</strong>
            ——那是客户复盘时要核对的东西。它保护的是目标的可用性，与内置规则拒 <code>rm -rf /</code>、
            <code>DROP TABLE</code> 是同一类。
          </p>
        </SubBlock>

        <SubBlock title="战果直接执行（loot_exec）" icon={<ShieldAlertIcon className="size-4 text-muted-foreground" />}>
          <SwitchRow
            id="agent-loot-exec"
            describedBy="agent-loot-exec-description"
            label={agentLootExec ? "已开启 · Agent 可用凭据 / 据点 / 隧道直连目标" : "已关闭（默认）· 模型需自己用 Bash 构造连接"}
            checked={agentLootExec}
            disabled={!loaded || savingLootExec}
            onChange={toggleAgentLootExec}
            hint={
              <>
                默认关闭。开启后，Agent 可以用一条<strong>已登记的凭据</strong>（口令 / 私钥）直接对目标执行命令（ssh
                / mysql），或经<strong>已登记结构化连法的据点</strong>执行（ssh / webshell），或经 ssh 据点开 SOCKS
                隧道访问内网，不必再把明文贴进命令行：
                明文经 <code>0600</code> 临时文件交给客户端，命令、执行台账与时间线里都不含明文，只记指纹。
                <br />
                它<strong>不改变授权边界</strong>——授权范围（RoE）、作业时间窗与停机开关照常生效，host 必须是凭据
                targets 里登记过的目标。关闭时该工具不出现在 Agent 的工具面里。
                <br />
                对下一轮装配生效；已经发起的后台执行不受影响。
              </>
            }
          />
        </SubBlock>

        <SubBlock title="自动复测（结果质量）" icon={<ScanSearchIcon className="size-4 text-muted-foreground" />}>
          <SwitchRow
            id="agent-auto-retest"
            describedBy="agent-auto-retest-description"
            label={agentAutoRetest ? "已开启 · 每条新漏洞都会起一次独立复测" : "已关闭（默认）· 漏洞只在人工点「复测」时复核"}
            checked={agentAutoRetest}
            disabled={!loaded || savingAutoRetest}
            onChange={toggleAgentAutoRetest}
            hint={
              <>
                默认关闭。开启后，每收到一条新漏洞，平台会启动一个<strong>独立的复测会话</strong>（retester
                agent）去复核：用原 PoC 的关键条件做一次最小验证，把结论记成
                <strong>仍可复现 / 已修复 / 无法确认</strong>，并附本次的证据。
                <br />
                注意「已修复」是<strong>有副作用的结论</strong>：会话正常结束且结论为 fixed 时，平台会把该漏洞的处置状态
                自动改成「已修复」。
                <br />
                它能让报告里的漏洞多一道独立判断，但<strong>每条漏洞都会产生一次完整的 LLM 会话开销</strong>
                ——finding 多的任务成本会成倍上升。
                <br />
                前提是 <code>retester</code> agent 处于启用状态（系统 → Agent）；它被停用时本开关不起作用。
                对之后新收到的漏洞生效，已收到的不会补跑。
              </>
            }
          />
        </SubBlock>

        <SubBlock title="带外确认（OAST）" icon={<RadarIcon className="size-4 text-muted-foreground" />}>
          <SwitchRow
            id="oast-enabled"
            label={
              oastEnabled
                ? oastTokenSet
                  ? "已开启 · Agent 可生成回调地址并轮询命中"
                  : "已开启但未配置 token · 工具仍不可用"
                : "已关闭（默认）· 盲打类漏洞只能靠盲猜"
            }
            checked={oastEnabled}
            disabled={!loaded || savingOast}
            onChange={toggleOast}
            hint={
              <>
                盲打类漏洞（SSRF / 命令注入 / 盲 XSS / 反序列化）在目标侧没有回显，唯一可靠的确认是
                <strong>让它回连一个我们能看到的地址</strong>。开启后 Agent 会得到 oast_mint / oast_poll：
                生成带任务标记的回调地址、轮询命中并自动记成事实（带来源 IP 与时间）。
                <br />
                用外部托管服务（requestrepo），<strong>BLADE 不开任何入站端口</strong>；token 只存不回显。
                回调只能证明「目标访问了该地址」，不能证明是哪段代码执行的——结论里要如实写。
              </>
            }
          />
          <div className="grid gap-3 md:grid-cols-[1fr_200px_auto_auto]">
            <Field
              label="服务 token"
              htmlFor="oast-token"
              hint={oastTokenSet ? "已配置；输入新值可覆盖。" : "从 requestrepo 复制的会话 token（只存不回显）。"}
            >
              <Input
                id="oast-token"
                type="password"
                autoComplete="off"
                placeholder={oastTokenSet ? "已配置（输入新值可覆盖）" : "粘贴会话 token"}
                value={oastTokenInput}
                disabled={!loaded || savingOast}
                onChange={(e) => setOastTokenInput(e.target.value)}
              />
            </Field>
            <Field label="服务域名" htmlFor="oast-domain" hint="默认 requestrepo.com。">
              <Input
                id="oast-domain"
                autoComplete="off"
                placeholder="requestrepo.com"
                value={oastDomain}
                disabled={!loaded || savingOast}
                onChange={(e) => setOastDomain(e.target.value)}
              />
            </Field>
            <div className="flex items-end">
              <Button type="button" onClick={saveOast} disabled={!loaded || savingOast}>
                保存
              </Button>
            </div>
            <div className="flex items-end">
              <Button
                type="button"
                variant="outline"
                onClick={clearOastToken}
                disabled={!loaded || savingOast || !oastTokenSet}
              >
                清除 token
              </Button>
            </div>
          </div>
        </SubBlock>

        <SubBlock title="潜行模式（噪声档位）" icon={<EyeOffIcon className="size-4 text-muted-foreground" />}>
          <div className="flex flex-wrap items-center gap-3">
            <Select
              value={noiseLevel}
              onValueChange={(v) => {
                if (!loaded || savingOast) return;
                if (v !== noiseLevel) saveNoiseLevel(v);
              }}
              disabled={!loaded || savingOast}
            >
              <SelectTrigger id="opsec-noise" className="w-56">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="normal">常规（默认）</SelectItem>
                <SelectItem value="quiet">静默 · 尽量不被发现</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <p className="text-muted-foreground text-xs leading-relaxed">
            静默模式下，planner 与 worker 的提示词会追加一段<strong>平台持有</strong>的操作纪律（改 DB
            提示词也删不掉它）：先最小验证再扩面、同类请求拉开间隔、被检出就换技术而不是加大力度、
            WAF/403 时做最小验证而不是重放、留痕最小化并销账。
            <br />
            它改变的是<strong>怎么打</strong>，不改变<strong>能打哪</strong>：授权范围、作业时间窗与停机开关照常生效。
            另外态势里的「检出热点」会把真正响过的技术列给规划层，避免在同一目标上重复同一手。
          </p>
        </SubBlock>

        <SubBlock title="可检出性知识库（事前）" icon={<ScanSearchIcon className="text-muted-foreground size-4" />}>
          <p className="text-muted-foreground text-xs leading-relaxed">
            动手**之前**就能知道"这一手会留下什么"：平台按命令在下面这张表里查，命中了就把 「留在哪 / 具体信号 /
            预计可见度」贴在**审批卡**上（人做决定的那一刻），agent 也能用 opsec_notes 工具主动查。
            <br />
            两条边界写在这里免得误读：①这是**预计**不是实测——实测结论走「检测验证」（P31），
            两者刻意分开；②**没被收录不代表没有日志**，只代表这份清单没有依据。本库刻意保守， 只收公开、可核对的信号。
          </p>
          {opsecKB ? (
            // 条目默认收起（下拉）：全长十几条，铺开会把整个「安全与策略」顶下去，
            // 而真正需要逐条看它的人会自己展开。上面那两条口径始终可见——那才是这张卡
            // 最该被读到的部分。
            <Advanced title={`知识库条目（${opsecKB.entries.length} 类，按预计可见度排序）`}>
              <ul className="flex flex-col gap-2">
                {[...opsecKB.entries]
                  .sort((a, b) => exposureWeight(b.exposure) - exposureWeight(a.exposure))
                  .map((e) => (
                    <li key={e.id} className="rounded-md border p-2.5">
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge variant={e.exposure === "高" ? "destructive" : "secondary"} className="shrink-0">
                          可见度 {e.exposure}
                        </Badge>
                        <span className="text-sm font-medium">{e.title}</span>
                      </div>
                      <p className="text-muted-foreground mt-1 text-xs">
                        留在哪：{e.where.join(" / ")}
                        {(e.signals?.length ?? 0) > 0 ? ` · 信号：${(e.signals ?? []).join("；")}` : ""}
                      </p>
                      <p className="mt-1 text-xs break-words">{e.note}</p>
                    </li>
                  ))}
              </ul>
            </Advanced>
          ) : (
            <p className="text-muted-foreground text-xs">知识库加载中…</p>
          )}
        </SubBlock>

        <SubBlock
          title="探索策略（全局默认）"
          icon={<ShieldAlertIcon className="text-muted-foreground size-4" />}
          actions={
            <Button type="button" size="sm" onClick={() => void savePolicyDefault()} disabled={savingPol}>
              保存默认
            </Button>
          }
        >
          {polDefault ? (
            <>
              <p className="text-muted-foreground text-xs leading-relaxed">
                给「探索链路」划边界的默认值，<b>只影响之后新建的任务</b>
                ——每个任务创建时会把当时的默认解析成自己的快照（运行中的任务不会被这里改动意外改变边界），任务页可单独覆盖。
                不符合的<b>探索方向不会进探索图</b>（拒绝时说明命中什么、往哪改），同一份策略还会编译成
                <b>任务级拦截规则</b>兜住命令层——换措辞也绕不过。这里是<b>加法</b>
                ：平台内置的破坏性拦截（删除类请求等）始终生效，不因这里放开而解除。
              </p>

              <div className="flex flex-col gap-1.5">
                <span className="text-sm font-medium">默认禁用手段</span>
                <div className="flex flex-wrap gap-1.5">
                  {polDefault.techniques.map((t) => {
                    const on = polTech.includes(t.id);
                    return (
                      <button
                        key={t.id}
                        type="button"
                        title={t.hint}
                        disabled={savingPol}
                        onClick={() => togglePolicyChip(polTech, setPolTech, t.id)}
                        className={`rounded-full border px-2 py-0.5 text-xs transition-colors ${
                          on
                            ? "border-red-500/40 bg-red-500/15 text-red-600 dark:text-red-400"
                            : "border-border text-muted-foreground hover:bg-muted"
                        }`}
                      >
                        {on ? "禁止 · " : ""}
                        {t.label}
                      </button>
                    );
                  })}
                </div>
              </div>

              <div className="flex flex-col gap-1.5">
                <span className="text-sm font-medium">默认禁止的敏感写</span>
                <div className="flex flex-wrap gap-1.5">
                  {polDefault.writes.map((w) => {
                    const on = polWrites.includes(w.id);
                    return (
                      <button
                        key={w.id}
                        type="button"
                        title={w.hint}
                        disabled={savingPol}
                        onClick={() => togglePolicyChip(polWrites, setPolWrites, w.id)}
                        className={`rounded-full border px-2 py-0.5 text-xs transition-colors ${
                          on
                            ? "border-red-500/40 bg-red-500/15 text-red-600 dark:text-red-400"
                            : "border-border text-muted-foreground hover:bg-muted"
                        }`}
                      >
                        {on ? "禁止 · " : ""}
                        {w.label}
                      </button>
                    );
                  })}
                </div>
              </div>

              <Advanced title="高级：自定义禁止条目（词表覆盖不到时手写）">
                <CustomRulesEditor
                  rules={polCustom}
                  onChange={setPolCustom}
                  disabled={savingPol}
                  limit={polDefault.custom_limit ?? 16}
                  patternLimit={polDefault.custom_pattern_limit ?? 200}
                />
              </Advanced>

              <p className="text-muted-foreground text-xs">
                当前第 {polDefault.version} 版
                {polDefault.effective.max_intents_per_round
                  ? ` · 每轮最多 ${polDefault.effective.max_intents_per_round} 条意图（任务页可单独覆盖）`
                  : " · 每轮意图数不限（任务页可单独覆盖）"}
              </p>
            </>
          ) : (
            <p className="text-muted-foreground text-sm">加载中…</p>
          )}
        </SubBlock>

        <SubBlock title="操作约束注入" icon={<ShieldAlertIcon className="text-muted-foreground size-4" />}>
          <p className="text-muted-foreground text-xs leading-relaxed">
            开启后，把每个任务的<b>操作约束</b>（在任务总览「操作约束」里维护的 allow/deny 条目）拼进对应 Agent
            的系统提示，用来框定探索边界（如「仅测当前端口」「禁止爆破」）。默认都开；
            切换即时生效（下一轮读取），无需重建 Agent。关闭后该 Agent 不再看到约束。
          </p>
          <SwitchRow
            id="inject-planner"
            label={<>注入规划者（planner）{injectPlanner ? " · 已开启" : " · 已关闭"}</>}
            checked={injectPlanner}
            disabled={!loaded}
            onChange={toggleInjectPlanner}
          />
          <SwitchRow
            id="inject-worker"
            label={<>注入执行者（worker）{injectWorker ? " · 已开启" : " · 已关闭"}</>}
            checked={injectWorker}
            disabled={!loaded}
            onChange={toggleInjectWorker}
          />
        </SubBlock>
      </Section>
    </>
  );
}


// SafetyBoundarySummary 把"默认能用/禁用哪些行为"收成一张表：内置拦截规则（拒绝/审批/
// 已关闭）+ 探索策略额外禁止项 + 凭据预算。此前这些散落在拦截规则页和策略区，操作者
// 只能靠碰壁来猜边界；这里只做只读汇总，调整仍走各自原有入口。
function SafetyBoundarySummary({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const [rules, setRules] = React.useState<InterceptRule[] | null>(null);
  React.useEffect(() => {
    let alive = true;
    api
      .interceptRules()
      .then((rs) => {
        if (alive) setRules(rs.filter((r) => r.name.startsWith("[内置]")));
      })
      .catch(() => {
        if (alive) setRules([]);
      });
    return () => {
      alive = false;
    };
  }, []);

  const label = (name: string) => name.replace(/^\[内置\]\s*/, "");
  const denied = (rules ?? []).filter((r) => r.enabled && r.action === "deny");
  const asked = (rules ?? []).filter((r) => r.enabled && r.action === "ask");
  const off = (rules ?? []).filter((r) => !r.enabled);

  const techLabels = s.polTech.map(
    (id) => s.polDefault?.techniques.find((t) => t.id === id)?.label ?? id,
  );
  const writeLabels = s.polWrites.map(
    (id) => s.polDefault?.writes.find((w) => w.id === id)?.label ?? id,
  );
  const policyBlocked = [...techLabels, ...writeLabels];

  const credAction =
    s.credBudgetAction === "allow" ? "放行（只记账）" : s.credBudgetAction === "deny" ? "直接拒绝" : "转人工审批";
  const scopeLabel = s.scopeMode === "off" ? "不检查" : s.scopeMode === "deny" ? "越界直接拒绝" : "越界转人工审批";

  return (
    <SubBlock title="默认安全边界（一览）" icon={<ShieldAlertIcon className="text-muted-foreground size-4" />}>
      <p className="text-muted-foreground text-xs leading-relaxed">
        <b>未列出的行为默认允许。</b>默认只拦「明显危害目标」的动作（DoS、删库、破坏系统）；其余如文件上传、
        表单提交、注入验证、越权测试都可直接运行。下面是全局默认实际生效的限制（单个任务可能有自己的策略覆盖，以任务页为准），均可人工调整。
      </p>
      {rules === null ? (
        <p className="text-muted-foreground text-xs">正在读取拦截规则…</p>
      ) : (
        <div className="flex flex-col gap-2 text-xs">
          <BoundaryRow
            tone="deny"
            title="默认拒绝（高危害）"
            items={denied.map((r) => label(r.name))}
            empty="无"
            hint="内置规则；在「拦截与审批」页可逐条关闭或改为审批。"
          />
          <BoundaryRow
            tone="ask"
            title="默认转人工审批"
            items={asked.map((r) => label(r.name))}
            empty="无"
            hint="命中后动作暂停，等你在审批页放行；也可改成允许/拒绝。"
          />
          <BoundaryRow
            tone="off"
            title="内置规则已关闭（可随时启用）"
            items={off.map((r) => label(r.name))}
            empty="无"
            hint="规则仍在，只是不生效；按授权边界需要时可重新打开。"
          />
          <BoundaryRow
            tone="policy"
            title="探索策略额外禁止"
            items={policyBlocked}
            empty="无（探索方向全授权）"
            hint="只影响本页下方「探索策略（全局默认）」，改完只对新建任务生效。"
          />
        </div>
      )}
      <p className="text-muted-foreground text-xs leading-relaxed">
        凭据尝试预算：
        {s.credBudget && s.credBudget !== "0" ? (
          <>
            30 分钟内每「目标 + 账号」<b>{s.credBudget}</b> 次，超预算后<b>{credAction}</b>
          </>
        ) : (
          <b>不限</b>
        )}
        （本页可改）。授权范围：<b>{scopeLabel}</b>。
      </p>
      <p className="text-muted-foreground text-xs leading-relaxed">
        调整入口：<Link className="underline underline-offset-2" href="/system/intercept">拦截与审批</Link>
        （内置规则的开关/动作）、下方「探索策略（全局默认）」（方向与写操作）、本页「凭据尝试预算」。
      </p>
    </SubBlock>
  );
}

function BoundaryRow({
  tone,
  title,
  items,
  empty,
  hint,
}: {
  tone: "deny" | "ask" | "off" | "policy";
  title: string;
  items: string[];
  empty: string;
  hint: string;
}) {
  const toneClass =
    tone === "deny"
      ? "border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-400"
      : tone === "ask"
        ? "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400"
        : tone === "policy"
          ? "border-border bg-muted text-foreground"
          : "border-border text-muted-foreground";
  return (
    <div className="flex flex-col gap-1">
      <span className="text-sm font-medium">{title}</span>
      <div className="flex flex-wrap gap-1">
        {items.length === 0 ? (
          <span className="text-muted-foreground text-xs">{empty}</span>
        ) : (
          items.map((item) => (
            <span key={item} className={`rounded-full border px-2 py-0.5 text-[11px] ${toneClass}`}>
              {item}
            </span>
          ))
        )}
      </div>
      <span className="text-muted-foreground text-[11px]">{hint}</span>
    </div>
  );
}
