"use client";

import { SearchIcon } from "lucide-react";

import { Advanced, Field, Section, SubBlock, SwitchRow } from "@/components/page-sections";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  backendLabel,
  describeChannelBackend,
  describeWebSearchProxyMode,
  formatWebSearchStats,
  WEB_SEARCH_BACKEND_OPTIONS,
} from "@/lib/search-helpers";
import type { WebSearchTestResult } from "@/lib/types";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 联网搜索。

function proxyToneClass(tone: "ok" | "warn" | "info"): string {
  switch (tone) {
    case "ok":
      return "border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";
    case "warn":
      return "border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400";
    default:
      return "border-border bg-muted/50 text-muted-foreground";
  }
}

function SearchTestResultCard({ result }: { result: WebSearchTestResult }) {
  const proxyInfo = result.proxy_mode ? describeWebSearchProxyMode(result.proxy_mode) : null;
  const ok = result.ok;

  return (
    <div
      className={
        ok
          ? "flex flex-col gap-1.5 rounded-md border border-emerald-500/40 bg-emerald-500/5 p-3 text-xs"
          : "flex flex-col gap-1.5 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-xs"
      }
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className={ok ? "font-medium text-emerald-600 dark:text-emerald-400" : "font-medium text-destructive"}>
          {ok ? `测试成功 · 返回 ${result.count ?? 0} 条结果` : "测试失败"}
        </span>
        <div className="flex flex-wrap items-center gap-2 text-muted-foreground">
          {result.backend && <span>执行通道：{backendLabel(result.backend)}</span>}
          {proxyInfo && (
            <span className={`rounded border px-1.5 py-0.5 text-[11px] ${proxyToneClass(proxyInfo.tone)}`}>
              {proxyInfo.badge}
            </span>
          )}
          {typeof result.elapsed_ms === "number" && <span>{result.elapsed_ms} ms</span>}
        </div>
      </div>

      {result.fallback_from && (
        <p className="text-amber-600 dark:text-amber-400">
          已由 <b>{backendLabel(result.fallback_from)}</b> 自动兜底
          {result.fallback_reason ? `：${result.fallback_reason}` : ""}
        </p>
      )}

      {!ok && result.error && (
        <p className="break-all rounded bg-background/60 px-2 py-1 font-mono text-[11px] text-destructive">
          {result.error}
        </p>
      )}

      {ok && result.sample && (
        <div className="rounded bg-background/60 px-2 py-1.5 text-muted-foreground">
          <div className="truncate font-medium text-foreground">{result.sample.title || "(无标题)"}</div>
          <div className="break-all font-mono text-[11px]">{result.sample.url}</div>
        </div>
      )}
    </div>
  );
}

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
    searxngURL,
    setSearxngURL,
    savingSearxngURL,
    saveSearxngURL,
    webSearchProxyMode,
    webSearchStats,
    lastSearchTest,
    proxyActive,
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
    tavilyNeedsKey,
    searxngNeedsURL,
  } = s;

  const channelInfo = describeChannelBackend(backend);
  const proxyModeInfo = describeWebSearchProxyMode(webSearchProxyMode, { activeProxy: proxyActive });
  const statsSummary = formatWebSearchStats(webSearchStats);

  return (
    <Section title="联网搜索" hint="联网搜索总开关、来源与独立出口代理配置；保存后实时生效，运行中 Agent 同步热更新。">
      <SubBlock title="网络搜索" icon={<SearchIcon className="size-4 text-muted-foreground" />}>
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
              （返回标题/链接/摘要，正文抓取由 WebFetch 负责）。网络搜索<b>不走</b>记录流量的 MITM
              代理，独立于流量捕获。
              <br />
              免费免 Key 可选 <b>DuckDuckGo（ddgs）</b>、<b>Brave 网页</b> 或 <b>自建 SearXNG</b>；API 通道可选{" "}
              <b>Brave API</b> / <b>Tavily</b> / <b>DeepSeek</b> / <b>OpenCode</b> / <b>Command Code</b>。
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
              <SelectTrigger className="w-full max-w-md">
                <SelectValue placeholder="选择来源" />
              </SelectTrigger>
              <SelectContent>
                {WEB_SEARCH_BACKEND_OPTIONS.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value}>
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}

        {webSearch && channelInfo && (
          <div className="flex flex-col gap-1.5 rounded-md border border-border/60 bg-muted/30 p-3">
            <p className="font-medium text-sm">{channelInfo.title}</p>
            <p className="text-muted-foreground text-xs leading-relaxed">{channelInfo.detail}</p>
            {channelInfo.warning && (
              <p className="text-amber-600 text-xs leading-relaxed dark:text-amber-400">{channelInfo.warning}</p>
            )}
          </div>
        )}

        {webSearch && backend === "brave-html" && (
          <div className="flex flex-col gap-1.5 rounded-md border border-border/60 bg-muted/30 p-3">
            <p className="font-medium text-sm">Brave 网页抓取（免费无 Key）</p>
            <p className="text-muted-foreground text-xs leading-relaxed">
              抓取 Brave 公开搜索结果页，<b>不是官方 API</b>，可能触发反爬验证或限流。 DDG
              限流或主通道失败时会自动用它兜底，并且默认跟随平台出口代理池；如需高稳定性建议配置 <b>Brave Search API</b>
              、<b>Tavily</b> 或 <b>自建 SearXNG</b>。
            </p>
          </div>
        )}

        {webSearch && backend === "searxng" && (
          <Field
            label="SearXNG 实例地址"
            htmlFor="searxng-url"
            hint={
              <>
                填写自建 SearXNG 根地址（如 <code>http://127.0.0.1:8888</code>），将调用{" "}
                <code>/search?format=json</code>（请确保 SearXNG 配置的 <code>search.formats</code> 已启用{" "}
                <code>json</code>）。默认直连实例或仅使用上方独立搜索代理，不经过全局出口代理池。
              </>
            }
          >
            <div className="flex items-center gap-2">
              <Input
                id="searxng-url"
                autoComplete="off"
                placeholder="http://127.0.0.1:8888"
                value={searxngURL}
                disabled={!loaded || savingSearxngURL}
                onChange={(e) => setSearxngURL(e.target.value)}
              />
              <Button type="button" onClick={saveSearxngURL} disabled={!loaded || savingSearxngURL}>
                保存
              </Button>
            </div>
            {searxngNeedsURL && (
              <p className="text-amber-500 text-xs">
                已选择 SearXNG 但尚未填写实例地址 —— 保存前主搜索将失败并尝试回落到兜底引擎。
              </p>
            )}
          </Field>
        )}

        {webSearch && backend === "brave-free" && (
          <Field
            label={
              <>
                Brave Search API Key
                {braveKeySet && <span className="ml-2 text-emerald-500 text-xs">已配置</span>}
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
              <p className="text-amber-500 text-xs">
                已选择 Brave API 但尚未配置 Key —— 请保存 Key，否则测试会报错、运行时将回落兜底引擎。
              </p>
            )}
          </Field>
        )}

        {webSearch && backend === "tavily" && (
          <Field
            label={
              <>
                Tavily Search API Key
                {tavilyKeySet && <span className="ml-2 text-emerald-500 text-xs">已配置</span>}
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
            {tavilyNeedsKey && (
              <p className="text-amber-500 text-xs">
                已选择 Tavily 但尚未配置 Key —— 请保存 Key，否则测试会报错、运行时将回落兜底引擎。
              </p>
            )}
          </Field>
        )}

        {webSearch && (
          <div className="flex flex-col gap-1.5 rounded-md border border-border/60 bg-muted/20 p-3 text-xs">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium text-foreground">当前搜索出口策略</span>
              <span
                className={`rounded border px-2 py-0.5 font-medium text-[11px] ${proxyToneClass(proxyModeInfo.tone)}`}
              >
                {proxyModeInfo.badge}
              </span>
            </div>
            <p className="text-muted-foreground leading-relaxed">{proxyModeInfo.detail}</p>
            {statsSummary && (
              <div className="mt-1 border-border/50 border-t pt-1.5 text-[11px] text-muted-foreground">
                <div>{statsSummary.summary}</div>
                {statsSummary.lastLine && <div className="mt-0.5 break-all">{statsSummary.lastLine}</div>}
              </div>
            )}
          </div>
        )}

        {webSearch && (
          <Advanced title="高级：独立出口代理">
            <Field
              label="独立搜索代理（可选）"
              htmlFor="ws-proxy"
              hint="仅用于覆盖搜索请求的代理（http/https/socks5）。留空时，ddgs / brave-html 与失败兜底抓取自动跟随平台出口代理池；带 Key / 自定义端点后端按独立代理或直连访问，均不走 MITM 流量代理。"
            >
              <div className="flex items-center gap-2">
                <Input
                  id="ws-proxy"
                  autoComplete="off"
                  placeholder="http://host:port 或 socks5://host:port（留空=自动策略）"
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
          <div className="flex flex-col gap-3 border-t pt-4">
            <div className="flex flex-wrap items-center justify-between gap-4">
              <p className="text-muted-foreground text-xs">
                用当前表单配置（来源 + 地址/Key + 代理）实时绕过缓存探测一次，查看实际执行通道、代理模式与耗时。
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
            {lastSearchTest && <SearchTestResultCard result={lastSearchTest} />}
          </div>
        )}
      </SubBlock>
    </Section>
  );
}
