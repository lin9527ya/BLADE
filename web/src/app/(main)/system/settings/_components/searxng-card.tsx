"use client";

import * as React from "react";

import { PlayIcon, RefreshCwIcon, ServerIcon, SquareIcon } from "lucide-react";
import { toast } from "sonner";

import { Advanced, Field, SubBlock, SwitchRow } from "@/components/page-sections";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import {
  describeSearxngState,
  SEARXNG_ENGINE_PRESETS,
  SEARXNG_PYPI_PRESETS,
  validateSearxngForm,
} from "@/lib/search-helpers";
import type { SearxngEnginesPreset, SearxngManagedConfig, SearxngProxyMode, SearxngStatus } from "@/lib/types";

function statusBadgeVariant(
  tone: "ok" | "warn" | "info" | "error",
): "default" | "secondary" | "destructive" | "outline" {
  switch (tone) {
    case "ok":
      return "default";
    case "error":
      return "destructive";
    case "warn":
      return "secondary";
    default:
      return "outline";
  }
}

function buildActionButtons(opts: {
  st: SearxngStatus | null;
  busy: string;
  onStart: () => void;
  onStop: () => void;
  onReinstall: () => void;
  onActivate: () => void;
}) {
  const { st, busy, onStart, onStop, onReinstall, onActivate } = opts;
  if (!st) return null;
  const inFlight = st.state === "installing" || st.state === "starting" || busy !== "";
  const isCompose = st.mode === "compose";

  return (
    <>
      {!st.active_in_search && st.url && (
        <Button type="button" size="sm" variant="outline" disabled={busy !== ""} onClick={onActivate}>
          设为默认搜索源
        </Button>
      )}
      {st.active_in_search && (
        <Badge variant="outline" className="text-emerald-600 dark:text-emerald-400">
          已接入联网搜索
        </Badge>
      )}
      {!isCompose && !st.installed && (
        <Button type="button" size="sm" disabled={inFlight} onClick={onStart}>
          {inFlight ? <Spinner data-icon="inline-start" /> : <PlayIcon className="size-3.5" />}
          {st.state === "installing" ? "部署中…" : "一键部署并启动"}
        </Button>
      )}
      {!isCompose && st.installed && !st.running && (
        <>
          <Button type="button" size="sm" disabled={inFlight} onClick={onStart}>
            {inFlight ? <Spinner data-icon="inline-start" /> : <PlayIcon className="size-3.5" />}
            启动
          </Button>
          <Button type="button" size="sm" variant="outline" disabled={inFlight} onClick={onReinstall}>
            <RefreshCwIcon className="size-3.5" /> 更新/重装
          </Button>
        </>
      )}
      {!isCompose && st.running && (
        <>
          <Button type="button" size="sm" variant="outline" disabled={inFlight} onClick={onStart}>
            <RefreshCwIcon className="size-3.5" /> 重启生效
          </Button>
          <Button type="button" size="sm" variant="outline" disabled={busy !== ""} onClick={onStop}>
            <SquareIcon className="size-3.5" /> 停止
          </Button>
        </>
      )}
    </>
  );
}

