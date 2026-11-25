"use client";

import Link from "next/link";

import {
  AlertTriangleIcon,
  ArrowUpRightIcon,
  Link2Icon,
  PinIcon,
  PinOffIcon,
  RadioTowerIcon,
  RefreshCwIcon,
} from "lucide-react";

import { Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import type { ProxyErrorKind, ProxyPoolStrategy } from "@/lib/types";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 网络与代理。

const ERROR_KIND_LABELS: Record<Exclude<ProxyErrorKind, "">, string> = {
  proxy_auth: "代理鉴权失败",
  proxy_connect: "代理连接拒绝",
  probe_timeout: "探测超时",
  probe_target_http: "探测目标 5xx",
  other: "探测异常",
};

function formatCheckedAt(ts?: string): string {
  if (!ts) return "";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString("zh-CN", { hour12: false });
}

function proxySummaryText(opts: {
  dirty: boolean;
  probing: boolean;
  blocked: boolean;
  active: string;
  pinned: string;
  cooldownRem: number;
  hasInput: boolean;
}): string {
  if (opts.dirty) return "有未保存的修改 · 点「保存」生效";
  if (opts.probing) return "正在并发探测全部候选节点…";
  if (opts.blocked) return "全部代理不可用：出口已阻断（不会直连）";
  if (opts.active) {
    const pinnedTag = opts.pinned ? "（已固定）" : "";
    const cooldownTag = opts.cooldownRem > 0 ? ` · 冷却剩余 ${opts.cooldownRem}s` : "";
    return `当前出口：${opts.active}${pinnedTag}${cooldownTag}`;
  }
  return opts.hasInput ? "正在初始化探测…" : "未配置 · 目标流量直连";
}

function pinButtonLabel(pinningProxy: string, candidate: string, isPinned: boolean): string {
  if (pinningProxy === candidate) return "切换中…";
  return isPinned ? "取消固定" : "固定使用";
}

function egressLineIcon(leaked?: boolean, error?: string): string {
  if (leaked) return "⚠";
  return error ? "✗" : "✓";
}

export function NetworkSection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const {
    trafficCapture,
    agentTrafficBinding,
    globalProxyPoolInput,
    setGlobalProxyPoolInput,
    proxySavedPool,
    proxyHealthURL,
    setProxyHealthURL,
    proxyHealthTimeout,
    setProxyHealthTimeout,
    proxyHealthInterval,
    setProxyHealthInterval,
    proxyFailThreshold,
    setProxyFailThreshold,
    proxySwitchCooldown,
    setProxySwitchCooldown,
    proxyStrategy,
    setProxyStrategy,
    proxyActive,
    proxyBlocked,
    proxyPinned,
    proxyProbing,
    probingProxy,
    pinningProxy,
    proxyStatus,
    proxySaveError,
    proxyEgressIPURL,
    setProxyEgressIPURL,
    proxyEgress,
    checkingProxyEgress,
    checkProxyEgress,
    checkSingleProxyEgress,
    refreshProxyStatus,
    pinProxy,
    savingGlobalProxy,
    clashApiURL,
    setClashApiURL,
    clashSecretInput,
    setClashSecretInput,
    clashSecretSet,
    clashGroup,
    setClashGroup,
    clashGroups,
    setClashGroups,
    clashTaskEndpoints,
    setClashTaskEndpoints,
    clashAuto,
    setClashAuto,
    clashCooldown,
    setClashCooldown,
    clashStatus,
    egress,
    savingClash,
    rotatingClash,
    saveClash,
    rotateClash,
    loaded,
    savingTraffic,
    savingAgentBinding,
    toggleTraffic,
    toggleAgentTrafficBinding,
    saveGlobalProxy,
  } = s;

  const trafficInfo = egress?.traffic;
  const failThreshold = egress?.proxy?.fail_threshold || Number.parseInt(proxyFailThreshold, 10) || 2;
  const cooldownRem = egress?.proxy?.cooldown_remaining_seconds ?? 0;
  const egressByProxy = new Map(proxyEgress.map((item) => [item.proxy, item]));
  const statusProxies = new Set(proxyStatus.map((h) => h.proxy));
  const extraEgressResults = proxyEgress.filter((r) => !statusProxies.has(r.proxy));
  const clashIsolationOverridden =
    trafficCapture &&
    (trafficInfo?.clash_isolation_overridden || (Boolean(clashGroups.trim()) && Boolean(clashTaskEndpoints.trim())));
  let autoBindLabel = "已关闭 · 可继续人工绑定";
  if (agentTrafficBinding) {
    autoBindLabel = trafficCapture ? "已开启 · 会增加 Token 消耗" : "已开启 · 当前未开流量捕获（仅可关联已有 ID）";
  }

  return (
    <Section
      title="网络与代理"
      hint="代理池支持原地热更新与运行时动态切换，不打断已有健康的任务运行；流量捕获切换会重置任务 Agent 缓存并在下一轮生效；Agent 自动绑定修改后立即拦截新写入并于下一轮 Agent 生效。"
    >
      <SubBlock title="流量捕获" icon={<RadioTowerIcon className="size-4 text-muted-foreground" />}>
        <SwitchRow
          id="traffic-capture"
          label={trafficCapture ? "已开启 · 正在记录流量并注入代理" : "已关闭 · 不记录、不注入代理"}
          checked={trafficCapture}
          disabled={!loaded || savingTraffic}
          onChange={toggleTraffic}
          hint={
            <>
              开启后，所有 Agent 的 HTTP 流量经记录代理全量落库，并向 Agent 注入 traffic_search / traffic_get /
              traffic_blob 工具与代理配置（提示词含代理说明）。
              <br />
              关闭（默认）时不记录任何流量：Agent
              <b>不会</b>拿到代理配置与流量工具，提示词也<b>不含</b>代理相关内容。
            </>
          }
        />
        {trafficInfo && (
          <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-md border bg-muted/30 px-3 py-2 text-xs">
            <span className={trafficInfo.proxy_listening ? "text-foreground" : "text-destructive"}>
              {trafficInfo.proxy_listening ? `录制代理 ${trafficInfo.proxy_addr || ":9899"} 监听中` : "录制代理未启动"}
            </span>
            <span className="text-muted-foreground">·</span>
            <span className="text-muted-foreground">{trafficInfo.ca_ready ? "MITM CA 已就绪" : "MITM CA 未就绪"}</span>
            <span className="text-muted-foreground">·</span>
            <span className="text-muted-foreground">
              已录制 <strong className="font-mono text-foreground">{trafficInfo.recorded_count}</strong> 条
            </span>
            <Link
              href="/function/traffic"
              className="ml-auto inline-flex items-center gap-1 font-medium text-primary hover:underline"
            >
              查看流量
              <ArrowUpRightIcon className="size-3.5" />
            </Link>
          </div>
        )}
        {clashIsolationOverridden && (
          <div className="mt-2.5 flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-amber-800 text-xs leading-relaxed dark:text-amber-300">
            <AlertTriangleIcon className="mt-0.5 size-3.5 shrink-0" />
            <span>
              当前已开启流量捕获：进程级录制代理会接管所有任务 HTTP 流量并共用全局出口，下方配置的「Clash
              按任务独立出口隔离」在捕获期间暂不生效。
            </span>
          </div>
        )}
      </SubBlock>

      <SubBlock title="Agent 自动绑定流量" icon={<Link2Icon className="size-4 text-muted-foreground" />}>
        <SwitchRow
          id="agent-traffic-binding"
          describedBy="agent-traffic-binding-description"
          label={autoBindLabel}
          checked={agentTrafficBinding}
          disabled={!loaded || savingAgentBinding}
          onChange={toggleAgentTrafficBinding}
          hint={
            <>
              默认关闭。开启后，漏洞入库时触发的报告 Agent 会核对已有 HTTP 请求/响应，关联对应流量后再编写报告。
              <b>查阅数据包及额外的工具调用会增加 Token 消耗。</b>
              <br />
              TCP、未抓包或没有匹配流量时仍可正常上报。此开关不影响流量捕获、人工绑定及已保存证据的查看；对下一轮 Agent
              生效，关闭后会立即拒绝新的自动绑定。
            </>
          }
        />
        {loaded && agentTrafficBinding && !trafficCapture && (
          <div className="mt-3 flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-amber-800 text-xs dark:text-amber-300">
            <div className="flex min-w-0 items-start gap-2">
              <AlertTriangleIcon className="mt-0.5 size-3.5 shrink-0" />
              <span className="leading-relaxed">
                未开启「流量捕获」：Agent 不会录制新 HTTP 请求，也不会注入 <code>traffic_search</code> /{" "}
                <code>traffic_get</code> 检索工具，当前仅对已明确交接的历史 <code>traffic_id</code> 有效。
              </span>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              <Button type="button" size="xs" disabled={savingTraffic} onClick={() => toggleTraffic(true)}>
                开启流量捕获
              </Button>
              <Button
                type="button"
                size="xs"
                variant="outline"
                disabled={savingAgentBinding}
                onClick={() => toggleAgentTrafficBinding(false)}
              >
                关闭自动绑定
              </Button>
            </div>
          </div>
        )}
      </SubBlock>

      <SubBlock title="全局代理池" icon={<RadioTowerIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="代理池（每行一个）"
          htmlFor="global-proxy-pool"
          hint={
            <>
              支持 <code>http://</code> / <code>https://</code> / <code>socks5://</code> / <code>socks5h://</code>
              （可带 user:pass）。 Clash 直接用混合端口，例如 <code>http://127.0.0.1:7890</code>。
              <br />
              服务端按健康检查自动切换；<b>全部不可用时出口会被阻断（不会直连）</b>，避免泄漏真实 IP。
              <br />
              代理池只切换你填的多个地址；Clash <b>内部节点的按任务切换</b>在下方「Clash 出口切换」里配置。 LLM
              的代理在各自 profile 里配置，不在此池内。
            </>
          }
        >
          <Textarea
            id="global-proxy-pool"
            rows={4}
            autoComplete="off"
            placeholder={"http://127.0.0.1:7890\nsocks5h://127.0.0.1:7891"}
            value={globalProxyPoolInput}
            disabled={!loaded || savingGlobalProxy}
            onChange={(e) => setGlobalProxyPoolInput(e.target.value)}
          />
          <div className="grid gap-3 md:grid-cols-[1fr_190px]">
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">
                健康检查 URL（支持多条，每行或逗号分隔，任一通畅即判节点健康）
              </span>
              <Textarea
                id="proxy-health-url"
                rows={2}
                autoComplete="off"
                placeholder={"https://www.gstatic.com/generate_204\nhttps://cp.cloudflare.com/generate_204"}
                value={proxyHealthURL}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setProxyHealthURL(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">调度与回切策略</span>
              <Select
                value={proxyStrategy}
                disabled={!loaded || savingGlobalProxy}
                onValueChange={(v) => setProxyStrategy(v as ProxyPoolStrategy)}
              >
                <SelectTrigger id="proxy-strategy" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ordered">主备优先（恢复后回切）</SelectItem>
                  <SelectItem value="sticky">粘性转移（可用不切）</SelectItem>
                  <SelectItem value="fastest">最低延迟（平滑择优）</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">超时（秒）</span>
              <Input
                id="proxy-health-timeout"
                type="number"
                min={1}
                max={60}
                value={proxyHealthTimeout}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setProxyHealthTimeout(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">检查间隔（秒）</span>
              <Input
                id="proxy-health-interval"
                type="number"
                min={5}
                max={600}
                value={proxyHealthInterval}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setProxyHealthInterval(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">失败阈值</span>
              <Input
                id="proxy-fail-threshold"
                type="number"
                min={1}
                max={10}
                value={proxyFailThreshold}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setProxyFailThreshold(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">切换冷却（秒）</span>
              <Input
                id="proxy-switch-cooldown"
                type="number"
                min={0}
                max={600}
                value={proxySwitchCooldown}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setProxySwitchCooldown(e.target.value)}
              />
            </div>
          </div>
          <div className="grid gap-3 md:grid-cols-[1fr_auto]">
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">出口 IP 查询 URL</span>
              <Input
                id="proxy-egress-ip-url"
                autoComplete="off"
                placeholder="https://api.ipify.org"
                value={proxyEgressIPURL}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setProxyEgressIPURL(e.target.value)}
              />
            </div>
            <div className="flex items-end">
              <Button
                type="button"
                variant="outline"
                onClick={checkProxyEgress}
                disabled={!loaded || checkingProxyEgress}
              >
                {checkingProxyEgress ? "检测中…" : "检测全部出口 IP"}
              </Button>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
              保存
            </Button>
            <Button type="button" variant="outline" onClick={refreshProxyStatus} disabled={!loaded || probingProxy}>
              <RefreshCwIcon className={probingProxy ? "mr-1 size-3.5 animate-spin" : "mr-1 size-3.5"} />
              {probingProxy ? "探测中…" : "立即探测"}
            </Button>
            {proxyPinned && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!loaded || pinningProxy !== ""}
                onClick={() => pinProxy("")}
              >
                <PinOffIcon className="mr-1 size-3.5" />
                取消固定
              </Button>
            )}
            <span className={proxyBlocked ? "text-destructive text-xs" : "text-muted-foreground text-xs"}>
              {proxySummaryText({
                dirty: globalProxyPoolInput.trim() !== proxySavedPool.trim(),
                probing: proxyProbing || probingProxy,
                blocked: proxyBlocked,
                active: proxyActive,
                pinned: proxyPinned,
                cooldownRem,
                hasInput: Boolean(globalProxyPoolInput.trim()),
              })}
            </span>
          </div>
          {proxySaveError && <p className="text-destructive text-xs">保存失败：{proxySaveError}</p>}
          {proxyStatus.length > 0 && (
            <div className="space-y-1.5 rounded-md border bg-muted/20 p-2.5">
              {proxyStatus.map((h) => {
                const isCurrent = !proxyBlocked && proxyActive === h.proxy;
                const isPinned = Boolean(h.pinned || (proxyPinned && proxyPinned === h.proxy));
                const eg = egressByProxy.get(h.proxy);
                const checkedAt = formatCheckedAt(h.checked_at);
                const errKindLabel = h.error_kind ? ERROR_KIND_LABELS[h.error_kind] : "";
                return (
                  <div
                    key={h.proxy}
                    className="flex flex-wrap items-center justify-between gap-2 rounded border bg-background px-2.5 py-2 text-xs"
                  >
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className={h.ok ? "font-mono text-foreground" : "font-mono text-destructive"}>
                          {h.ok ? "✓" : "✗"} {h.proxy}
                        </span>
                        {isCurrent && <Badge variant="secondary">当前生效</Badge>}
                        {isPinned && (
                          <Badge variant="outline">
                            <PinIcon className="mr-0.5 size-3" />
                            已固定
                          </Badge>
                        )}
                        {!h.ok && (h.fail_count ?? 0) > 0 && (
                          <Badge variant="destructive">
                            失败 {h.fail_count}/{failThreshold}
                          </Badge>
                        )}
                        {errKindLabel && <Badge variant="outline">{errKindLabel}</Badge>}
                        {eg?.leaked && <Badge variant="destructive">出口与本机同 IP</Badge>}
                      </div>
                      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-muted-foreground">
                        {h.ok && typeof h.latency_ms === "number" && (
                          <span>
                            延迟 {h.latency_ms}ms
                            {typeof h.avg_latency_ms === "number" && h.avg_latency_ms > 0
                              ? `（均 ${h.avg_latency_ms}ms）`
                              : ""}
                          </span>
                        )}
                        {checkedAt && <span>探测于 {checkedAt}</span>}
                        {eg?.ip && !eg.error && <span>出口 IP {eg.ip}</span>}
                        {!h.ok && h.error && <span className="text-destructive">{h.error}</span>}
                        {eg?.error && <span className="text-destructive">出口 IP 查询：{eg.error}</span>}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                      <Button
                        type="button"
                        size="xs"
                        variant="outline"
                        disabled={!loaded || checkingProxyEgress}
                        onClick={() => checkSingleProxyEgress(h.proxy)}
                      >
                        测出口 IP
                      </Button>
                      <Button
                        type="button"
                        size="xs"
                        variant={isPinned ? "secondary" : "outline"}
                        disabled={!loaded || pinningProxy !== ""}
                        onClick={() => pinProxy(isPinned ? "" : h.proxy)}
                      >
                        {pinButtonLabel(pinningProxy, h.proxy, isPinned)}
                      </Button>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
          {extraEgressResults.length > 0 && (
            <ul className="space-y-0.5 text-xs">
              {extraEgressResults.map((r) => (
                <li key={r.proxy} className={r.error || r.leaked ? "text-destructive" : "text-muted-foreground"}>
                  {egressLineIcon(r.leaked, r.error)} {r.proxy}
                  {r.ip ? ` · 出口 IP ${r.ip}` : ""}
                  {r.leaked ? " · 与本机网卡地址相同（疑似未走代理）" : ""}
                  {r.error ? ` · ${r.error}` : ""}
                </li>
              ))}
            </ul>
          )}
          <p className="text-muted-foreground text-xs">
            出口 IP 检测只经候选代理发起，不会直连；若结果与你的真实出口不一致，请检查 Clash 规则/节点。
          </p>
        </Field>
      </SubBlock>

      <SubBlock title="Clash 出口切换" icon={<RadioTowerIcon className="size-4 text-muted-foreground" />}>
        <Field
          label="Clash 外部控制器"
          htmlFor="clash-api-url"
          hint={
            <>
              目标 WAF 封的是**出口 IP**，代理端口本身仍然健康，代理池不会因此切换。开启 Clash 的
              <code> external-controller</code> 后，BLADE 可在 Selector 组里换节点并验证出口 IP 已变化。
              <br />
              安全约束：只选非 <code>DIRECT/REJECT</code> 节点；切换后经当前代理查出口
              IP，必须变化且不是本机地址；失败不直连。
              <br />
              强烈建议给 BLADE 单独建一个 <code>type: mixed</code> 的 listener，并用 <code>proxy: 组名</code> 强制走该
              Selector 组——否则 Clash 规则仍可能把目标判成 DIRECT，换节点也救不了真实 IP。
              <br />
              Clash 当前是 <code>global</code> 模式时，真正决定所有出口的是 <b>GLOBAL</b> 组：组名必须填{" "}
              <code>GLOBAL</code>（或改用规则模式/专用 listener）；填其它组名只会改那个组，不会改变实际流量。
            </>
          }
        >
          <div className="grid gap-3 md:grid-cols-2">
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">控制器地址</span>
              <Input
                id="clash-api-url"
                autoComplete="off"
                placeholder="http://192.168.10.137:9090"
                value={clashApiURL}
                disabled={!loaded || savingClash}
                onChange={(e) => setClashApiURL(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">secret（只写不回显）</span>
              <Input
                autoComplete="off"
                type="password"
                placeholder={clashSecretSet ? "已配置 · 留空保持不变" : "未配置（控制器无 secret 可留空）"}
                value={clashSecretInput}
                disabled={!loaded || savingClash}
                onChange={(e) => setClashSecretInput(e.target.value)}
              />
            </div>
          </div>
          <div className="grid gap-3 md:grid-cols-[1fr_130px]">
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">默认 / 回退 Selector 组</span>
              <Input
                autoComplete="off"
                placeholder="BLADE（global 模式填 GLOBAL）"
                value={clashGroup}
                disabled={!loaded || savingClash}
                onChange={(e) => setClashGroup(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <span className="text-muted-foreground text-xs">切换冷却（秒）</span>
              <Input
                type="number"
                min={0}
                max={600}
                value={clashCooldown}
                disabled={!loaded || savingClash}
                onChange={(e) => setClashCooldown(e.target.value)}
              />
            </div>
          </div>
          <div className="space-y-1.5">
            <span className="text-muted-foreground text-xs">
              任务出口组池（每行一个 Selector 组；任务按组隔离出口 IP，留空 = 全部共用默认组）
            </span>
            <Textarea
              autoComplete="off"
              rows={3}
              placeholder={"Tiktok\nAI\nNetflix\nSteam"}
              value={clashGroups}
              disabled={!loaded || savingClash}
              onChange={(e) => setClashGroups(e.target.value)}
            />
            <p className="text-muted-foreground text-xs leading-relaxed">
              每个组必须在 Clash 里是独立的 <code>Selector</code>，组内节点互不重叠；BLADE 会把任务稳定绑定到
              空闲的组，任务之间不会互抢出口 IP。只有同时配置了任务专属 listener 的组才算可用组；
              可用组数就是独占运行的任务上限，超出部分自动排队，任务暂停/结束立即让出组并补位。 未配置 listener
              的组仅用于节点轮换，不参与独占调度；开启流量捕获时按任务出口隔离不可用。
            </p>
          </div>
          <div className="space-y-1.5">
            <span className="text-muted-foreground text-xs">
              任务专属出口 listener（每行 <code>组名=代理地址</code>；填了才会让任务真正走不同端口/IP）
            </span>
            <Textarea
              autoComplete="off"
              rows={3}
              placeholder={"Tiktok=http://127.0.0.1:7890\nAI=http://127.0.0.1:7891\nNetflix=http://127.0.0.1:7892"}
              value={clashTaskEndpoints}
              disabled={!loaded || savingClash}
              onChange={(e) => setClashTaskEndpoints(e.target.value)}
            />
            <p className="text-muted-foreground text-xs leading-relaxed">
              在 Clash 配置里给每个组建一个独立 <code>mixed</code> listener，并设置 <code>proxy: 组名</code>，再把
              listener 地址填到这里。只填组池不填 listener 时，任务仍共用
              全局代理端口，只能隔离“切换哪个组”，不能同时给不同任务不同 IP。
            </p>
          </div>
          <SwitchRow
            id="clash-auto-rotate"
            label={clashAuto ? "自动：WAF 拦截或目标持续不可达时切换" : "自动切换已关闭（仍可用工具/按钮手动切）"}
            checked={clashAuto}
            disabled={!loaded || savingClash}
            onChange={() => setClashAuto((v) => !v)}
            hint="两类触发：① 强 WAF 指纹（Cloudflare / Incapsula / Akamai / Sucuri / AWS WAF / ModSecurity + 403/429，普通登录 403 不触发）；② 目标连接持续不可达（超时 / DNS 失败 / 代理网关 502、504 / code=000，connection refused 不算）。连续 2 次命中且过了冷却才切一次。"
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" onClick={saveClash} disabled={!loaded || savingClash}>
              保存 Clash 配置
            </Button>
            <Button type="button" variant="outline" onClick={rotateClash} disabled={!loaded || rotatingClash}>
              {rotatingClash ? "切换中…" : "立即切换出口"}
            </Button>
            {clashStatus?.node && (
              <span className="text-muted-foreground text-xs">
                当前节点：{clashStatus.node}
                {clashStatus.egress_ip ? ` · 出口 IP ${clashStatus.egress_ip}` : ""}
                {clashStatus.group ? ` · 组 ${clashStatus.group}` : ""}
              </span>
            )}
          </div>
          {clashStatus?.last_error && <p className="text-destructive text-xs">最近错误：{clashStatus.last_error}</p>}
          {egress && egress.tasks.length > 0 && (
            <div className="space-y-1 text-xs">
              <p className="text-muted-foreground">任务出口绑定（只读，来自服务端租约）</p>
              <ul className="space-y-0.5">
                {egress.tasks.map((t) => (
                  <li key={t.task_id} className="text-muted-foreground">
                    #{t.task_id} {t.name || ""} → {t.group}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {egress?.note && <p className="text-muted-foreground text-xs">{egress.note}</p>}
          {clashStatus?.rotated_at && (
            <p className="text-muted-foreground text-xs">
              最近切换：{new Date(clashStatus.rotated_at).toLocaleString()}
              {clashStatus.last_reason ? ` · ${clashStatus.last_reason}` : ""}
            </p>
          )}
        </Field>
      </SubBlock>
    </Section>
  );
}
