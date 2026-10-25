#!/usr/bin/env python3
"""DeepSeek/Anthropic 网关缓存探针。

连续发送两次**完全相同**的 Anthropic Messages 请求，检查第二次的
`cache_read_input_tokens` 是否 > 0。用于在优化 BLADE 之前先确认网关真的在做
前缀缓存（DeepSeek 缓存按账号 + 前缀；多上游 key 轮询/无粘性路由会让它整体失效）。

用法：
  export BLADE_PROBE_BASE_URL="https://api.deepseek.com/anthropic"
  export BLADE_PROBE_API_KEY="..."
  export BLADE_PROBE_MODEL="deepseek-chat"
  python3 bench/cache_probe.py

可选：
  --header "x-session-id: probe-abc"   自定义粘性/会话头（可重复）
  --bearer                             用 Authorization: Bearer 而不是 x-api-key
  --runs 3                             发几次（默认 2）
  --delay 1                            两次之间的间隔秒数
  --json                               输出机器可读 JSON

判定：第二次及以后 `cache_read_input_tokens > 0` 即 PASS；全为 0 说明网关侧
没有前缀缓存（或每次落到不同上游账号），先修网关再谈 BLADE 优化。
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

## 固定前缀：足够长（>64 token 缓存块），且两次请求逐字节相同。
_PARA = (
    "The quick brown fox jumps over the lazy dog. Cache probe paragraph 0123456789. "
    "Authorization scope is fixed for this probe; no target interaction is performed. "
)
STABLE_PREFIX = (_PARA * 40) + "\n\nReply with exactly: OK"
SYSTEM = "You are a cache probe. Follow the user's instruction exactly."


def build_body(model: str) -> dict:
    return {
        "model": model,
        "max_tokens": 16,
        "temperature": 0,
        "system": SYSTEM,
        "messages": [{"role": "user", "content": STABLE_PREFIX}],
    }


def call(url: str, key: str, body: dict, extra_headers: dict, bearer: bool, timeout: float) -> dict:
    headers = {
        "content-type": "application/json",
        "anthropic-version": "2023-06-01",
    }
    if bearer:
        headers["authorization"] = "Bearer " + key
    else:
        headers["x-api-key"] = key
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


def usage_of(resp: dict) -> dict:
    u = resp.get("usage") or {}
    inp = int(u.get("input_tokens") or 0)
    hit = int(u.get("cache_read_input_tokens") or 0)
    write = int(u.get("cache_creation_input_tokens") or 0)
    return {"input": inp, "cache_read": hit, "cache_write": write, "miss": max(0, inp - hit)}


def main() -> int:
    ap = argparse.ArgumentParser(description="Anthropic/DeepSeek 前缀缓存探针")
    ap.add_argument("--base-url", default=os.environ.get("BLADE_PROBE_BASE_URL", ""))
    ap.add_argument("--url", default=os.environ.get("BLADE_PROBE_URL", ""), help="完整 URL（优先于 base-url）")
    ap.add_argument("--key", default=os.environ.get("BLADE_PROBE_API_KEY", ""))
    ap.add_argument("--model", default=os.environ.get("BLADE_PROBE_MODEL", ""))
    ap.add_argument("--header", action="append", default=[], help="额外请求头，格式 Name: Value，可重复")
    ap.add_argument("--bearer", action="store_true", help="用 Authorization: Bearer（默认 x-api-key）")
    ap.add_argument("--runs", type=int, default=2)
    ap.add_argument("--delay", type=float, default=1.0)
    ap.add_argument("--timeout", type=float, default=60.0)
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()
    if not args.model:
        raise SystemExit("missing model: --model or BLADE_PROBE_MODEL")
    if not args.key:
        raise SystemExit("missing key: --key or BLADE_PROBE_API_KEY")
    url = args.url or (args.base_url.rstrip("/") + "/v1/messages" if args.base_url else "")
    if not url:
        raise SystemExit("missing url: --url/--base-url or BLADE_PROBE_BASE_URL")

    extra = {}
    for h in args.header:
        if ":" not in h:
            raise SystemExit(f"bad --header (want Name: Value): {h}")
        k, v = h.split(":", 1)
        extra[k.strip()] = v.strip()

    body = build_body(args.model)
    runs = []
    for i in range(max(1, args.runs)):
        resp = call(url, args.key, body, extra, args.bearer, args.timeout)
        u = usage_of(resp)
        u["run"] = i + 1
        runs.append(u)
        if not args.json:
            pct = (100.0 * u["cache_read"] / u["input"]) if u["input"] else 0.0
            print(f"run {i+1}: input={u['input']} cache_read={u['cache_read']} "
                  f"cache_write={u['cache_write']} miss={u['miss']} hit={pct:.1f}%")
        if i + 1 < args.runs:
            time.sleep(max(0.0, args.delay))

    cached = any(u["cache_read"] > 0 for u in runs[1:]) if len(runs) > 1 else runs[0]["cache_read"] > 0
    verdict = "PASS" if cached else "FAIL"
    if args.json:
        print(json.dumps({"verdict": verdict, "runs": runs}, ensure_ascii=False, indent=2))
    else:
        print(f"verdict: {verdict}")
        if not cached:
            print("第二次仍无缓存命中：检查网关是否多上游 key 轮询/缺少粘性头，"
                  "以及 --header 是否需要与会话头一致。")
    return 0 if cached else 1


if __name__ == "__main__":
    sys.exit(main())
