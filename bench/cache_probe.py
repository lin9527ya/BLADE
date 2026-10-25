#!/usr/bin/env python3
"""DeepSeek/网关前缀缓存探针（Anthropic / OpenAI / Responses 三种协议）。

连续发送两次**完全相同**的请求，检查第二次的缓存命中 token 是否 > 0。用于在优化
BLADE 之前先确认网关真的在做前缀缓存（DeepSeek 缓存按账号 + 前缀；多上游 key
轮询/无粘性路由会让它整体失效）。

用法（Anthropic 兼容端点）：
  export BLADE_PROBE_BASE_URL="https://api.deepseek.com/anthropic"
  export BLADE_PROBE_API_KEY="..."
  export BLADE_PROBE_MODEL="deepseek-chat"
  python3 bench/cache_probe.py

用法（commandcode 的 DeepSeek：注意必须走 Responses，不是 Anthropic）：
  export BLADE_PROBE_BASE_URL="https://api.commandcode.ai/provider/v1"
  export BLADE_PROBE_FORMAT="openai-responses"
  export BLADE_PROBE_MODEL="deepseek-v4.1-flash"
  python3 bench/cache_probe.py

可选：
  --format anthropic|openai|openai-responses  默认 anthropic
  --header "x-session-id: probe-abc"           自定义粘性/会话头（可重复）
  --auth auto|bearer|api-key                   auto：anthropic 用 x-api-key，其余 Bearer
  --runs 3                                     发几次（默认 2）
  --delay 1                                    两次之间的间隔秒数
  --json                                       输出机器可读 JSON（含原始 usage）
  --raw                                        文本模式也打印原始 usage

判定：第二次及以后命中 token > 0 即 PASS；全为 0 说明网关侧没有前缀缓存，或
网关返回的缓存字段名不在已知集合里（看 --raw 的原始 usage）。
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

# 固定前缀：足够长（>64 token 缓存块），且两次请求逐字节相同。
_PARA = (
    "The quick brown fox jumps over the lazy dog. Cache probe paragraph 0123456789. "
    "Authorization scope is fixed for this probe; no target interaction is performed. "
)
STABLE_PREFIX = (_PARA * 40) + "\n\nReply with exactly: OK"
SYSTEM = "You are a cache probe. Follow the user's instruction exactly."


def build_body(fmt: str, model: str) -> dict:
    if fmt == "anthropic":
        return {
            "model": model,
            "max_tokens": 16,
            "temperature": 0,
            "system": SYSTEM,
            "messages": [{"role": "user", "content": STABLE_PREFIX}],
        }
    if fmt == "openai":
        return {
            "model": model,
            "max_tokens": 16,
            "temperature": 0,
            "messages": [
                {"role": "system", "content": SYSTEM},
                {"role": "user", "content": STABLE_PREFIX},
            ],
        }
    # openai-responses
    return {
        "model": model,
        "max_output_tokens": 16,
        "input": SYSTEM + "\n\n" + STABLE_PREFIX,
    }


def endpoint(base_url: str, url: str, fmt: str) -> str:
    if url:
        return url
    b = base_url.rstrip("/")
    if fmt == "anthropic":
        return b + "/v1/messages"
    suffix = "/chat/completions" if fmt == "openai" else "/responses"
    if b.endswith("/v1"):
        return b + suffix
    return b + "/v1" + suffix


def call(url: str, key: str, body: dict, fmt: str, auth: str, extra_headers: dict,
         timeout: float, user_agent: str) -> dict:
    headers = {
        "content-type": "application/json",
        "accept": "application/json",
        # 与 Norma/Go 客户端一致：某些网关（Cloudflare 前置）会按 UA 拦截
        # Python-urllib，返回 403 / error code 1010。
        "user-agent": user_agent,
    }
    use_bearer = auth == "bearer" or (auth == "auto" and fmt != "anthropic")
    if use_bearer:
        headers["authorization"] = "Bearer " + key
    else:
        headers["x-api-key"] = key
        if fmt == "anthropic":
            headers["anthropic-version"] = "2023-06-01"
    headers.update(extra_headers)
    req = urllib.request.Request(url, data=json.dumps(body).encode("utf-8"), headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "replace")[:800]
        raise SystemExit(f"HTTP {e.code}: {detail}")
    except urllib.error.URLError as e:
        raise SystemExit(f"request failed: {e.reason}")
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        raise SystemExit(f"non-JSON response: {raw[:500]}")


def usage_of(fmt: str, resp: dict) -> dict:
    """归一化缓存字段；同时兼容 OpenAI 标准与 DeepSeek 原生字段名。"""
    u = resp.get("usage") or {}
    if fmt == "anthropic":
        inp = int(u.get("input_tokens") or 0)
        hit = int(u.get("cache_read_input_tokens") or 0)
        write = int(u.get("cache_creation_input_tokens") or 0)
    elif fmt == "openai":
        inp = int(u.get("prompt_tokens") or 0)
        hit = int((u.get("prompt_tokens_details") or {}).get("cached_tokens") or 0)
        native_hit = u.get("prompt_cache_hit_tokens")
        native_miss = u.get("prompt_cache_miss_tokens")
        if hit == 0 and native_hit is not None:
            hit = int(native_hit)
        if inp == 0 and native_hit is not None:
            inp = int(native_hit) + int(native_miss or 0)
        write = 0
    else:  # openai-responses
        inp = int(u.get("input_tokens") or 0)
        hit = int((u.get("input_tokens_details") or {}).get("cached_tokens") or 0)
        native_hit = u.get("prompt_cache_hit_tokens")
        native_miss = u.get("prompt_cache_miss_tokens")
        if hit == 0 and native_hit is not None:
            hit = int(native_hit)
        if inp == 0 and native_hit is not None:
            inp = int(native_hit) + int(native_miss or 0)
        write = 0
    if inp < hit:
        inp = hit
    return {"input": inp, "cache_read": hit, "cache_write": write, "miss": max(0, inp - hit)}


def main() -> int:
    ap = argparse.ArgumentParser(description="Anthropic/OpenAI/Responses 前缀缓存探针")
    ap.add_argument("--format", choices=["anthropic", "openai", "openai-responses"],
                    default=os.environ.get("BLADE_PROBE_FORMAT", "anthropic"))
    ap.add_argument("--base-url", default=os.environ.get("BLADE_PROBE_BASE_URL", ""))
    ap.add_argument("--url", default=os.environ.get("BLADE_PROBE_URL", ""), help="完整 URL（优先于 base-url）")
    ap.add_argument("--key", default=os.environ.get("BLADE_PROBE_API_KEY", ""))
    ap.add_argument("--model", default=os.environ.get("BLADE_PROBE_MODEL", ""))
    ap.add_argument("--header", action="append", default=[], help="额外请求头，格式 Name: Value，可重复")
    ap.add_argument("--auth", choices=["auto", "bearer", "api-key"], default="auto")
    ap.add_argument("--runs", type=int, default=2)
    ap.add_argument("--delay", type=float, default=1.0)
    ap.add_argument("--timeout", type=float, default=60.0)
    ap.add_argument("--user-agent", default=os.environ.get("BLADE_PROBE_UA", "Go-http-client/1.1"))
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--raw", action="store_true")
    args = ap.parse_args()
    if not args.model:
        raise SystemExit("missing model: --model or BLADE_PROBE_MODEL")
    if not args.key:
        raise SystemExit("missing key: --key or BLADE_PROBE_API_KEY")
    url = endpoint(args.base_url, args.url, args.format)
    if not url:
        raise SystemExit("missing url: --url/--base-url or BLADE_PROBE_BASE_URL")

    extra = {}
    for h in args.header:
        if ":" not in h:
            raise SystemExit(f"bad --header (want Name: Value): {h}")
        k, v = h.split(":", 1)
        extra[k.strip()] = v.strip()

    body = build_body(args.format, args.model)
    runs = []
    for i in range(max(1, args.runs)):
        resp = call(url, args.key, body, args.format, args.auth, extra, args.timeout, args.user_agent)
        u = usage_of(args.format, resp)
        u["run"] = i + 1
        u["raw_usage"] = resp.get("usage") or {}
        runs.append(u)
        if not args.json:
            pct = (100.0 * u["cache_read"] / u["input"]) if u["input"] else 0.0
            print(f"run {i+1}: input={u['input']} cache_read={u['cache_read']} "
                  f"cache_write={u['cache_write']} miss={u['miss']} hit={pct:.1f}%")
            if args.raw or u["cache_read"] == 0:
                print("  raw usage: " + json.dumps(u["raw_usage"], ensure_ascii=False))
        if i + 1 < args.runs:
            time.sleep(max(0.0, args.delay))

    cached = any(u["cache_read"] > 0 for u in runs[1:]) if len(runs) > 1 else runs[0]["cache_read"] > 0
    verdict = "PASS" if cached else "FAIL"
    if args.json:
        print(json.dumps({"format": args.format, "url": url, "verdict": verdict, "runs": runs},
                         ensure_ascii=False, indent=2))
    else:
        print(f"verdict: {verdict}")
        if not cached:
            print("第二次仍无缓存命中：检查网关是否多上游 key 轮询/缺少粘性头；"
                  "若 raw usage 里缓存字段非空，则是 BLADE 台账未解析该字段名。")
    return 0 if cached else 1


if __name__ == "__main__":
    sys.exit(main())
