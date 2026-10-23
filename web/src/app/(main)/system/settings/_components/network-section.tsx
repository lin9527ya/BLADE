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
    refreshProxyStatus,
    savingGlobalProxy,
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
      </Section>
    </>
  );
}