export function SearxngCard({ onSearchActivated }: { onSearchActivated?: () => void }) {
  const [status, setStatus] = React.useState<SearxngStatus | null>(null);
  const [busy, setBusy] = React.useState<string>("");
  const [dirty, setDirty] = React.useState(false);

  const [portText, setPortText] = React.useState("8888");
  const [proxyMode, setProxyMode] = React.useState<SearxngProxyMode>("inherit");
  const [customProxy, setCustomProxy] = React.useState("");
  const [enginesPreset, setEnginesPreset] = React.useState<SearxngEnginesPreset>("security");
  const [repoURL, setRepoURL] = React.useState("https://github.com/searxng/searxng.git");
  const [pypiIndex, setPypiIndex] = React.useState("");
  const [installProxy, setInstallProxy] = React.useState("");
  const [autoSwitch, setAutoSwitch] = React.useState(true);

  const prevActiveRef = React.useRef<boolean | null>(null);
  const prevStateRef = React.useRef<string>("");
  const dirtyRef = React.useRef(dirty);
  dirtyRef.current = dirty;

  const syncDraft = React.useCallback((cfg: SearxngManagedConfig) => {
    setPortText(String(cfg.port || 8888));
    setProxyMode(cfg.proxy_mode);
    setCustomProxy(cfg.custom_proxy);
    setEnginesPreset(cfg.engines_preset);
    setRepoURL(cfg.repo_url || "https://github.com/searxng/searxng.git");
    setPypiIndex(cfg.pypi_index);
    setInstallProxy(cfg.install_proxy);
    setAutoSwitch(cfg.auto_switch);
    setDirty(false);
  }, []);

  const refreshStatus = React.useCallback(
    async (forceDraftSync = false) => {
      try {
        const next = await api.searxngStatus();
        setStatus(next);
        if (forceDraftSync || !dirtyRef.current) {
          syncDraft(next.config);
        }
        const transitionedToRunning =
          (prevStateRef.current === "installing" || prevStateRef.current === "starting") && next.state === "running";
        if (
          (transitionedToRunning || (prevActiveRef.current === false && next.active_in_search)) &&
          next.active_in_search
        ) {
          toast.success(`自建 SearXNG 已就绪并接入联网搜索（${next.url}）`);
          onSearchActivated?.();
        }
        if (
          (prevStateRef.current === "installing" || prevStateRef.current === "starting") &&
          next.state === "error" &&
          next.last_error
        ) {
          toast.error(`SearXNG 启动失败：${next.last_error}`);
        }
        prevStateRef.current = next.state;
        prevActiveRef.current = next.active_in_search;
      } catch {
        // 状态读取失败时静默保留旧状态，避免轮询刷屏 toast
      }
    },
    [onSearchActivated, syncDraft],
  );

  React.useEffect(() => {
    void refreshStatus(true);
  }, [refreshStatus]);

  React.useEffect(() => {
    if (!status || (status.state !== "installing" && status.state !== "starting")) {
      return;
    }
    const timer = window.setInterval(() => {
      void refreshStatus(false);
    }, 1500);
    return () => window.clearInterval(timer);
  }, [status, refreshStatus]);

  function currentDraftConfig(): SearxngManagedConfig {
    return {
      enabled: status?.config.enabled ?? false,
      port: Number.parseInt(portText.trim(), 10) || 8888,
      proxy_mode: proxyMode,
      custom_proxy: customProxy.trim(),
      engines_preset: enginesPreset,
      repo_url: repoURL.trim(),
      pypi_index: pypiIndex.trim(),
      install_proxy: installProxy.trim(),
      auto_switch: autoSwitch,
    };
  }

  async function saveDraftIfDirty(): Promise<boolean> {
    if (!dirty) return true;
    const cfg = currentDraftConfig();
    const err = validateSearxngForm(portText, cfg);
    if (err) {
      toast.error(err);
      return false;
    }
    try {
      const next = await api.saveSearxngConfig(cfg);
      setStatus(next);
      syncDraft(next.config);
      if (next.active_in_search) {
        onSearchActivated?.();
      }
      return true;
    } catch (e) {
      toast.error(`保存 SearXNG 配置失败：${(e as Error).message}`);
      return false;
    }
  }

  async function handleSaveConfig() {
    const cfg = currentDraftConfig();
    const err = validateSearxngForm(portText, cfg);
    if (err) {
      toast.error(err);
      return;
    }
    setBusy("save");
    try {
      const next = await api.saveSearxngConfig(cfg);
      setStatus(next);
      syncDraft(next.config);
      toast.success(next.running ? "已保存 SearXNG 配置并重建设置" : "已保存 SearXNG 配置");
      onSearchActivated?.();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  async function handleStart() {
    setBusy("start");
    try {
      if (!(await saveDraftIfDirty())) return;
      const next = await api.startSearxng({ auto_install: true });
      setStatus(next);
      prevStateRef.current = next.state;
      prevActiveRef.current = next.active_in_search;
      if (next.state === "running") {
        toast.success(`自建 SearXNG 已启动（${next.url}）`);
        onSearchActivated?.();
      } else {
        toast.info(next.state === "installing" ? "已开始安装并部署 SearXNG，请稍候…" : "正在启动 SearXNG…");
      }
    } catch (e) {
      toast.error(`启动失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  async function handleReinstall() {
    setBusy("install");
    try {
      if (!(await saveDraftIfDirty())) return;
      const next = await api.installSearxng(true);
      setStatus(next);
      prevStateRef.current = next.state;
      toast.info("已开始重新拉取并安装 SearXNG（Python venv）…");
    } catch (e) {
      toast.error(`重装失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  async function handleStop() {
    setBusy("stop");
    try {
      const next = await api.stopSearxng();
      setStatus(next);
      prevStateRef.current = next.state;
      toast.success("已停止本机 SearXNG sidecar");
    } catch (e) {
      toast.error(`停止失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  async function handleActivate() {
    setBusy("activate");
    try {
      const next = await api.activateSearxng();
      setStatus(next);
      toast.success(`已将联网搜索切换到自建 SearXNG（${next.url}）`);
      onSearchActivated?.();
    } catch (e) {
      toast.error(`切换失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  const stateInfo = describeSearxngState(status);
  const selectedPreset = SEARXNG_ENGINE_PRESETS.find((p) => p.value === enginesPreset) ?? SEARXNG_ENGINE_PRESETS[0];
  const isCompose = status?.mode === "compose";
  const inFlight = status?.state === "installing" || status?.state === "starting";

  return (
    <SubBlock
      title="自建 SearXNG（BLADE 内置托管）"
      icon={<ServerIcon className="size-4 text-muted-foreground" />}
      actions={buildActionButtons({
        st: status,
        busy,
        onStart: () => void handleStart(),
        onStop: () => void handleStop(),
        onReinstall: () => void handleReinstall(),
        onActivate: () => void handleActivate(),
      })}
    >
      <div className="flex flex-col gap-2 rounded-md border border-border/60 bg-muted/20 p-3 text-xs">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={statusBadgeVariant(stateInfo.tone)}>{stateInfo.badge}</Badge>
            {status?.url && <span className="font-mono text-muted-foreground">{status.url}</span>}
          </div>
          {status?.effective_proxy && (
            <span className="font-mono text-[11px] text-muted-foreground">出海代理：{status.effective_proxy}</span>
          )}
        </div>
        <p className="text-muted-foreground leading-relaxed">{stateInfo.summary}</p>
        {isCompose && (
          <p className="text-muted-foreground leading-relaxed">
            当前检测为 Docker Compose 伴生部署，<code>docker-compose.yml</code> 默认随 BLADE 一同启动{" "}
            <code>searxng</code> 容器并挂载 <code>./data/searxng</code>
            。修改下方引擎/代理配置点击保存后，如容器已启动，需执行 <code>docker compose restart searxng</code> 令新{" "}
            <code>settings.yml</code> 生效。
          </p>
        )}
        {inFlight && status?.install_step && (
          <div className="flex items-center gap-2 text-primary">
            <Spinner />
            <span>{status.install_step}</span>
          </div>
        )}
        {status?.last_error && (
          <p className="break-all rounded bg-destructive/10 px-2 py-1.5 font-mono text-[11px] text-destructive">
            {status.last_error}
          </p>
        )}
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        {!isCompose && (
          <Field
            label="本机监听端口（仅绑 127.0.0.1）"
            htmlFor="searxng-managed-port"
            hint="默认 8888，BLADE 会严格绑定 127.0.0.1 且禁用限流、自动启用 json 格式。"
          >
            <Input
              id="searxng-managed-port"
              inputMode="numeric"
              value={portText}
              disabled={!status || busy !== ""}
              onChange={(e) => {
                setPortText(e.target.value);
                setDirty(true);
              }}
            />
          </Field>
        )}

        <Field label="搜索引擎预设" htmlFor="searxng-preset" hint={selectedPreset.desc}>
          <Select
            value={enginesPreset}
            disabled={!status || busy !== ""}
            onValueChange={(v) => {
              setEnginesPreset(v as SearxngEnginesPreset);
              setDirty(true);
            }}
          >
            <SelectTrigger id="searxng-preset">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SEARXNG_ENGINE_PRESETS.map((p) => (
                <SelectItem key={p.value} value={p.value}>
                  {p.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Field
          label="SearXNG 出海代理策略"
          htmlFor="searxng-proxy-mode"
          hint="BLADE 请求本机 SearXNG 始终回环直连；此设置控制 SearXNG 向上游搜索引擎抓取时的出口代理。"
        >
          <Select
            value={proxyMode}
            disabled={!status || busy !== ""}
            onValueChange={(v) => {
              setProxyMode(v as SearxngProxyMode);
              setDirty(true);
            }}
          >
            <SelectTrigger id="searxng-proxy-mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="inherit">跟随 BLADE 搜索/平台出口代理池（推荐）</SelectItem>
              <SelectItem value="custom">自定义代理</SelectItem>
              <SelectItem value="direct">本机直连上游</SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {proxyMode === "custom" && (
          <Field
            label="自定义出海代理地址"
            htmlFor="searxng-custom-proxy"
            hint="支持 http://、https://、socks5://、socks5h://（如 socks5h://127.0.0.1:7897）。"
          >
            <Input
              id="searxng-custom-proxy"
              autoComplete="off"
              placeholder="socks5h://127.0.0.1:7897"
              value={customProxy}
              disabled={!status || busy !== ""}
              onChange={(e) => {
                setCustomProxy(e.target.value);
                setDirty(true);
              }}
            />
          </Field>
        )}
      </div>

      <SwitchRow
        id="searxng-auto-switch"
        label="部署 / 启动成功后自动将「联网搜索」切换到自建 SearXNG"
        hint="开启后，健康检查通过即自动开启总开关、将搜索来源切换为 SearXNG 并填入本机地址；关闭则只启停 sidecar，不影响上面当前使用的搜索引擎。"
        checked={autoSwitch}
        disabled={!status || busy !== ""}
        onChange={(v) => {
          setAutoSwitch(v);
          setDirty(true);
        }}
      />

      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={!status || busy !== ""}
          onClick={() => void handleSaveConfig()}
        >
          {busy === "save" ? <Spinner data-icon="inline-start" /> : null}
          保存 SearXNG 配置{dirty ? " *" : ""}
        </Button>
      </div>

      <Advanced title="高级：Git 源码镜像、PyPI 源与运行日志">
        {!isCompose && (
          <>
            <Field
              label="SearXNG Git 源码仓库地址"
              htmlFor="searxng-repo-url"
              hint="国内下载 GitHub 较慢时可替换为可用 Git 镜像或反代前缀（例如 https://gh-proxy.com/https://github.com/searxng/searxng.git）。"
            >
              <Input
                id="searxng-repo-url"
                autoComplete="off"
                value={repoURL}
                disabled={!status || busy !== ""}
                onChange={(e) => {
                  setRepoURL(e.target.value);
                  setDirty(true);
                }}
              />
            </Field>

            <Field label="PyPI 镜像源（可选，留空走官方）" htmlFor="searxng-pypi-index">
              <Input
                id="searxng-pypi-index"
                autoComplete="off"
                placeholder="https://pypi.tuna.tsinghua.edu.cn/simple"
                value={pypiIndex}
                disabled={!status || busy !== ""}
                onChange={(e) => {
                  setPypiIndex(e.target.value);
                  setDirty(true);
                }}
              />
              <div className="flex flex-wrap gap-1.5 pt-1">
                {SEARXNG_PYPI_PRESETS.map((preset) => (
                  <Button
                    key={preset.label}
                    type="button"
                    size="sm"
                    variant="outline"
                    className="h-7 px-2 text-xs"
                    disabled={!status || busy !== ""}
                    onClick={() => {
                      setPypiIndex(preset.url);
                      setDirty(true);
                    }}
                  >
                    {preset.label}
                  </Button>
                ))}
              </div>
            </Field>

            <Field
              label="安装阶段独立代理（可选，留空复用上方出海代理）"
              htmlFor="searxng-install-proxy"
              hint="用于 git clone 与 pip 安装依赖。"
            >
              <Input
                id="searxng-install-proxy"
                autoComplete="off"
                placeholder="http://127.0.0.1:7897"
                value={installProxy}
                disabled={!status || busy !== ""}
                onChange={(e) => {
                  setInstallProxy(e.target.value);
                  setDirty(true);
                }}
              />
            </Field>
          </>
        )}

        <div className="flex flex-wrap items-center justify-between gap-2 text-muted-foreground text-xs">
          <span>
            Python：{status?.python_bin ? `${status.python_bin} (${status.python_version ?? ""})` : "宿主由环境决定"} ·
            数据目录：<code>{status?.data_dir || "data/searxng"}</code>
          </span>
          <Button type="button" size="sm" variant="outline" onClick={() => void refreshStatus(false)}>
            <RefreshCwIcon className="size-3.5" /> 刷新状态与日志
          </Button>
        </div>

        {status?.install_log_tail && (
          <div className="grid gap-1.5">
            <span className="font-medium text-muted-foreground text-xs">最近安装日志</span>
            <pre className="max-h-44 overflow-auto rounded-md border bg-background/80 p-2 font-mono text-[11px] leading-relaxed">
              {status.install_log_tail}
            </pre>
          </div>
        )}

        {status?.log_tail && (
          <div className="grid gap-1.5">
            <span className="font-medium text-muted-foreground text-xs">最近运行日志</span>
            <pre className="max-h-44 overflow-auto rounded-md border bg-background/80 p-2 font-mono text-[11px] leading-relaxed">
              {status.log_tail}
            </pre>
          </div>
        )}
      </Advanced>
    </SubBlock>
  );
}
