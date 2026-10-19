"use client";

import * as React from "react";

import { SearchIcon } from "lucide-react";

import { Advanced, Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 联网搜索。

export function SearchSection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const {
    webSearch,
    setWebSearch,
    backend,
    setBackend,
    braveKeySet,
    braveKeyInput,
    setBraveKeyInput,
    tavilyKeySet,
    tavilyKeyInput,
    setTavilyKeyInput,
    savingTavilyKey,
    proxyInput,
    setProxyInput,
    savingProxy,
    testing,
    loaded,
    savingWebSearch,
    savingKey,
    saveWebSearch,
    saveBraveKey,
    saveTavilyKey,
    saveProxy,
    runTest,
    braveNeedsKey,
  } = s;

  return (
    <>
      <Section
        title="联网搜索"
        hint="这是网络搜索的总开关 + 来源配置；开关与来源改动会重建 Agent，Key 与代理立即生效。"
      >
        <SubBlock title="网络搜索" icon={<SearchIcon className="text-muted-foreground size-4" />}>
          <SwitchRow
            id="web-search"
            label={webSearch ? "总开关已开启 · 可在各 Agent 配置里单独启用" : "已关闭 · 各 Agent 无法启用网络搜索"}
            checked={webSearch}
            disabled={!loaded || savingWebSearch}
            onChange={(v) => {
              setWebSearch(v); // optimistic
              saveWebSearch({ web_search_enabled: v });
            }}
            hint={
              <>
                开启后，才能在<b>每个 Agent 的配置</b>里单独选择是否启用 <b>web_search</b>
                （仅返回标题/链接/摘要，不抓取正文；抓取由 WebFetch 负责）。网络搜索<b>不走</b>
                记录代理，独立于流量捕获。
                <br />
                来源可选 <b>DuckDuckGo（ddgs）</b>（无需 Key）、<b>Brave（免费版）</b>（需填写 Brave API Key）、
                <b>Tavily</b>（需填写 Tavily API Key）或 <b>DeepSeek</b>（复用当前 LLM 配置）。 总开关关闭时，各 Agent
                的网络搜索开关不可用。
              </>
            }
          />

          {webSearch && (
            <Field label="搜索来源">
              <Select
                value={backend}
                disabled={!loaded || savingWebSearch}
                onValueChange={(v) => {
                  setBackend(v); // optimistic
                  saveWebSearch({ web_search_backend: v });
                }}
              >
                <SelectTrigger className="w-72">
                  <SelectValue placeholder="选择来源" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ddgs">DuckDuckGo（ddgs · 免费无 Key）</SelectItem>
                  <SelectItem value="brave-free">Brave（免费版 · 需 Key）</SelectItem>
                  <SelectItem value="tavily">Tavily（需 Key）</SelectItem>
                  <SelectItem value="deepseek">DeepSeek（官方 · 复用激活配置）</SelectItem>
                  <SelectItem value="opencode">opencode（借用同名 LLM 配置 · flash）</SelectItem>
                  <SelectItem value="commandcode">commandcode（原生搜索通道）</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )}

          {webSearch && (backend === "deepseek" || backend === "opencode" || backend === "commandcode") && (
            <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
              <p className="text-sm font-medium">
                {backend === "deepseek" ? "DeepSeek 官方联网搜索" : `${backend} 联网搜索`}
              </p>
              <p className="text-muted-foreground text-xs leading-relaxed">
                {backend === "deepseek" ? (
                  <>
                    该来源直接复用<b>当前激活的 LLM 配置</b>。因此它<b>仅支持 DeepSeek 官方模型</b>，且该配置
                    <b>必须使用 anthropic 协议</b>
                    ——DeepSeek 的 OpenAI 协议端点不支持服务端搜索。切换 LLM 配置后此来源可能失效。
                  </>
                ) : backend === "opencode" ? (
                  <>
                    借用<b>名为 opencode 的 LLM 配置</b>的端点与 Key，走 Anthropic 兼容的
                    <code> /v1/messages</code> + 服务端 <code>web_search</code> 工具， 搜索模型
                    <b>固定 deepseek-v4-flash</b>（实测：同端点上 v4.1-flash 不执行服务端工具， 只会空想一轮）。配置里
                    <b>必须填会话头名字</b>（opencode 要求
                    <code> x-opencode-session</code>，缺了直接 400）且<b>协议为 anthropic</b>。 改配置后<b>立即生效</b>
                    。
                  </>
                ) : (
                  <>
                    走 commandcode 的<b>原生搜索通道</b>（<code>/alpha/web-search</code>）：
                    <b>与模型无关、与 LLM 配额解耦</b>——它没有"消息 + 工具"这条路 （<code>/messages</code> 只服务 Claude
                    系，deepseek 只在
                    <code>/responses</code> 上且拒收内置 web_search 工具）。只需该配置的 端点与 Key 可用，
                    <b>不用管它选的模型与协议</b>。
                  </>
                )}
              </p>
              <p className="text-muted-foreground text-xs leading-relaxed">
                与其它来源不同，搜索由 <b>DeepSeek 服务端执行</b>：每次搜索会额外消耗一次模型调用（产生 Token
                费用），搜索请求<b>不经过上面的出口代理</b>，也<b>不计入流量留痕</b>；返回结果
                <b>只有标题和链接</b>（无摘要），需要正文时由 WebFetch 抓取。
              </p>
              <p className="text-muted-foreground text-xs leading-relaxed">
                是否满足上述条件由你自行确认，系统不做拦截；可用下方「测试搜索」按钮实际跑一次来验证。
              </p>
            </div>
          )}

          {webSearch && backend === "brave-free" && (
            <Field
              label={
                <>
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                </>
              }
              htmlFor="brave-key"
              hint={<>免费版额度约 2,000 次/月。前往 https://brave.com/search/api/ 获取 Key。</>}
            >
              <div className="flex items-center gap-2">
                <Input
                  id="brave-key"
                  type="password"
                  autoComplete="off"
                  placeholder={braveKeySet ? "已配置（留空则不变）" : "输入 Brave API Key"}
                  value={braveKeyInput}
                  disabled={!loaded || savingKey}
                  onChange={(e) => setBraveKeyInput(e.target.value)}
                />
                <Button
                  type="button"
                  onClick={saveBraveKey}
                  disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                >
                  保存
                </Button>
              </div>
              {braveNeedsKey && (
                <p className="text-xs text-amber-500">
                  已选择 Brave 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                </p>
              )}
            </Field>
          )}

          {webSearch && backend === "tavily" && (
            <Field
              label={
                <>
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                </>
              }
              htmlFor="tavily-key"
              hint={<>前往 https://tavily.com 注册并获取 API Key。</>}
            >
              <div className="flex items-center gap-2">
                <Input
                  id="tavily-key"
                  type="password"
                  autoComplete="off"
                  placeholder={tavilyKeySet ? "已配置（留空则不变）" : "输入 Tavily API Key（tvly-…）"}
                  value={tavilyKeyInput}
                  disabled={!loaded || savingTavilyKey}
                  onChange={(e) => setTavilyKeyInput(e.target.value)}
                />
                <Button
                  type="button"
                  onClick={saveTavilyKey}
                  disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                >
                  保存
                </Button>
              </div>
              {!tavilyKeySet && (
                <p className="text-xs text-amber-500">
                  已选择 Tavily 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                </p>
              )}
            </Field>
          )}

          {webSearch && (
            <Advanced title="高级：出口代理">
              <Field
                label="出口代理（可选）"
                htmlFor="ws-proxy"
                hint="独立出口代理，仅用于访问搜索端点（VPN/SOCKS 等）。与记录流量的 MITM 代理无关；网络不通时经此代理访问。"
              >
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port 或 socks5://host:port（留空=直连）"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    保存
                  </Button>
                </div>
              </Field>
            </Advanced>
          )}

          {webSearch && (
            <div className="flex flex-wrap items-center justify-between gap-4 border-t pt-4">
              <p className="text-muted-foreground text-xs">
                用当前配置（来源 + 代理 + Key）实际搜索一次「test」，验证是否可用。
              </p>
              <Button
                type="button"
                variant="outline"
                onClick={runTest}
                disabled={!loaded || testing}
                className="shrink-0"
              >
                {testing ? "测试中…" : "测试搜索"}
              </Button>
            </div>
          )}
        </SubBlock>
      </Section>
    </>
  );
}
