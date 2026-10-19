"use client";

import * as React from "react";

import { RadioTowerIcon } from "lucide-react";

import { Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 网络与代理。

export function NetworkSection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const {
    trafficCapture,
    agentTrafficBinding,
    globalProxyInput,
    setGlobalProxyInput,
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

        <SubBlock title="全局代理" icon={<RadioTowerIcon className="text-muted-foreground size-4" />}>
          <Field
            label="代理地址"
            htmlFor="global-proxy"
            hint={
              <>
                开启<b>流量捕获</b>时，它作为记录代理的<b>上游</b>（流量仍全量落库，再经此代理出网）；关闭捕获时，
                直接注入 Agent 的 bash / WebFetch 出网。与网络搜索代理、LLM 代理相互独立。
                <br />
                <b>提示</b>：socks5 在<b>关闭捕获</b>时依赖各命令行工具对 <code>ALL_PROXY</code> 的支持（curl
                可用，部分工具可能忽略）；若主要用 socks5，建议开启流量捕获——此路径由 MITM
                亲自拨号，工具无感知、稳定生效。
              </>
            }
          >
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder="socks5://user:pass@host:1080 或 http://host:port（留空=直连）"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                保存
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "已配置 · 所有目标流量经此代理出网" : "未配置 · 目标流量直连出网"}
            </p>
          </Field>
        </SubBlock>
      </Section>
    </>
  );
}
