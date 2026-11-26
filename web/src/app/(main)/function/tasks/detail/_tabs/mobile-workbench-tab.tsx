"use client";

import * as React from "react";

import {
  CameraIcon,
  CheckCircle2Icon,
  FolderOpenIcon,
  KeyRoundIcon,
  PlayIcon,
  RefreshCwIcon,
  ShieldAlertIcon,
  SmartphoneIcon,
  SquareIcon,
  TerminalIcon,
  UploadIcon,
  WifiIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { deviceCapabilitySummary, formatSampleBytes, uiElementDisplayLabel } from "@/lib/mobile-utils";
import type {
  FridaScriptSpec,
  MobileDevice,
  MobileIPCFuzzReport,
  MobileIPCProbeResult,
  MobileLogcatAuditReport,
  MobileSandboxEntry,
  MobileStorageAuditReport,
  MobileUICrawlResult,
  MobileUIElement,
  TaskMobileState,
} from "@/lib/types";

function staticSeverityVariant(sev: string): "destructive" | "default" | "outline" {
  if (sev === "high") return "destructive";
  if (sev === "medium") return "default";
  return "outline";
}

function ipcKindForType(type: string): string {
  if (type === "provider") return "provider";
  if (type === "receiver") return "broadcast";
  if (type === "service") return "service";
  return "activity";
}

export function MobileWorkbenchTab({ taskId }: { taskId: string }) {
  const [state, setState] = React.useState<TaskMobileState | null>(null);
  const [devices, setDevices] = React.useState<MobileDevice[]>([]);
  const [adbWarn, setAdbWarn] = React.useState("");
  const [scripts, setScripts] = React.useState<FridaScriptSpec[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState("");

  // Upload APK to existing task
  const [uploadPct, setUploadPct] = React.useState<number | null>(null);
  const sampleInputRef = React.useRef<HTMLInputElement>(null);

  // Device connect / bind form
  const [wifiEndpoint, setWifiEndpoint] = React.useState("");
  const [pairEndpoint, setPairEndpoint] = React.useState("");
  const [pairCode, setPairCode] = React.useState("");
  const [selectedSerial, setSelectedSerial] = React.useState("");

  // Candidate domain scope approval
  const [checkedDomains, setCheckedDomains] = React.useState<string[]>([]);

  // Frida hooks
  const [fridaMode, setFridaMode] = React.useState<"spawn" | "attach">("spawn");
  const [fridaModules, setFridaModules] = React.useState<string[]>(["ssl_unpinning", "crypto_monitor"]);
  const [fridaCustomJS, setFridaCustomJS] = React.useState("");
  const [rpcMethod, setRpcMethod] = React.useState("");
  const [rpcArgsText, setRpcArgsText] = React.useState("[]");
  const [rpcOutput, setRpcOutput] = React.useState("");

  // Screen & UI automation
  const [screenPng, setScreenPng] = React.useState("");
  const screenImgRef = React.useRef<HTMLImageElement>(null);
  const [uiElements, setUIElements] = React.useState<MobileUIElement[]>([]);
  const [uiInputText, setUIInputText] = React.useState("");
  const [deeplinkInput, setDeeplinkInput] = React.useState("");
  const [crawlResult, setCrawlResult] = React.useState<MobileUICrawlResult | null>(null);

  // IPC probe & fuzz results
  const [ipcResult, setIPCResult] = React.useState<MobileIPCProbeResult | null>(null);
  const [ipcFuzz, setIPCFuzz] = React.useState<MobileIPCFuzzReport | null>(null);

  // Sandbox browser, storage & logcat audits
  const [sandboxPath, setSandboxPath] = React.useState("");
  const [sandboxEntries, setSandboxEntries] = React.useState<MobileSandboxEntry[]>([]);
  const [sandboxPreview, setSandboxPreview] = React.useState("");
  const [storageAudit, setStorageAudit] = React.useState<MobileStorageAuditReport | null>(null);
  const [logcatAudit, setLogcatAudit] = React.useState<MobileLogcatAuditReport | null>(null);

  const refreshState = React.useCallback(async () => {
    try {
      const [st, devRes, sc] = await Promise.all([
        api.taskMobileState(taskId),
        api.mobileDevices().catch(() => ({ devices: [] as MobileDevice[], adb_warning: "" })),
        api.fridaScripts().catch(() => []),
      ]);
      setState(st);
      setDevices(devRes.devices ?? []);
      setAdbWarn(devRes.adb_warning ?? "");
      setScripts(sc);
      if (st.mobile_device_id) {
        setSelectedSerial(st.mobile_device_id);
      } else if (!selectedSerial && devRes.devices?.length) {
        setSelectedSerial(devRes.devices[0].serial);
      }
      if (st.manifest?.candidate_domains?.length) {
        const existing = new Set(st.scope_hosts ?? []);
        setCheckedDomains(st.manifest.candidate_domains.filter((d) => !existing.has(d.toLowerCase())));
      }
    } catch (e) {
      toast.error(`加载移动端状态失败：${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, [taskId, selectedSerial]);

  React.useEffect(() => {
    void refreshState();
  }, [refreshState]);

  usePolling(
    async (alive) => {
      const st = await api.taskMobileState(taskId);
      if (alive()) setState(st);
    },
    8000,
    [taskId],
  );

  async function runAction(label: string, fn: () => Promise<void>) {
    setBusy(label);
    try {
      await fn();
      await refreshState();
    } catch (e) {
      toast.error(`${label}失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  async function handleSampleUpload(files: FileList | null) {
    const file = files?.[0];
    if (!file) return;
    setUploadPct(0);
    try {
      await api.uploadAppPackage("task", taskId, file, (pct) => setUploadPct(pct));
      toast.success(`已上传并解析样本：${file.name}`);
      await refreshState();
    } catch (e) {
      toast.error(`上传安装包失败：${(e as Error).message}`);
    } finally {
      setUploadPct(null);
      if (sampleInputRef.current) sampleInputRef.current.value = "";
    }
  }

  async function refreshScreen() {
    setBusy("截屏");
    try {
      const res = await api.taskMobileScreen(taskId);
      setScreenPng(res.png_base64);
      setUIElements(res.ui_tree?.elements ?? []);
    } catch (e) {
      toast.error(`读取设备屏幕失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  async function handleImageTap(ev: React.MouseEvent<HTMLButtonElement>) {
    const img = screenImgRef.current;
    if (!img) return;
    const rect = img.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const x = Math.round(((ev.clientX - rect.left) / rect.width) * img.naturalWidth);
    const y = Math.round(((ev.clientY - rect.top) / rect.height) * img.naturalHeight);
    await runAction("点击坐标", async () => {
      await api.runTaskMobileUI(taskId, { action: "tap", x, y });
      toast.success(`已点击 (${x}, ${y})`);
    });
    void refreshScreen();
  }

  async function openSandbox(path: string) {
    setBusy("沙箱读取");
    try {
      const res = await api.taskMobileSandbox(taskId, path);
      setSandboxPath(res.path);
      setSandboxEntries(res.entries ?? []);
      setSandboxPreview(res.preview ?? "");
    } catch (e) {
      toast.error(`读取 Root 沙箱失败：${(e as Error).message}`);
    } finally {
      setBusy("");
    }
  }

  const manifest = state?.manifest;
  const bound = state?.bound_device;
  const scopeSet = new Set((state?.scope_hosts ?? []).map((h) => h.toLowerCase()));

  if (loading && !state) {
    return <div className="py-12 text-center text-muted-foreground text-sm">正在加载移动端工作台…</div>;
  }

  return (
    <div className="grid gap-4 lg:grid-cols-12">
      {/* Left column: Sample Static Profile & Candidate Scope + Frida + Sandbox */}
      <div className="grid gap-4 lg:col-span-7">
        {/* 1. Package & Static Assessment */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between gap-2 pb-3">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <SmartphoneIcon className="size-4" />
                应用样本与静态体检
              </CardTitle>
              <CardDescription>支持最大 1024 MiB APK / AAB / XAPK / IPA 流式上传与纯原生解析</CardDescription>
            </div>
            <div className="flex items-center gap-1.5">
              {manifest && state?.app_sample_paths?.length ? (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy !== ""}
                  onClick={() =>
                    void runAction("SAST 复检", async () => {
                      await api.runTaskMobileStaticAudit(taskId);
                      toast.success("已完成深度静态 SAST/二进制安全体检");
                    })
                  }
                >
                  深度 SAST 体检
                </Button>
              ) : null}
              <input
                ref={sampleInputRef}
                type="file"
                accept=".apk,.aab,.xapk,.apkm,.apks,.ipa"
                className="hidden"
                onChange={(e) => void handleSampleUpload(e.target.files)}
              />
              <Button
                size="sm"
                variant="outline"
                disabled={uploadPct !== null}
                onClick={() => sampleInputRef.current?.click()}
              >
                <UploadIcon className="size-3.5" />
                {uploadPct !== null ? `上传中 ${uploadPct}%` : "上传安装包"}
              </Button>
            </div>
          </CardHeader>
          <CardContent className="grid gap-3 text-sm">
            {manifest ? (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="secondary" className="font-mono">
                    {manifest.package_name || manifest.sample_name}
                  </Badge>
                  <Badge variant="outline">{manifest.platform.toUpperCase()}</Badge>
                  {manifest.version_name && <Badge variant="outline">v{manifest.version_name}</Badge>}
                  <span className="text-muted-foreground text-xs">{formatSampleBytes(manifest.size_bytes)}</span>
                  {manifest.frameworks?.map((fw) => (
                    <Badge key={fw} className="bg-sky-500/15 text-sky-600 dark:text-sky-400">
                      {fw}
                    </Badge>
                  ))}
                  {manifest.packers?.map((pk) => (
                    <Badge key={pk} className="bg-amber-500/15 text-amber-600 dark:text-amber-400">
                      壳: {pk}
                    </Badge>
                  ))}
                  {manifest.debuggable && <Badge variant="destructive">debuggable=true</Badge>}
                  {manifest.cleartext_traffic && <Badge variant="destructive">cleartext=true</Badge>}
                  {manifest.fairplay_encrypted && <Badge variant="secondary">FairPlay 加密</Badge>}
                  {manifest.hermes_bytecode && <Badge variant="secondary">Hermes Bytecode</Badge>}
                  {manifest.signature_schemes && manifest.signature_schemes.length > 0 && (
                    <Badge variant="outline">签名: {manifest.signature_schemes.join("/")}</Badge>
                  )}
                </div>

                {manifest.static_findings && manifest.static_findings.length > 0 && (
                  <div className="grid gap-1.5 rounded-md border p-2.5">
                    <div className="flex items-center justify-between text-xs font-medium">
                      <span>静态 SAST 与二进制安全审计 ({manifest.static_findings.length})</span>
                      {manifest.sbom_hints && manifest.sbom_hints.length > 0 && (
                        <span className="truncate font-mono text-[10px] text-muted-foreground">
                          SBOM: {manifest.sbom_hints.slice(0, 4).join(", ")}
                        </span>
                      )}
                    </div>
                    <div className="max-h-40 space-y-1 overflow-auto">
                      {manifest.static_findings.map((sf) => (
                        <div
                          key={`${sf.id}:${sf.location ?? ""}`}
                          className="flex items-start justify-between gap-2 rounded bg-muted/40 px-2 py-1 text-xs"
                        >
                          <div className="min-w-0">
                            <div className="flex flex-wrap items-center gap-1.5">
                              <Badge variant={staticSeverityVariant(sf.severity)} className="text-[10px] uppercase">
                                {sf.severity}
                              </Badge>
                              {sf.masvs && (
                                <span className="font-mono text-[10px] text-muted-foreground">{sf.masvs}</span>
                              )}
                              <span className="font-medium">{sf.title}</span>
                            </div>
                            {sf.recommendation && (
                              <div className="mt-0.5 text-[11px] text-muted-foreground">{sf.recommendation}</div>
                            )}
                          </div>
                        </div>
                      ))}
                    </div>
                    {manifest.native_binaries && manifest.native_binaries.length > 0 && (
                      <div className="mt-1 flex flex-wrap gap-1.5 text-[11px]">
                        {manifest.native_binaries.slice(0, 8).map((nb) => (
                          <Badge key={nb.path} variant="outline" className="font-mono text-[10px]">
                            {nb.path.split("/").pop()} [{nb.format.toUpperCase()} {nb.pie ? "PIE" : "NoPIE"}{" "}
                            {nb.nx ? "NX" : "NoNX"} {nb.stack_canary ? "Canary" : "NoCanary"}
                            {nb.relro ? ` RELRO:${nb.relro}` : ""}]
                          </Badge>
                        ))}
                      </div>
                    )}
                  </div>
                )}

                {manifest.warnings && manifest.warnings.length > 0 && (
                  <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-2.5 text-xs text-amber-700 dark:text-amber-300">
                    {manifest.warnings.map((w) => (
                      <div key={w}>• {w}</div>
                    ))}
                  </div>
                )}

                {/* Candidate API Domains & Scope Approval */}
                {manifest.candidate_domains && manifest.candidate_domains.length > 0 && (
                  <div className="grid gap-2 rounded-md border p-3">
                    <div className="flex items-center justify-between">
                      <span className="font-medium text-xs">静态提取后端候选域名</span>
                      <Button
                        size="xs"
                        disabled={checkedDomains.length === 0 || busy !== ""}
                        onClick={() =>
                          void runAction("纳入授权范围", async () => {
                            const r = await api.approveTaskMobileScope(taskId, checkedDomains);
                            toast.success(`已将 ${r.added} 个域名纳入任务授权范围`);
                          })
                        }
                      >
                        将已选纳入任务范围
                      </Button>
                    </div>
                    <div className="flex flex-wrap gap-2">
                      {manifest.candidate_domains.map((dom) => {
                        const inScope = scopeSet.has(dom.toLowerCase());
                        const checked = checkedDomains.includes(dom);
                        return (
                          <label
                            key={dom}
                            className="flex items-center gap-1.5 rounded border px-2 py-1 font-mono text-xs"
                          >
                            <Checkbox
                              disabled={inScope}
                              checked={inScope || checked}
                              onCheckedChange={(v) => {
                                setCheckedDomains((prev) => (v ? [...prev, dom] : prev.filter((x) => x !== dom)));
                              }}
                            />
                            <span>{dom}</span>
                            {inScope && <Badge variant="secondary">已授权</Badge>}
                          </label>
                        );
                      })}
                    </div>
                  </div>
                )}

                {/* Exported Components & One-Click IPC Probe */}
                {manifest.exported_components && manifest.exported_components.length > 0 && (
                  <div className="grid gap-2">
                    <div className="flex items-center justify-between">
                      <span className="font-medium text-xs">
                        导出组件与 DeepLink ({manifest.exported_components.length})
                      </span>
                      <div className="flex items-center gap-1.5">
                        <Input
                          className="h-7 w-48 font-mono text-xs"
                          placeholder="scheme://host/path"
                          value={deeplinkInput}
                          onChange={(e) => setDeeplinkInput(e.target.value)}
                        />
                        <Button
                          size="xs"
                          variant="outline"
                          disabled={!deeplinkInput.trim() || !state?.mobile_device_id || busy !== ""}
                          onClick={() =>
                            void runAction("DeepLink 探测", async () => {
                              const r = await api.runTaskMobileIPCProbe(taskId, {
                                kind: "deeplink",
                                uri: deeplinkInput.trim(),
                              });
                              setIPCResult(r);
                            })
                          }
                        >
                          调起 DeepLink
                        </Button>
                        <Button
                          size="xs"
                          variant="secondary"
                          disabled={!state?.mobile_device_id || busy !== ""}
                          onClick={() =>
                            void runAction("批量 IPC Fuzz", async () => {
                              const rep = await api.runTaskMobileIPCFuzz(taskId, 25);
                              setIPCFuzz(rep);
                              toast.success(
                                `IPC 批量探测完成：共 ${rep.probed_count} 项，可直接访问 ${rep.accessible_count} 项`,
                              );
                            })
                          }
                        >
                          一键批量 Fuzz
                        </Button>
                      </div>
                    </div>
                    <div className="max-h-44 space-y-1 overflow-auto rounded-md border p-2">
                      {manifest.exported_components.map((c) => (
                        <div key={`${c.type}:${c.name}`} className="flex items-center justify-between gap-2 text-xs">
                          <div className="min-w-0 truncate font-mono">
                            <Badge variant="outline" className="mr-1.5 text-[10px]">
                              {c.type}
                            </Badge>
                            {c.name}
                          </div>
                          {(c.type === "activity" ||
                            c.type === "service" ||
                            c.type === "provider" ||
                            c.type === "receiver") && (
                            <Button
                              size="xs"
                              variant="ghost"
                              disabled={!state?.mobile_device_id || busy !== ""}
                              onClick={() =>
                                void runAction("IPC 探测", async () => {
                                  const kind = ipcKindForType(c.type);
                                  const res = await api.runTaskMobileIPCProbe(taskId, {
                                    kind,
                                    component: kind === "provider" ? undefined : c.name,
                                    uri:
                                      kind === "provider" && c.authorities ? `content://${c.authorities}/` : undefined,
                                  });
                                  setIPCResult(res);
                                })
                              }
                            >
                              探测
                            </Button>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {ipcFuzz && (
                  <div className="rounded-md border bg-muted/30 p-2.5 text-xs">
                    <div className="mb-1 flex items-center justify-between font-medium">
                      <span>
                        批量 IPC 边界测试：已测 {ipcFuzz.probed_count} · 可访问 {ipcFuzz.accessible_count} · 拒绝{" "}
                        {ipcFuzz.denied_count}
                      </span>
                    </div>
                    <div className="max-h-32 space-y-1 overflow-auto font-mono text-[11px]">
                      {ipcFuzz.results.map((r) => (
                        <div key={`${r.kind}:${r.target}`} className="flex items-center justify-between gap-2">
                          <span className="truncate">
                            [{r.kind}] {r.target}
                          </span>
                          <Badge variant={r.accessible ? "destructive" : "secondary"} className="text-[10px]">
                            {r.accessible ? "暴露可调起" : "受限/拒绝"}
                          </Badge>
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {ipcResult && (
                  <div className="rounded-md border bg-muted/40 p-2.5 font-mono text-xs">
                    <div className="mb-1 flex items-center justify-between">
                      <span>{ipcResult.command}</span>
                      <Badge variant={ipcResult.success ? "secondary" : "destructive"}>
                        exit {ipcResult.exit_code}
                      </Badge>
                    </div>
                    <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all text-muted-foreground">
                      {ipcResult.output || ipcResult.crash_log || "（无输出）"}
                    </pre>
                  </div>
                )}
              </>
            ) : (
              <div className="py-4 text-center text-muted-foreground text-xs">
                当前任务尚未关联移动应用安装包，可点击右上角「上传安装包」补充。
              </div>
            )}
          </CardContent>
        </Card>

        {/* 2. Frida Dynamic Hook Workbench */}
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <TerminalIcon className="size-4" />
              Frida 动态插桩与 SSL Pinning 绕过
            </CardTitle>
            <CardDescription>
              {state?.pinning_alerts && state.pinning_alerts.length > 0 ? (
                <span className="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400">
                  <ShieldAlertIcon className="size-3.5" />
                  检测到 {state.pinning_alerts.length} 个域名的 TLS Pinning 握手拒绝，建议开启 SSL Pinning Bypass
                </span>
              ) : (
                "面向授权测试：内置 ssl_unpinning / anti_detection / crypto_monitor / okhttp_plaintext 等模块"
              )}
            </CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3">
            <div className="flex flex-wrap gap-2">
              {scripts.map((sc) => {
                const checked = fridaModules.includes(sc.id);
                return (
                  <label key={sc.id} className="flex items-center gap-1.5 rounded border px-2 py-1 text-xs">
                    <Checkbox
                      checked={checked}
                      onCheckedChange={(v) =>
                        setFridaModules((prev) => (v ? [...prev, sc.id] : prev.filter((x) => x !== sc.id)))
                      }
                    />
                    <span title={sc.description}>{sc.name}</span>
                  </label>
                );
              })}
            </div>
            <Textarea
              rows={3}
              className="font-mono text-xs"
              placeholder="可选自定义 Frida JS 脚本（如 rpc.exports = { sign: function(data) { ... } }）"
              value={fridaCustomJS}
              onChange={(e) => setFridaCustomJS(e.target.value)}
            />
            <div className="flex flex-wrap items-center gap-2">
              <NativeSelect
                size="sm"
                value={fridaMode}
                onChange={(e) => setFridaMode(e.target.value as "spawn" | "attach")}
              >
                <NativeSelectOption value="spawn">spawn（冷启动拦截早期校验）</NativeSelectOption>
                <NativeSelectOption value="attach">attach（附加当前运行进程）</NativeSelectOption>
              </NativeSelect>
              <Button
                size="sm"
                variant="outline"
                disabled={!state?.mobile_device_id || busy !== ""}
                onClick={() =>
                  void runAction("启动 frida-server", async () => {
                    const r = await api.startTaskFridaServer(taskId);
                    toast.success(r.message);
                  })
                }
              >
                启动 frida-server (Root)
              </Button>
              <Button
                size="sm"
                disabled={!state?.mobile_device_id || busy !== ""}
                onClick={() =>
                  void runAction("注入 Frida Hook", async () => {
                    await api.startTaskFridaSession(taskId, {
                      mode: fridaMode,
                      modules: fridaModules,
                      custom_js: fridaCustomJS,
                    });
                    toast.success("Frida 会话已注入启动");
                  })
                }
              >
                <PlayIcon className="size-3.5" />
                注入启动
              </Button>
            </div>

            {state?.frida_sessions && state.frida_sessions.length > 0 && (
              <div className="space-y-2">
                <div className="grid gap-1.5 rounded-md border bg-muted/20 p-2 text-xs">
                  <div className="font-medium">Frida rpc.exports 在线签名/函数调用测试</div>
                  <div className="flex flex-wrap gap-1.5">
                    <Input
                      className="h-7 w-36 font-mono text-xs"
                      placeholder="导出方法名 (如 calcSign)"
                      value={rpcMethod}
                      onChange={(e) => setRpcMethod(e.target.value)}
                    />
                    <Input
                      className="h-7 flex-1 font-mono text-xs"
                      placeholder='JSON 参数列表 (如 ["arg1"])'
                      value={rpcArgsText}
                      onChange={(e) => setRpcArgsText(e.target.value)}
                    />
                    <Button
                      size="xs"
                      variant="outline"
                      disabled={!rpcMethod.trim() || busy !== ""}
                      onClick={() =>
                        void runAction("Frida RPC 调用", async () => {
                          let args: unknown[] = [];
                          try {
                            const parsed = JSON.parse(rpcArgsText || "[]");
                            if (Array.isArray(parsed)) args = parsed;
                          } catch {
                            throw new Error("RPC 参数必须是合法 JSON 数组");
                          }
                          const res = await api.invokeTaskMobileFridaRPC(taskId, { method: rpcMethod.trim(), args });
                          setRpcOutput(res.raw);
                        })
                      }
                    >
                      执行 RPC
                    </Button>
                  </div>
                  {rpcOutput && (
                    <pre className="max-h-24 overflow-auto rounded bg-muted/40 p-1.5 font-mono text-[11px]">
                      {rpcOutput}
                    </pre>
                  )}
                </div>
                {state.frida_sessions.map((sess) => (
                  <div key={sess.id} className="rounded-md border p-2.5 text-xs">
                    <div className="mb-1 flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2 font-mono">
                        <Badge variant={sess.running ? "default" : "secondary"}>
                          {sess.running ? "运行中" : "已结束"}
                        </Badge>
                        <span>{sess.package}</span>
                        <span className="text-muted-foreground">{sess.modules.join(", ")}</span>
                      </div>
                      {sess.running && (
                        <Button
                          size="xs"
                          variant="outline"
                          onClick={() =>
                            void runAction("停止 Frida", async () => {
                              await api.stopTaskFridaSession(taskId, sess.id);
                            })
                          }
                        >
                          <SquareIcon className="size-3" />
                          停止
                        </Button>
                      )}
                    </div>
                    {sess.exit_error && <div className="text-destructive">{sess.exit_error}</div>}
                    {sess.last_logs && sess.last_logs.length > 0 && (
                      <pre className="max-h-28 overflow-auto rounded bg-muted/40 p-2 font-mono text-[11px]">
                        {sess.last_logs.join("\n")}
                      </pre>
                    )}
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        {/* 3. Rooted Private Sandbox Browser */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <FolderOpenIcon className="size-4" />
                Root 应用私有目录审计 (/data/data/…)
              </CardTitle>
              <CardDescription>
                检查 SharedPreferences 明文 Token、SQLite 本地库、webview 缓存等存储风险
              </CardDescription>
            </div>
            <div className="flex flex-wrap gap-1.5">
              <Button
                size="xs"
                variant="default"
                disabled={!bound?.rooted || busy !== ""}
                onClick={() =>
                  void runAction("沙箱隐私体检", async () => {
                    const rep = await api.runTaskMobileStorageAudit(taskId);
                    setStorageAudit(rep);
                    toast.success(`沙箱体检完成：发现 ${rep.findings.length} 项存储隐患`);
                  })
                }
              >
                一键隐私体检
              </Button>
              <Button
                size="xs"
                variant="outline"
                disabled={!state?.mobile_device_id || busy !== ""}
                onClick={() =>
                  void runAction("Logcat 审计", async () => {
                    const rep = await api.taskMobileLogcat(taskId);
                    setLogcatAudit(rep);
                    toast.success(`Logcat 扫描 ${rep.scanned_lines} 行，命中 ${rep.findings.length} 项线索`);
                  })
                }
              >
                Logcat 泄露检查
              </Button>
              <Button
                size="xs"
                variant="outline"
                disabled={!bound?.rooted || busy !== ""}
                onClick={() => void openSandbox("")}
              >
                根目录
              </Button>
              <Button
                size="xs"
                variant="outline"
                disabled={!bound?.rooted || busy !== ""}
                onClick={() => void openSandbox("shared_prefs")}
              >
                shared_prefs
              </Button>
              <Button
                size="xs"
                variant="outline"
                disabled={!bound?.rooted || busy !== ""}
                onClick={() => void openSandbox("databases")}
              >
                databases
              </Button>
            </div>
          </CardHeader>
          <CardContent className="grid gap-2 text-xs">
            {sandboxPath !== "" && <div className="font-mono text-muted-foreground">路径: {sandboxPath}</div>}
            {sandboxEntries.length > 0 && (
              <div className="max-h-36 divide-y overflow-auto rounded border">
                {sandboxEntries.map((ent) => (
                  <button
                    key={ent.path}
                    type="button"
                    className="flex w-full items-center justify-between px-2.5 py-1.5 text-left hover:bg-muted/50"
                    onClick={() => void openSandbox(ent.path)}
                  >
                    <span className="font-mono">{ent.is_dir ? `📁 ${ent.name}` : `📄 ${ent.name}`}</span>
                    {!ent.is_dir && ent.size !== undefined && (
                      <span className="text-muted-foreground">{formatSampleBytes(ent.size)}</span>
                    )}
                  </button>
                ))}
              </div>
            )}
            {sandboxPreview && (
              <pre className="max-h-44 overflow-auto rounded border bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap">
                {sandboxPreview}
              </pre>
            )}
            {storageAudit && (
              <div className="space-y-1 rounded border bg-muted/20 p-2">
                <div className="font-medium">
                  沙箱自动化审计：明文 SQLite {storageAudit.sqlite_files?.length ?? 0} 个 · SQLCipher{" "}
                  {storageAudit.sqlcipher_files?.length ?? 0} 个 · 风险 {storageAudit.findings.length} 项
                </div>
                {storageAudit.findings.map((f) => (
                  <div
                    key={`${f.kind}:${f.location}:${f.summary}`}
                    className="rounded bg-muted/40 px-2 py-1 font-mono text-[11px]"
                  >
                    [{f.severity.toUpperCase()}] {f.summary} {f.evidence ? `· ${f.evidence}` : ""}
                  </div>
                ))}
              </div>
            )}
            {logcatAudit && (
              <div className="space-y-1 rounded border bg-muted/20 p-2">
                <div className="font-medium">Logcat 敏感信息审计（扫描 {logcatAudit.scanned_lines} 行）</div>
                <div className="max-h-28 space-y-1 overflow-auto font-mono text-[11px]">
                  {logcatAudit.findings.map((lf) => (
                    <div key={`${lf.kind}:${lf.line}`} className="truncate rounded bg-muted/40 px-2 py-0.5">
                      [{lf.severity}/{lf.kind}] {lf.line}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Right column: Device Binding & Proxy/CA Controls + Interactive Screen & Human Session Handoff */}
      <div className="grid gap-4 lg:col-span-5">
        {/* 4. Device Binding & Root CA / Proxy */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <WifiIcon className="size-4" />
                设备连接与 Root / CA 环境
              </CardTitle>
              <CardDescription>{adbWarn ? `ADB 提示: ${adbWarn}` : "支持 USB ADB 与局域网无线调试"}</CardDescription>
            </div>
            <Button size="xs" variant="ghost" onClick={() => void refreshState()}>
              <RefreshCwIcon className="size-3.5" />
            </Button>
          </CardHeader>
          <CardContent className="grid gap-3 text-xs">
            {/* Wi-Fi connect/pair bar */}
            <div className="grid gap-1.5 rounded-md border p-2.5">
              <Label className="text-xs">无线 ADB 连接 / Android 11+ 配对</Label>
              <div className="flex gap-1.5">
                <Input
                  className="h-7 font-mono text-xs"
                  placeholder="IP:端口（如 192.168.1.50:5555）"
                  value={wifiEndpoint}
                  onChange={(e) => setWifiEndpoint(e.target.value)}
                />
                <Button
                  size="xs"
                  variant="outline"
                  disabled={!wifiEndpoint.trim() || busy !== ""}
                  onClick={() =>
                    void runAction("无线 ADB 连接", async () => {
                      const res = await api.connectMobileDevice({ endpoint: wifiEndpoint.trim() });
                      toast.success(res.message);
                    })
                  }
                >
                  连接
                </Button>
              </div>
              <div className="flex gap-1.5">
                <Input
                  className="h-7 font-mono text-xs"
                  placeholder="配对端口 (ip:port)"
                  value={pairEndpoint}
                  onChange={(e) => setPairEndpoint(e.target.value)}
                />
                <Input
                  className="h-7 w-24 font-mono text-xs"
                  placeholder="6位配对码"
                  value={pairCode}
                  onChange={(e) => setPairCode(e.target.value)}
                />
                <Button
                  size="xs"
                  variant="ghost"
                  disabled={!pairEndpoint.trim() || !pairCode.trim() || busy !== ""}
                  onClick={() =>
                    void runAction("无线配对", async () => {
                      const res = await api.connectMobileDevice({
                        pair_endpoint: pairEndpoint.trim(),
                        pair_code: pairCode.trim(),
                      });
                      toast.success(res.message);
                    })
                  }
                >
                  配对
                </Button>
              </div>
            </div>

            {/* Bind device selector */}
            <div className="flex items-center gap-2">
              <NativeSelect
                size="sm"
                className="flex-1"
                value={selectedSerial}
                onChange={(e) => setSelectedSerial(e.target.value)}
              >
                <NativeSelectOption value="">-- 选择已发现测试设备 --</NativeSelectOption>
                {devices.map((d) => (
                  <NativeSelectOption key={d.serial} value={d.serial}>
                    {d.model || d.serial} ({d.serial}) · {d.state} {d.rooted ? "[Root]" : ""}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
              {state?.mobile_device_id ? (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy !== ""}
                  onClick={() =>
                    void runAction("解绑设备", async () => {
                      await api.unbindTaskMobileDevice(taskId);
                      toast.success("已释放设备独占锁");
                    })
                  }
                >
                  解绑
                </Button>
              ) : (
                <Button
                  size="sm"
                  disabled={!selectedSerial || busy !== ""}
                  onClick={() =>
                    void runAction("绑定设备", async () => {
                      await api.bindTaskMobileDevice(taskId, selectedSerial, "usb_reverse");
                      toast.success(`已绑定设备 ${selectedSerial}`);
                    })
                  }
                >
                  绑定到任务
                </Button>
              )}
            </div>

            {bound && (
              <div className="grid gap-2 rounded-md border bg-muted/30 p-2.5">
                <div className="flex flex-wrap gap-1.5">
                  <Badge variant={bound.state === "online" ? "default" : "secondary"}>{bound.state}</Badge>
                  <Badge variant="outline">
                    Android {bound.os_version} (API {bound.api_level})
                  </Badge>
                  <Badge variant="outline">{bound.abi}</Badge>
                  {deviceCapabilitySummary(bound).map((tag) => (
                    <Badge key={tag} variant="secondary">
                      {tag}
                    </Badge>
                  ))}
                </div>
                {bound.proxy_addr && (
                  <div className="font-mono text-muted-foreground">当前设备代理: {bound.proxy_addr}</div>
                )}
                <div className="grid grid-cols-2 gap-1.5 pt-1">
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={!state?.app_sample_paths?.length || busy !== ""}
                    onClick={() =>
                      void runAction("安装 APK", async () => {
                        const r = await api.installTaskMobileApp(taskId);
                        toast.success(r.message);
                      })
                    }
                  >
                    安装/重装 APP
                  </Button>
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={!bound.rooted || busy !== ""}
                    onClick={() =>
                      void runAction("推送系统 CA", async () => {
                        const r = await api.pushTaskMobileCA(taskId);
                        toast.success(r.message);
                      })
                    }
                  >
                    <CheckCircle2Icon className="size-3" />
                    Root 装系统 CA
                  </Button>
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={busy !== ""}
                    onClick={() =>
                      void runAction("开启 USB 代理", async () => {
                        const r = await api.toggleTaskMobileProxy(taskId, "enable_usb");
                        toast.success(r.message);
                      })
                    }
                  >
                    开启 USB 抓包代理
                  </Button>
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={!bound.rooted || busy !== ""}
                    onClick={() =>
                      void runAction("开启 Root 透明代理", async () => {
                        const r = await api.toggleTaskMobileProxy(taskId, "enable_transparent");
                        toast.success(r.message);
                      })
                    }
                  >
                    透明分流 (iptables+QUIC)
                  </Button>
                  <Button
                    size="xs"
                    variant="ghost"
                    disabled={busy !== ""}
                    onClick={() =>
                      void runAction("还原代理", async () => {
                        await api.toggleTaskMobileProxy(taskId, "disable");
                        toast.success("已恢复手机直连与代理设置");
                      })
                    }
                  >
                    一键还原手机代理
                  </Button>
                </div>
              </div>
            )}
          </CardContent>
        </Card>

        {/* 5. Human-in-the-loop Session Extractor & Interactive Screen */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <CameraIcon className="size-4" />
                真机交互与人机接力会话提取
              </CardTitle>
              <CardDescription>人工完成验证码/刷脸登录后，点击提取实时 Token 注入 Agent 黑板</CardDescription>
            </div>
            <Button
              size="xs"
              variant="default"
              disabled={busy !== ""}
              onClick={() =>
                void runAction("提取登录会话", async () => {
                  const res = await api.extractTaskMobileSession(taskId);
                  if (res.count > 0) {
                    toast.success(`成功提取并写入 ${res.count} 条移动端会话凭据`);
                  } else {
                    toast.info("近期抓包中暂未检测到认证请求头，请先操作 APP 登录接口");
                  }
                })
              }
            >
              <KeyRoundIcon className="size-3" />
              提取实时会话
            </Button>
          </CardHeader>
          <CardContent className="grid gap-3 text-xs">
            <div className="flex flex-wrap gap-1.5">
              <Button
                size="xs"
                variant="outline"
                disabled={!state?.mobile_device_id || busy !== ""}
                onClick={() => void refreshScreen()}
              >
                刷新屏幕与控件树
              </Button>
              <Button
                size="xs"
                variant="outline"
                disabled={!state?.mobile_device_id || busy !== ""}
                onClick={() =>
                  void runAction("启动 APP", async () => {
                    await api.runTaskMobileUI(taskId, { action: "launch" });
                    await refreshScreen();
                  })
                }
              >
                拉起被测 APP
              </Button>
              <Button
                size="xs"
                variant="outline"
                disabled={!state?.mobile_device_id || busy !== ""}
                onClick={() =>
                  void runAction("返回", async () => {
                    await api.runTaskMobileUI(taskId, { action: "back" });
                    await refreshScreen();
                  })
                }
              >
                返回键
              </Button>
              <Button
                size="xs"
                variant="secondary"
                disabled={!state?.mobile_device_id || busy !== ""}
                onClick={() =>
                  void runAction("自主界面遍历", async () => {
                    const rep = await api.runTaskMobileUICrawl(taskId, 12);
                    setCrawlResult(rep);
                    toast.success(
                      `遍历完成：访问 ${rep.unique_screens_visited} 个不重复页面，新增抓包 ${rep.traffic_delta ?? 0} 条`,
                    );
                    await refreshScreen();
                  })
                }
              >
                自主界面遍历 (Crawl)
              </Button>
            </div>
            {crawlResult && (
              <div className="rounded border bg-muted/20 p-2 font-mono text-[11px]">
                UI 状态机覆盖：执行 {crawlResult.steps_executed} 步 · 不重复页面 {crawlResult.unique_screens_visited} 个
                · 新增流量 {crawlResult.traffic_delta ?? 0} 条
              </div>
            )}

            {screenPng && (
              <div className="grid gap-2 sm:grid-cols-2">
                <button
                  type="button"
                  className="block w-full overflow-hidden rounded-md border bg-black/5 p-0"
                  onClick={(e) => void handleImageTap(e)}
                >
                  <img
                    ref={screenImgRef}
                    src={`data:image/png;base64,${screenPng}`}
                    alt="设备实时画面（点击直接向手机发送 tap）"
                    className="max-h-80 w-full cursor-crosshair object-contain"
                  />
                </button>
                <div className="flex max-h-80 flex-col gap-1.5 overflow-auto rounded-md border p-2">
                  <div className="flex gap-1">
                    <Input
                      className="h-7 text-xs"
                      placeholder="选中输入框先点控件再输入…"
                      value={uiInputText}
                      onChange={(e) => setUIInputText(e.target.value)}
                    />
                    <Button
                      size="xs"
                      variant="outline"
                      disabled={!uiInputText || busy !== ""}
                      onClick={() =>
                        void runAction("输入文本", async () => {
                          await api.runTaskMobileUI(taskId, { action: "input", text: uiInputText });
                          setUIInputText("");
                        })
                      }
                    >
                      输入
                    </Button>
                  </div>
                  {uiElements.map((el) => (
                    <button
                      key={`${el.index}-${el.bounds}`}
                      type="button"
                      className="flex items-center justify-between gap-1 rounded px-1.5 py-1 text-left hover:bg-muted"
                      onClick={() =>
                        void runAction("点击控件", async () => {
                          await api.runTaskMobileUI(taskId, { action: "tap", x: el.center_x, y: el.center_y });
                          await refreshScreen();
                        })
                      }
                    >
                      <span className="truncate font-mono text-[11px]">{uiElementDisplayLabel(el)}</span>
                      <span className="shrink-0 text-[10px] text-muted-foreground">
                        {el.center_x},{el.center_y}
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
