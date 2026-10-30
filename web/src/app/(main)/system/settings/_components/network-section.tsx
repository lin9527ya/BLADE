"use client";

import * as React from "react";

import { RadioTowerIcon } from "lucide-react";

import { Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 网络与代理。

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
    proxyFailThreshold,
    setProxyFailThreshold,
    proxyActive,
    proxyBlocked,
    proxyStatus,
    proxySaveError,
    proxyEgressIPURL,
    setProxyEgressIPURL,
    proxyEgress,
    checkingProxyEgress,
    checkProxyEgress,
    refreshProxyStatus,
    savingGlobalProxy,
    clashApiURL,
    setClashApiURL,
    clashSecretInput,
    setClashSecretInput,
    clashSecretSet,
    clashGroup,
    setClashGroup,
    clashAuto,
    setClashAuto,
    clashCooldown,
    setClashCooldown,
    clashStatus,
    savingClash,
    rotatingClash,
    saveClash,
    rotateClash,
    loaded,
    savingTraffic,
    toggleTraffic,
    toggleAgentTrafficBinding,
    saveGlobalProxy,
  } = s;

  return (
    <>
      <Section title="网络与代理" hint="本区改动会重建 Agent 生效（运行中的任务不受影响）。">
        <SubBlock title="流量捕获" icon={<RadioTowerIcon className="text-muted-foreground size-4" />}>
          <SwitchRow
            id="traffic-capture"
            label={trafficCapture ? "已开启 · 正在记录流量并注入代理" : "已关闭 · 不记录、不注入代理"}
            checked={trafficCapture}
            disabled={!loaded || savingTraffic}
            onChange={toggleTraffic}
            hint={
              <>
                开启后，所有 Agent 的 HTTP 流量经记录代理全量落库，并向 Agent 注入 traffic_search / traffic_get
                工具与代理配置（提示词含代理说明）。
                <br />
                关闭（默认）时不记录任何流量：Agent
                <b>不会</b>拿到代理配置与流量工具，提示词也<b>不含</b>代理相关内容。
              </>
            }
          />
        </SubBlock>

        <SubBlock title="Agent 自动绑定流量" icon={<RadioTowerIcon className="text-muted-foreground size-4" />}>
          <SwitchRow
            id="agent-traffic-binding"
            describedBy="agent-traffic-binding-description"
            label={agentTrafficBinding ? "已开启 · 会增加 Token 消耗" : "已关闭 · 可继续人工绑定"}
            checked={agentTrafficBinding}
            disabled={!loaded || savingTraffic}
            onChange={toggleAgentTrafficBinding}
            hint={
              <>
                默认关闭。开启后，漏洞入库时触发的报告 Agent 会核对已有 HTTP 请求/响应，关联对应流量后再编写报告。
                <b>查阅数据包及额外的工具调用会增加 Token 消耗。</b>
                <br />
                TCP、未抓包或没有匹配流量时仍可正常上报。此开关不影响流量捕获、人工绑定及已保存证据的查看。 对下一轮
                Agent 生效；关闭后会立即拒绝新的自动绑定。
              </>
            }
          />
        </SubBlock>

        <SubBlock title="全局代理池" icon={<RadioTowerIcon className="text-muted-foreground size-4" />}>
          <Field
            label="代理池（每行一个）"
            htmlFor="global-proxy-pool"
            hint={
              <>
                支持 <code>http://</code> / <code>https://</code> / <code>socks5://</code> / <code>socks5h://</code>（可带 user:pass）。
                Clash 直接用混合端口，例如 <code>http://127.0.0.1:7890</code>。
                <br />
                服务端按健康检查自动切换；<b>全部不可用时出口会被阻断（不会直连）</b>，避免泄漏真实 IP。
                <br />
                代理池只切换你填的多个地址；<b>只填一个 Clash 端口时，Clash 内部节点由 Clash 的代理组
                （select / url-test / fallback）决定，BLADE 不会替你切换 Clash 节点</b>。
                LLM 的代理在各自 profile 里配置，不在此池内。
              </>
            }
          >
            <Textarea
              id="global-proxy-pool"
              rows={4}
              autoComplete="off"
              placeholder={"http://127.0.0.1:7890\nsocks5://127.0.0.1:7891"}
              value={globalProxyPoolInput}
              disabled={!loaded || savingGlobalProxy}
              onChange={(e) => setGlobalProxyPoolInput(e.target.value)}
            />
            <div className="grid gap-3 md:grid-cols-[1fr_110px_110px]">
              <div className="space-y-1.5">
                <span className="text-muted-foreground text-xs">健康检查 URL</span>
                <Input
                  id="proxy-health-url"
                  autoComplete="off"
                  placeholder="https://www.gstatic.com/generate_204"
                  value={proxyHealthURL}
                  disabled={!loaded || savingGlobalProxy}
                  onChange={(e) => setProxyHealthURL(e.target.value)}
                />
              </div>
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
                  {checkingProxyEgress ? "检测中…" : "检测出口 IP"}
                </Button>
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                保存
              </Button>
              <Button type="button" variant="outline" onClick={refreshProxyStatus} disabled={!loaded}>
                刷新状态
              </Button>
              <span className={proxyBlocked ? "text-destructive text-xs" : "text-muted-foreground text-xs"}>
                {globalProxyPoolInput.trim() !== proxySavedPool.trim()
                  ? "有未保存的修改 · 点「保存」生效"
                  : globalProxyPoolInput.trim() && proxyStatus.length === 0
                    ? "正在检测代理…"
                    : proxyBlocked
                      ? "全部代理不可用：出口已阻断（不会直连）"
                      : proxyActive
                        ? `当前出口：${proxyActive}`
                        : globalProxyPoolInput.trim()
                          ? "正在检测代理…"
                          : "未配置 · 目标流量直连"}
              </span>
            </div>
            {proxySaveError && <p className="text-destructive text-xs">保存失败：{proxySaveError}</p>}
            {proxyEgress.length > 0 && (
              <ul className="space-y-0.5 text-xs">
                {proxyEgress.map((r) => (
                  <li key={r.proxy} className={r.error || r.leaked ? "text-destructive" : "text-muted-foreground"}>
                    {r.leaked ? "⚠" : r.error ? "✗" : "✓"} {r.proxy}
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
            {proxyStatus.length > 0 && (
              <ul className="space-y-0.5 text-xs">
                {proxyStatus.map((h) => (
                  <li key={h.proxy} className={h.ok ? "text-muted-foreground" : "text-destructive"}>
                    {h.ok ? "✓" : "✗"} {h.proxy}
                    {h.ok && h.latency_ms ? ` · ${h.latency_ms}ms` : ""}
                    {!h.ok && h.error ? ` · ${h.error}` : ""}
                  </li>
                ))}
              </ul>
            )}
          </Field>
        </SubBlock>

        <SubBlock title="Clash 出口切换" icon={<RadioTowerIcon className="text-muted-foreground size-4" />}>
          <Field
            label="Clash 外部控制器"
            htmlFor="clash-api-url"
            hint={
              <>
                目标 WAF 封的是**出口 IP**，代理端口本身仍然健康，代理池不会因此切换。开启 Clash 的
                <code> external-controller</code> 后，BLADE 可在 Selector 组里换节点并验证出口 IP 已变化。
                <br />
                安全约束：只选非 <code>DIRECT/REJECT</code> 节点；切换后经当前代理查出口 IP，必须变化且不是本机地址；失败不直连。
                <br />
                强烈建议给 BLADE 单独建一个 <code>type: mixed</code> 的 listener，并用 <code>proxy: 组名</code> 强制走该 Selector 组——否则 Clash 规则仍可能把目标判成 DIRECT，换节点也救不了真实 IP。
                <br />
                Clash 当前是 <code>global</code> 模式时，真正决定所有出口的是 <b>GLOBAL</b> 组：组名必须填 <code>GLOBAL</code>（或改用规则模式/专用 listener）；填其它组名只会改那个组，不会改变实际流量。
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
                <span className="text-muted-foreground text-xs">Selector 组名（留空 = 自动选第一个）</span>
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
            <SwitchRow
              id="clash-auto-rotate"
              label={clashAuto ? "自动：检测到强 WAF 特征后自动切换" : "自动切换已关闭（仍可用工具/按钮手动切）"}
              checked={clashAuto}
              disabled={!loaded || savingClash}
              onChange={() => setClashAuto((v) => !v)}
              hint="只认 Cloudflare / Incapsula / Akamai / Sucuri / AWS WAF / ModSecurity 等强指纹 + 403/429；普通登录 403 不触发。连续 2 次命中且过了冷却才切一次。"
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
            {clashStatus?.rotated_at && (
              <p className="text-muted-foreground text-xs">
                最近切换：{new Date(clashStatus.rotated_at).toLocaleString()}
                {clashStatus.last_reason ? ` · ${clashStatus.last_reason}` : ""}
              </p>
            )}
          </Field>
        </SubBlock>
      </Section>
    </>
  );
}
