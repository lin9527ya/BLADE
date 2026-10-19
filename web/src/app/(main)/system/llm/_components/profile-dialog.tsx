"use client";

import * as React from "react";

import { EFFORT_LEVELS, MAX_TOKENS_FIELDS, MAX_TOKENS_FIELD_HINTS, NONE, THINKING_TYPES, fromStore, toStore } from "./llm-helpers";

import { ProfileRetryFields, ZERO_OVERRIDE } from "./retry";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { LLMProfile, LLMRetryOverride } from "@/lib/types";
import {
  Loader2Icon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  SaveIcon,
} from "lucide-react";
import { toast } from "sonner";

// LLM 配置档（provider / 模型 / 代理 / 参数）的编辑弹窗。

export function ProfileDialog({
  profile,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // null = 新建
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const isNew = !profile;
  const [name, setName] = React.useState("");
  const [format, setFormat] = React.useState<"anthropic" | "openai" | "openai-responses">("anthropic");
  const [model, setModel] = React.useState("");
  const [baseUrl, setBaseUrl] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [keyHint, setKeyHint] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [cw, setCw] = React.useState("0"); // 上下文窗口(K tokens);0=默认200K
  const [thinkingType, setThinkingType] = React.useState(NONE);
  const [effort, setEffort] = React.useState(NONE);
  const [priority, setPriority] = React.useState("0"); // 轮询顺位;越大越先
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [streaming, setStreaming] = React.useState(true); // true=流式(默认);false=非流式
  const [maxTokens, setMaxTokens] = React.useState("0"); // 单次回复输出上限;0=不发送
  const [maxTokensField, setMaxTokensField] = React.useState(NONE); // 上限用哪个字段名;NONE=max_tokens
  const [sessionHeaderKey, setSessionHeaderKey] = React.useState(""); // 自定义会话头名;空=不发送

  // opencode zen 网关强制要求 x-opencode-session（缺了 400 MissingSessionID）：
  // 地址是 opencode.ai 且输入框留空时，测试/保存都自动按该头名走（后端也有同款兜底）。
  const effectiveSessionHeader = (url: string, key: string): string => {
    const k = key.trim();
    if (k) return k;
    return /(^|\.)opencode\.ai/i.test(url) ? "x-opencode-session" : "";
  };
  const [retry, setRetry] = React.useState<LLMRetryOverride>(ZERO_OVERRIDE); // 本配置的重试覆盖;全 0=跟随全局
  const [testing, setTesting] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  // 每次打开时从传入的 profile 灌一遍表单（新建则重置为默认值）。抽屉关掉再打开
  // 就是一次干净的开始，不会留下上一个配置的残影。
  React.useEffect(() => {
    if (!open) return;
    setName(profile?.name ?? "");
    setFormat(profile?.format === "openai" || profile?.format === "openai-responses" ? profile.format : "anthropic");
    setModel(profile?.model ?? "");
    setBaseUrl(profile?.base_url ?? "");
    setProxy(profile?.proxy ?? "");
    setRps(String(profile?.rate_per_second ?? 0));
    setRpm(String(profile?.rate_per_minute ?? 0));
    setCw(String(profile?.context_window_k ?? 0));
    setThinkingType(fromStore(profile?.thinking_type));
    setEffort(fromStore(profile?.reasoning_effort));
    setPriority(String(profile?.priority ?? 0));
    setPoolExclude(profile?.pool_exclude ?? false);
    setStreaming(profile?.streaming ?? true);
    setMaxTokens(String(profile?.max_tokens ?? 0));
    setMaxTokensField(fromStore(profile?.max_tokens_field));
    setSessionHeaderKey(profile?.session_header_key ?? "");
    setRetry(profile?.retry ?? ZERO_OVERRIDE);
    setApiKey("");
    setKeyHint(profile?.api_key_hint ?? "");
    setModels([]);
    setModelsOpen(false);
  }, [open, profile]);

  const profileId = profile ? Number(profile.id) : undefined;

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(format, baseUrl, apiKey, proxy, profileId);
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsOpen(true);
        toast.success(`已加载 ${r.models.length} 个模型`);
      } else {
        toast.error(`加载模型失败：${r.error ?? "未获取到模型"}`);
      }
    } catch (e) {
      toast.error(`加载模型出错：${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function testConnection() {
    if (testing) return;
    setTesting(true);
    try {
      // 用配置实际会跑的思考参数来测，这样不支持该字段的模型在这里就失败，
      // 而不是等到跑任务时才炸。传 profile id：Key 输入框留空时用已存的 Key。
      const r = await api.testLLM(
        format,
        model,
        baseUrl,
        apiKey,
        proxy,
        toStore(thinkingType),
        toStore(effort),
        profileId,
        streaming,
        effectiveSessionHeader(baseUrl, sessionHeaderKey),
      );
      // 回复内容一并展示：看得见模型确实说了话，才算和会话里跑通是一回事。
      if (r.ok)
        toast.success(`连接成功 · ${r.latency_ms ?? "?"}ms · ${r.model ?? model}`, {
          description: r.reply ? `回复：${r.reply}` : undefined,
        });
      else toast.error(`连接失败：${r.error ?? "未知"}`);
    } catch (e) {
      toast.error(`测试出错：${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  async function save() {
    if (!name.trim() || !model.trim()) {
      toast.error("请填写名称与模型");
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      const { id } = await api.saveLLMProfile({
        ...(profile ? { id: Number(profile.id) } : {}),
        name: name.trim(),
        format,
        model: model.trim(),
        base_url: baseUrl.trim(),
        proxy: proxy.trim(),
        api_key: apiKey,
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        context_window_k: Number(cw) || 0,
        thinking_type: toStore(thinkingType),
        reasoning_effort: toStore(effort),
        priority: Number(priority) || 0,
        pool_exclude: poolExclude,
        streaming,
        max_tokens: Math.max(0, Number(maxTokens) || 0),
        // 字段名开关只对 openai(Chat Completions) 有意义，其它格式一律回落到默认；
        // 后端也会再做一次同样的归一化，这里只是别让 UI 送出自相矛盾的值。
        max_tokens_field: format === "openai" ? toStore(maxTokensField) : "",
        session_header_key: effectiveSessionHeader(baseUrl, sessionHeaderKey),
        retry,
      });
      if (isNew) toast.success(`已新建：${name.trim()}（在卡片上「设为激活」以启用）`);
      else toast.success(profile?.is_default ? "已保存，激活配置即时生效，无需重启" : "已保存");
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <DialogHeader className="border-b px-4 py-3">
          <DialogTitle className="flex items-center gap-2">
            {isNew ? "新建模型配置" : `编辑：${profile?.name}`}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                激活中
              </Badge>
            )}
          </DialogTitle>
          <DialogDescription>
            {isNew
              ? "新建后不会自动激活，请在卡片上「设为激活」以启用。"
              : "修改后点击保存；激活配置保存后对全部 Agent 立即生效。"}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">名称</Label>
              <Input
                id="p-name"
                placeholder="例如：OpenAI 生产"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>格式</Label>
              <Select value={format} onValueChange={(v) => setFormat(v as "anthropic" | "openai" | "openai-responses")}>
                <SelectTrigger>
                  <SelectValue placeholder="选择格式" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI (Chat Completions)</SelectItem>
                  <SelectItem value="openai-responses">OpenAI (Responses API)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-model">模型</Label>
            <div className="flex gap-2">
              <Input
                id="p-model"
                className="font-mono"
                placeholder="claude-opus-4-8"
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
              {/* modal: 这个 Popover 的内容被 portal 到 <body>，在弹窗的滚动锁之外，
                  不加 modal 时列表能渲染却滚不动。modal 让它自己持有最上层滚动锁。 */}
              <Popover open={modelsOpen} onOpenChange={setModelsOpen} modal>
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    className="shrink-0"
                    disabled={loadingModels}
                    onClick={loadModels}
                    title="从 API 加载可用模型"
                  >
                    {loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                  </Button>
                </PopoverTrigger>
                {models.length > 0 && (
                  <PopoverContent className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1" align="end">
                    {models.map((m) => (
                      <button
                        key={m}
                        type="button"
                        className="w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
                        onClick={() => {
                          setModel(m);
                          setModelsOpen(false);
                        }}
                      >
                        {m}
                      </button>
                    ))}
                  </PopoverContent>
                )}
              </Popover>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-base-url">Base URL（可选）</Label>
            <Input
              id="p-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-proxy">代理（可选）</Label>
            <Input
              id="p-proxy"
              className="font-mono"
              placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              仅 LLM 出站请求走此代理，支持 http/https/socks5，可带账号密码（如
              socks5://user:pass@host:port，密码含特殊字符需 URL 编码）；留空表示不使用代理（直连）。
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-session-header">自定义会话头（可选）</Label>
            <Input
              id="p-session-header"
              className="font-mono"
              autoComplete="off"
              placeholder="如 x-opencode-session（opencode.ai 端点必须；留空=不发送）"
              value={sessionHeaderKey}
              onChange={(e) => setSessionHeaderKey(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              填写头名后，每次请求都会带上这个 HTTP 头，头值自动填为 <b>当前会话的 session id</b>（chat 会话如
              conv-12、worker 如 exp3-worker-i87）。用于按 session-id 头做提示缓存 /
              粘性路由的网关；同一会话多轮稳定、不同会话互不相同。留空则不发送（
              <b>opencode.ai 端点例外</b>：会自动按 x-opencode-session 发送，否则网关直接 400）。
              浏览器若把用户名等自动填充进来（此前把「admin」填进过本框），请清空。
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-api-key">API Key</Label>
            <Input
              id="p-api-key"
              type="password"
              autoComplete="new-password"
              placeholder={keyHint ? `已设置（${keyHint}），留空保持不变` : "sk-…"}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-3">
            <div className="grid gap-2">
              <Label htmlFor="p-rps">每秒限速</Label>
              <Input id="p-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-rpm">每分钟限速</Label>
              <Input id="p-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-cw">上下文窗口(K)</Label>
              <Input
                id="p-cw"
                type="number"
                min={0}
                max={1000}
                value={cw}
                onChange={(e) => setCw(e.target.value)}
                placeholder="200"
              />
            </div>
          </div>
          <p className="-mt-2 text-muted-foreground text-xs">
            限速 0 = 不限，全 Agent 共享。上下文窗口单位 K（千 token），0 = 默认 200K，上限 1000（即
            1M）；设太高会导致压缩不触发。
          </p>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-priority" className="text-sm">
                  轮询优先级
                </Label>
                <p className="text-muted-foreground text-xs">
                  数字越大越先被选中；激活配置恒为第 1 顺位，与本值无关。相同优先级的配置会轮流打头，天然分摊额度。
                </p>
              </div>
              <Input
                id="p-priority"
                type="number"
                className="w-24 shrink-0"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">不参与轮询</Label>
                <p className="text-muted-foreground text-xs">
                  开启后不会被当作故障转移目标（仍可被 Agent / 任务显式指定使用）。 适合「只给某个 Agent
                  专用、不希望别人失败时烧掉」的昂贵配置。
                </p>
              </div>
              <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label="不参与轮询" />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">流式输出 · streaming</Label>
                <p className="text-muted-foreground text-xs">
                  开启（默认）走流式 SSE，有运行中实时进度与实时 token 计数。 关闭则走真·非流式（stream:false，
                  一次性返回完整响应）——可绕开部分网关糟糕的 SSE 实现（空帧 / 思考字段丢帧），
                  代价是失去运行中的实时进度。
                </p>
              </div>
              <Switch checked={streaming} onCheckedChange={setStreaming} aria-label="流式输出" />
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-max-tokens" className="text-sm">
                  输出上限 · max tokens
                </Label>
                <p className="text-muted-foreground text-xs">
                  单次回复最多生成多少 token，随每次请求发出。0（默认）= 不发送该字段，由服务端默认值决定。
                  这与上面的「上下文窗口」是两回事：那是模型总容量，只在本地用来算压缩阈值。
                  设太小会让推理模型在思考阶段就被截断，一个字答案都出不来。
                </p>
              </div>
              <Input
                id="p-max-tokens"
                type="number"
                min={0}
                className="w-28 shrink-0"
                value={maxTokens}
                onChange={(e) => setMaxTokens(e.target.value)}
                placeholder="0"
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">上限字段名</Label>
                <p className="text-muted-foreground text-xs">{MAX_TOKENS_FIELD_HINTS[format]}</p>
              </div>
              <Select
                value={format === "openai" ? maxTokensField : NONE}
                onValueChange={setMaxTokensField}
                disabled={format !== "openai"}
              >
                <SelectTrigger className="w-56 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {MAX_TOKENS_FIELDS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label className="text-sm">思考开关 · thinking.type</Label>
                <p className="text-muted-foreground text-xs">
                  控制是否发送 thinking 字段。不发送=不带该字段（兼容 MiniMax 等不支持 的模型）；关闭=发
                  disabled；开启=发 enabled。与下面的强度互相独立。
                </p>
              </div>
              <Select value={thinkingType} onValueChange={setThinkingType}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINKING_TYPES.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">思考强度 · reasoning_effort</Label>
                <p className="text-muted-foreground text-xs">
                  独立的强度档位（OpenAI reasoning_effort / Anthropic output_config.effort）。 有些接口没有 thinking
                  字段、只靠强度即可激活思考，故可单独设置、不发送思考开关。
                </p>
              </div>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EFFORT_LEVELS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <ProfileRetryFields value={retry} onChange={setRetry} />
        </div>

        <div className="flex gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={testConnection} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            {testing ? "测试中…" : "测试连接"}
          </Button>
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? "新建" : "保存"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
