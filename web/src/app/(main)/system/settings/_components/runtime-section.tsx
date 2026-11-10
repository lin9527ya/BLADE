"use client";

import { CpuIcon, GaugeIcon, ShieldCheckIcon } from "lucide-react";

import { Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 执行与并发。

export function RuntimeSection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const {
    loaded,
    workers,
    setWorkers,
    intentQueueFactor,
    setIntentQueueFactor,
    savingWorkers,
    supervisorEnabled,
    setSupervisorEnabled,
    supervisorInterval,
    setSupervisorInterval,
    savingSupervisor,
    saveSupervisor,
    tokenBudget,
    setTokenBudget,
    savingBudget,
    completionGate,
    setCompletionGate,
    savingCompletionGate,
    saveCompletionGate,
    boundedView,
    setBoundedView,
    savingBoundedView,
    saveBoundedView,
    execCPU,
    setExecCPU,
    execMem,
    setExecMem,
    execProcs,
    setExecProcs,
    execMaxActive,
    setExecMaxActive,
    execMaxAge,
    setExecMaxAge,
    mcpConcurrent,
    setMcpConcurrent,
    mcpBreakerFails,
    setMcpBreakerFails,
    mcpBreakerCooldown,
    setMcpBreakerCooldown,
    savingLimits,
    saveLimits,
    saveWorkers,
    saveTokenBudget,
    logLevel,
    setLogLevel,
    savingLogLevel,
    saveLogLevel,
    constraintProfile,
    constraintGroups,
    savingConstraints,
    saveConstraints,
  } = s;

  return (
    <Section title="执行与并发" hint="改完对之后启动的任务与之后提交的长任务执行生效（运行中的任务不受影响）。">
      <SubBlock title="探索约束" icon={<GaugeIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="预设"
          htmlFor="constraint-profile"
          hint="经典混合：约束关闭，但保留 ARTEX 的防停摆/开局探测/深度优先骨架（默认）。增强：全部开启。自定义：逐组选择；单独拨动任一组会自动切到自定义。"
        >
          <Select
            value={constraintProfile}
            onValueChange={(v) => saveConstraints({ profile: v as "classic" | "enhanced" | "custom" })}
            disabled={!loaded || savingConstraints}
          >
            <SelectTrigger id="constraint-profile" className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="classic">经典（约束关）</SelectItem>
                <SelectItem value="enhanced">增强（约束全开）</SelectItem>
                <SelectItem value="custom">自定义</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </Field>
        {[
          ["opening", "开局摸底", "开局探测、粗粒度硬拒与 record_probe_fact 证据门。"],
          ["dispatch", "派单与补位", "槽位补满、空闲补派、空转暂停、队列背压、防打转、每轮意图上限。"],
          ["breadth", "广度与横向", "广度债、横向推进纪律、scope_surface。"],
          ["ledger", "探索/通路台账", "ledger/chain 提示、coverage_matrix、台账工具与态势字段。"],
          ["review_endgame", "复盘与收官期", "强制复盘轮、复盘 3 条上限、升级阶梯、收官期禁扩面。"],
          ["completion", "收官审计", "prove_goal 的覆盖/通路/线索/证据缺口审计。"],
          ["exhaustion", "穷尽复核", "mark_strategy_exhausted 六门与独立复核员。"],
          ["supervisor", "执行审核员", "定期抽查 worker 并用 steer 纠偏（额外 LLM 调用）。"],
          ["depth", "深度与线索", "否定结论变体门槛、线索写回/分诊与 leads 态势。"],
        ].map(([key, label, hint]) => (
          <SwitchRow
            key={key}
            id={`constraint-${key}`}
            label={label}
            hint={hint}
            checked={!!constraintGroups[key]}
            disabled={!loaded || savingConstraints}
            onChange={(v) => saveConstraints({ group: key, value: v })}
          />
        ))}
      </SubBlock>
      <SubBlock title="日志级别" icon={<CpuIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="最低级别"
          htmlFor="log-level"
          hint="低于该级别的日志仍写终端，但不进日志页、不落库、不推 SSE。debug 用于排障，会产生较多日志。"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Select value={logLevel} onValueChange={(v) => setLogLevel(v as typeof logLevel)}>
              <SelectTrigger id="log-level" className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="debug">debug</SelectItem>
                  <SelectItem value="info">info（默认）</SelectItem>
                  <SelectItem value="warn">warn</SelectItem>
                  <SelectItem value="error">error</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button size="sm" onClick={() => void saveLogLevel()} disabled={savingLogLevel || !loaded}>
              {savingLogLevel ? <Spinner data-icon="inline-start" /> : null}
              保存
            </Button>
          </div>
        </Field>
      </SubBlock>
      <SubBlock title="任务 Token 预算" icon={<GaugeIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="预算（token 数）"
          htmlFor="token-budget"
          hint={
            <>
              按任务累计 LLM 用量（输入+输出）：到 <b>70% / 85% / 95%</b> 各向 planner 投递一次收敛提示，达到{" "}
              <b>100%</b> 时任务进入收尾——停派新意图、收在跑方向、出终局报告，不再无限消耗。 留空或填 <b>0</b> =
              关闭护栏。
              <br />
              这是<b>体量护栏</b>：缓存命中的 token 也按全量计入，不代表实际费用。
              <br />
              {Number(tokenBudget) > 0 ? "已配置 · 到量提示并自动收尾" : "未配置 · 任务不设 token 上限"}
            </>
          }
        >
          <div className="flex items-center gap-2">
            <Input
              id="token-budget"
              inputMode="numeric"
              autoComplete="off"
              className="max-w-xl"
              placeholder="例如 2000000（0 = 关闭）"
              value={tokenBudget}
              disabled={!loaded || savingBudget}
              onChange={(e) => setTokenBudget(e.target.value)}
            />
            <Button type="button" onClick={saveTokenBudget} disabled={!loaded || savingBudget}>
              保存
            </Button>
          </div>
        </Field>
      </SubBlock>

      <SubBlock title="态势视图" icon={<GaugeIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="有界黑板视图"
          htmlFor="bounded-view"
          hint={
            <>
              开启时 planner 每轮只预取窗口化的态势（totals 真实、列表有上限，省 token），需要更多细节时由模型自己
              <b> graph_overview(detail=true) / list_facts</b> 翻页；关闭后每轮预取完整态势，<b>上下文更大但不再裁剪</b>。
              只想让大模型自己决定看多少时，可以关掉它。
            </>
          }
        >
          <div className="flex items-center gap-3">
            <Switch
              id="bounded-view"
              checked={boundedView}
              disabled={!loaded || savingBoundedView}
              onCheckedChange={setBoundedView}
            />
            <Button size="sm" onClick={() => void saveBoundedView()} disabled={savingBoundedView || !loaded}>
              {savingBoundedView ? <Spinner data-icon="inline-start" /> : null}
              保存
            </Button>
          </div>
        </Field>
      </SubBlock>

      <SubBlock title="收官审计" icon={<ShieldCheckIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="目标达成后的充分性检查"
          htmlFor="completion-gate"
          hint={
            <>
              目标已达成但探索台账仍有<b>未接受的风险</b>（未测面、未推进的据点/凭据、未闭合候选、未处理线索）时：
              <b>阻断</b>会拒绝收官并要求补测或显式接受风险；<b>只提醒</b>把缺口写进回执但不拦；<b>关闭</b>恢复旧行为。
              <br />
              接受风险需要写理由，理由会进报告的数据缺项——放行可以，但必须说得出来。
            </>
          }
        >
          <div className="flex flex-wrap items-center gap-2">
            <Select value={completionGate} onValueChange={(v) => setCompletionGate(v as typeof completionGate)}>
              <SelectTrigger id="completion-gate" className="w-56">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="block">阻断（默认）</SelectItem>
                  <SelectItem value="notice">只提醒</SelectItem>
                  <SelectItem value="off">关闭</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button size="sm" onClick={() => void saveCompletionGate()} disabled={savingCompletionGate || !loaded}>
              {savingCompletionGate ? <Spinner data-icon="inline-start" /> : null}
              保存
            </Button>
          </div>
        </Field>
      </SubBlock>

      <SubBlock title="执行审核员（Supervisor）" icon={<ShieldCheckIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="启用与审核周期"
          htmlFor="supervisor-enabled"
          hint="定时抽查在跑 worker 的最近轨迹，判定偏离时注入纠偏。关闭后不再调用审核模型、不再下发纠偏（已有会话记录保留）。周期 60–1800 秒，改完对下一个周期生效。"
        >
          <div className="flex flex-wrap items-center gap-3">
            <Switch
              id="supervisor-enabled"
              checked={supervisorEnabled}
              disabled={!loaded || savingSupervisor}
              onCheckedChange={setSupervisorEnabled}
            />
            <Input
              id="supervisor-interval"
              type="number"
              min={60}
              max={1800}
              className="w-32 font-mono text-sm"
              value={supervisorInterval}
              disabled={!loaded || savingSupervisor || !supervisorEnabled}
              onChange={(e) => setSupervisorInterval(e.target.value)}
            />
            <span className="text-muted-foreground text-xs">秒</span>
            <Button onClick={saveSupervisor} disabled={!loaded || savingSupervisor}>
              保存
            </Button>
          </div>
        </Field>
      </SubBlock>

      <SubBlock title="工作并发 · Work Agent 数" icon={<CpuIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="并发工作 agent 数"
          htmlFor="workers"
          hint="每个任务的固定 Worker 槽位数（默认 3）。任务首次启动时固化，后续所有意图都在这 N 个槽位内复用同一会话执行（不再每个意图新建 worker，缓存命中更高）；数值越大并发探测越多、消耗也越高。修改后对之后首次启动的任务生效，正在运行的任务不受影响。"
        >
          <div className="flex items-center gap-2">
            <Input
              id="workers"
              type="number"
              min={1}
              className="w-32 font-mono text-sm"
              placeholder="3"
              value={workers}
              disabled={!loaded || savingWorkers}
              onChange={(e) => setWorkers(e.target.value)}
            />
          </div>
        </Field>
        <Field
          label="待领意图上限倍数（×workers）"
          htmlFor="intent-queue-factor"
          hint="待领意图达到「倍数×worker 数」时，规划者不再接收低优先级新方向（priority ≥8 的跟进仍放行）。默认 4；保存后下一轮规划生效，无需重启。"
        >
          <div className="flex items-center gap-2">
            <Input
              id="intent-queue-factor"
              type="number"
              min={1}
              max={32}
              className="w-32 font-mono text-sm"
              placeholder="4"
              value={intentQueueFactor}
              disabled={!loaded || savingWorkers}
              onChange={(e) => setIntentQueueFactor(e.target.value)}
            />
            <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
              保存
            </Button>
          </div>
        </Field>
      </SubBlock>

      <SubBlock
        title="长任务执行限额与 MCP 守卫"
        icon={<GaugeIcon className="size-4 text-muted-foreground" />}
        actions={
          <Button type="button" size="sm" onClick={() => void saveLimits()} disabled={!loaded || savingLimits}>
            保存
          </Button>
        }
      >
        <p className="text-muted-foreground text-xs leading-relaxed">
          长任务（run_execution）此前只有硬超时，没有 CPU/内存/进程数上限——一条跑飞的扫描就能把平台自己这台机器打满。
          这里给每次执行加 <code>ulimit</code> 前导（<b>0 = 该轴不限</b>）
          ：请求了什么、本机是否真的生效，都会如实记进执行台账（本机 <code>/bin/sh</code>
          不支持某项时记「未生效」，不假装限住了）。
          <br />
          外部 MCP server 挂掉或变慢时，Agent 会一轮轮重试、把时间全烧在等待上。守卫给每个 server 加<b>并发上限</b>与
          <b>连续失败熔断</b>
          ：开闸期间直接快速失败并说明原因（不发网络请求），冷却后放行一次真实调用，成功即恢复；也可以在 MCP
          页手动重置。
        </p>
        <div className="grid gap-4 md:grid-cols-3">
          <Field label="CPU 上限（秒，0 = 不限）" htmlFor="exec-cpu">
            <Input
              id="exec-cpu"
              inputMode="numeric"
              value={execCPU}
              onChange={(e) => setExecCPU(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
          <Field label="内存上限（MB，0 = 不限）" htmlFor="exec-mem">
            <Input
              id="exec-mem"
              inputMode="numeric"
              value={execMem}
              onChange={(e) => setExecMem(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
          <Field label="进程数上限（0 = 不限）" htmlFor="exec-procs">
            <Input
              id="exec-procs"
              inputMode="numeric"
              value={execProcs}
              onChange={(e) => setExecProcs(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
          <Field label="同时执行数上限（0 = 不限）" htmlFor="exec-max-active">
            <Input
              id="exec-max-active"
              inputMode="numeric"
              value={execMaxActive}
              onChange={(e) => setExecMaxActive(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
          <Field label="单条执行最大存活（分钟，0 = 不限）" htmlFor="exec-max-age">
            <Input
              id="exec-max-age"
              inputMode="numeric"
              value={execMaxAge}
              onChange={(e) => setExecMaxAge(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
          <Field label="MCP 每 server 并发上限（0 = 不限）" htmlFor="mcp-conc">
            <Input
              id="mcp-conc"
              inputMode="numeric"
              value={mcpConcurrent}
              onChange={(e) => setMcpConcurrent(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
          <Field label="连续失败多少次熔断" htmlFor="mcp-fails">
            <Input
              id="mcp-fails"
              inputMode="numeric"
              value={mcpBreakerFails}
              onChange={(e) => setMcpBreakerFails(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
          <Field label="熔断冷却（秒）" htmlFor="mcp-cool">
            <Input
              id="mcp-cool"
              inputMode="numeric"
              value={mcpBreakerCooldown}
              onChange={(e) => setMcpBreakerCooldown(e.target.value.replace(/[^0-9]/g, ""))}
              disabled={!loaded || savingLimits}
            />
          </Field>
        </div>
        <p className="text-muted-foreground text-xs">
          限额只在<b>长任务执行</b>上生效；普通 Bash 调用不受影响（它本身有 run 级超时）。
        </p>
      </SubBlock>
    </Section>
  );
}
