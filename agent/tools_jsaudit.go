package agent

// js_audit：SPA/webpack 前端源码泄露审计领域工具（v0.4.59）。
//
// ## 为什么需要它
//
// 任务 18（Vue 站点）实测：worker 对前端只有 api-recon 的 JS 逆向前文，没有系统性
// 的 sourcemap 检测——`//# sourceMappingURL=x.js.map` 指向的 .map 若公开（webpack
// productionSourceMap 未关），sourcesContent 里是**完整原始源码**（Vue SFC/接口/
// 密钥/内网地址），属 CWE-540 源码信息泄露，L3 即可报 high；密钥命中直接 critical。
// 依赖模型"想起来"去 curl .map 不可靠——做成领域工具，一次调用出全链路结论。
//
// ## 检测链（脚本内实现，stdlib only）
//
// 1. 入口 HTML → 提取 <script src> / modulepreload；
// 2. 每个 JS：读尾部 sourceMappingURL（外链或 data:base64 内联）；无提示也探测
//    <js>.map 兜底；从 bundle 里再挖 hash 命名的 chunk（上限内入队）；
// 3. .map 是 JSON 且含 sources → 还原目录落盘（.vue/.env/config 计数），密钥与
//    接口/路由扫描（AKIA/私钥/JWT/AK-SK kv/内网段/URL/API path/router path）；
// 4. 每行一个 JSON 事件，summary 收尾——worker 照常 record_fact / report_finding。
//
// ## 设计原则（与 http_probe 同款）
//
// **只落脚本 + 组命令，不自己发请求**——流量录制、审批管控、审计全部经由 Bash
// 自动生效（http(s)_proxy 环境变量让 python urllib 走记录代理）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

const jsAuditScript = `#!/usr/bin/env python3
"""js_audit: SPA/webpack 前端源码泄露审计。

用法: python3 jsaudit.py --out <outdir> [--block-file <rules.json>] <url1> [url2 ...]
url 可以是入口 HTML（提取 <script src>）或 .js 文件本体。
每行输出一个 JSON 事件；最后一行 type=summary。仅标准库；
http(s)_proxy 环境变量自动生效（流量走记录代理）；证书校验关闭（同 curl -k）。
"""
import sys, os, json, re, base64, ssl, socket, ipaddress, gzip, zlib, urllib.request, urllib.parse, urllib.error

MAX_BYTES = 3_000_000
MAX_MAP_BYTES = 12_000_000
UA = {
    "User-Agent": os.environ.get("TARGET_USER_AGENT", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"),
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
    "Accept-Language": os.environ.get("TARGET_ACCEPT_LANGUAGE", "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7"),
    "Accept-Encoding": "gzip, deflate",
}
SENSITIVE_HEADERS = {
    "authorization", "proxy-authorization", "cookie",
    "x-api-key", "x-auth-token", "x-access-token", "x-session-token",
    "x-csrf-token", "x-xsrf-token", "x-token", "api-key",
}
META_HOSTS = {"localhost", "localhost.localdomain", "metadata", "metadata.google.internal", "instance-data"}
CGNAT_NET = ipaddress.ip_network("100.64.0.0/10")
AWS_V6_META = ipaddress.ip_address("fd00:ec2::254")
BLOCK_RULES = []
INITIAL_ORIGINS = set()

def _origin(url):
    p = urllib.parse.urlparse(url)
    port = p.port
    if port is None:
        port = 443 if p.scheme.lower() == "https" else (80 if p.scheme.lower() == "http" else None)
    return (p.scheme.lower(), (p.hostname or "").lower().rstrip("."), port)

def _has_proxy_env():
    return any(os.environ.get(k) for k in ("HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"))

def _norm_ip(ip):
    mapped = getattr(ip, "ipv4_mapped", None)
    return mapped if mapped is not None else ip

def _is_always_blocked_ip(ip):
    ip = _norm_ip(ip)
    return (ip.is_loopback or ip.is_link_local or ip.is_unspecified or ip.is_multicast or ip == AWS_V6_META)

def _is_internal_ip(ip):
    ip = _norm_ip(ip)
    return ip.is_private or ip.is_reserved or (ip.version == 4 and ip in CGNAT_NET)

def _match_block_rule(url, host, ips):
    low_host = host.lower()
    low_url = url.lower()
    for rule in BLOCK_RULES:
        kind = (rule.get("kind") or "").strip()
        pat = (rule.get("pattern") or "").strip()
        if not pat:
            continue
        lpat = pat.lower()
        if kind == "exact_domain" and low_host == lpat:
            return f"{kind}: {pat}"
        if kind == "fuzzy_domain" and lpat in low_host:
            return f"{kind}: {pat}"
        if kind == "exact_ip" and pat in ips:
            return f"{kind}: {pat}"
        if kind == "fuzzy_ip" and any(pat in ip_s for ip_s in ips):
            return f"{kind}: {pat}"
        if kind == "exact_url" and url.strip() == pat:
            return f"{kind}: {pat}"
        if kind == "fuzzy_url" and lpat in low_url:
            return f"{kind}: {pat}"
        if kind == "cidr":
            try:
                net = ipaddress.ip_network(pat, strict=False)
                for ip_s in ips:
                    if _norm_ip(ipaddress.ip_address(ip_s)) in net:
                        return f"{kind}: {pat}"
            except ValueError:
                pass
    return None

def check_url_allowed(url, is_sub_or_redirect=False):
    try:
        p = urllib.parse.urlparse(url)
    except Exception:
        return False, "URL 解析失败"
    scheme = (p.scheme or "").lower()
    if scheme not in ("http", "https"):
        return False, f"禁止协议 {scheme or '<empty>'}"
    host = (p.hostname or "").strip().rstrip(".")
    if not host:
        return False, "缺少主机名"
    orig = _origin(url)
    same_initial = orig in INITIAL_ORIGINS
    if (is_sub_or_redirect or not same_initial) and (host.lower() in META_HOSTS or host.lower().endswith(".localhost")):
        if not same_initial:
            return False, f"禁止访问本机/元数据主机 {host}"
    resolved = []
    try:
        resolved = [_norm_ip(ipaddress.ip_address(host))]
    except ValueError:
        if not _has_proxy_env():
            port = orig[2] or (443 if scheme == "https" else 80)
            try:
                infos = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
                seen_ip = set()
                for info in infos:
                    addr = info[4][0].split("%", 1)[0]
                    if addr not in seen_ip:
                        seen_ip.add(addr)
                        resolved.append(_norm_ip(ipaddress.ip_address(addr)))
            except OSError:
                resolved = []
    ip_strs = [str(ip) for ip in resolved]
    hit = _match_block_rule(url, host, ip_strs)
    if hit:
        return False, f"命中黑名单规则 [{hit}]"
    if not same_initial:
        for ip in resolved:
            if _is_always_blocked_ip(ip):
                return False, f"目标解析到受限地址 {ip}（回环/链路本地/元数据）"
            if is_sub_or_redirect and _is_internal_ip(ip):
                return False, f"禁止跳转到未授权内网地址 {ip}"
    return True, ""

class _DisabledSchemesHandler(urllib.request.BaseHandler):
    def file_open(self, req):
        raise urllib.error.URLError("blocked file:// scheme")
    def ftp_open(self, req):
        raise urllib.error.URLError("blocked ftp:// scheme")
    def data_open(self, req):
        raise urllib.error.URLError("blocked data: scheme")
    def unknown_open(self, req):
        raise urllib.error.URLError(f"blocked unsupported scheme: {req.type}")

class _ScopedRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        target = urllib.parse.urljoin(req.full_url, newurl)
        ok, reason = check_url_allowed(target, is_sub_or_redirect=True)
        if not ok:
            raise urllib.error.URLError(f"blocked redirect to {target}: {reason}")
        next_req = super().redirect_request(req, fp, code, msg, headers, newurl)
        if next_req is not None and _origin(req.full_url) != _origin(next_req.full_url):
            for hdrs in (next_req.headers, next_req.unredirected_hdrs):
                for k in list(hdrs.keys()):
                    lk = k.lower()
                    if lk == "host" or lk in SENSITIVE_HEADERS or any(w in lk for w in ("auth", "token", "secret", "cookie", "session", "key")):
                        hdrs.pop(k, None)
        return next_req

_ctx = ssl.create_default_context()
_ctx.check_hostname = False
_ctx.verify_mode = ssl.CERT_NONE
_opener = urllib.request.build_opener(
    _DisabledSchemesHandler(),
    _ScopedRedirectHandler(),
    urllib.request.HTTPSHandler(context=_ctx),
)

def _decode_resp(raw, encoding):
    enc = (encoding or "").lower().strip()
    if enc == "gzip":
        try:
            return gzip.decompress(raw)
        except Exception:
            return raw
    if enc == "deflate":
        try:
            return zlib.decompress(raw)
        except Exception:
            try:
                return zlib.decompress(raw, -zlib.MAX_WBITS)
            except Exception:
                return raw
    return raw

def fetch(url, cap=MAX_BYTES):
    ok, reason = check_url_allowed(url, is_sub_or_redirect=False)
    if not ok:
        raise urllib.error.URLError(f"blocked {url}: {reason}")
    req = urllib.request.Request(url, headers=UA)
    with _opener.open(req, timeout=15) as r:
        raw = r.read(cap + 1)
        data = _decode_resp(raw, r.headers.get("Content-Encoding", ""))
        return r.getcode(), data[:cap + 1], r.geturl()

def emit(obj):
    print(json.dumps(obj, ensure_ascii=False))
    sys.stdout.flush()

SCRIPT_RE = re.compile(r'<script[^>]+src=["\']([^"\']+)["\']', re.I)
PRELOAD_RE = re.compile(r'<link[^>]+(?:modulepreload|preload)[^>]+href=["\']([^"\']+\.js[^"\']*)["\']', re.I)
SMU_RE = re.compile(r'[#@]\s*sourceMappingURL=([^\s\'"]+)')
URL_RE = re.compile(r'https?://[^\s"\'<>()\\{}\[\]]{4,120}')
API_RE = re.compile(r'["\'](/(?:api|v[0-9]|rest|gateway|service|prod-api|dev-api)[A-Za-z0-9_\-/.]{2,80})["\']')
ROUTER_RE = re.compile(r'path:\s*["\'](/[^"\']{1,60})["\']')
CHUNK_RE = re.compile(r'["\']([A-Za-z0-9_\-./]{2,100}?[0-9a-f]{6,8}\.js)["\']')
SECRET_PATTERNS = [
    ("private_key", re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----')),
    ("aws_ak", re.compile(r'AKIA[0-9A-Z]{16}')),
    ("aliyun_ak", re.compile(r'LTAI[0-9A-Za-z]{12,}')),
    ("akid", re.compile(r'AKID[A-Za-z0-9]{10,}')),
    ("jwt", re.compile(r'eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.')),
    ("ak_sk_kv", re.compile(r'(?:access[_-]?key|secret[_-]?key|api[_-]?key|apikey|app[_-]?secret|client[_-]?secret|private[_-]?key|access[_-]?token)["\']?\s*[:=]\s*["\']([A-Za-z0-9_\-/+=]{10,64})["\']', re.I)),
    ("internal_ip", re.compile(r'(?:10\.\d+\.\d+\.\d+|192\.168\.\d+\.\d+|172\.(?:1[6-9]|2\d|3[01])\.\d+\.\d+)')),
]

def secret_scan(text, src):
    hits = {}
    for label, pat in SECRET_PATTERNS:
        m = pat.search(text)
        if m:
            s = m.group(0)[:120]
            hits[label] = s
    if hits:
        emit({"type": "secrets", "source": src, "hits": hits,
              "note": "确认是真实凭据（能调通接口/解密数据）再升级报告，先 record_fact"})
    return len(hits)

def endpoint_scan(js, url):
    urls = []
    for m in URL_RE.findall(js):
        if m not in urls:
            urls.append(m)
        if len(urls) >= 30:
            break
    apis = []
    for m in API_RE.findall(js):
        if m not in apis:
            apis.append(m)
        if len(apis) >= 30:
            break
    routes = []
    for m in ROUTER_RE.findall(js):
        if m not in routes:
            routes.append(m)
        if len(routes) >= 20:
            break
    if urls or apis or routes:
        emit({"type": "endpoints", "url": url, "urls": urls, "api_paths": apis, "router_paths": routes})
    return len(urls) + len(apis) + len(routes)

def chunk_candidates(js, base):
    out = []
    for m in CHUNK_RE.findall(js):
        if m in out or len(out) >= 8:
            continue
        u = urllib.parse.urljoin(base, m)
        ok, reason = check_url_allowed(u, is_sub_or_redirect=True)
        if not ok:
            emit({"type": "blocked", "url": u, "reason": reason})
            continue
        out.append(u)
    return out

def safe_name(src, i):
    # webpack:///./src/views/Login.vue -> src/views/Login.vue；防穿越，限制长度。
    p = urllib.parse.urlparse(src).path if "://" in src else src
    p = p.lstrip("/")
    p = re.sub(r"(\.\./)+", "_up/", p)
    parts = [re.sub(r'[<>:"|?*\x00-\x1f]', "_", seg) for seg in p.split("/") if seg and seg not in (".", "..")]
    name = os.path.join(*parts) if parts else ("file_%d" % i)
    if len(name) > 180:
        name = name[-180:]
    return "%04d_%s" % (i, name)

def write_sources(m, outdir, tag):
    sources = m.get("sources") or []
    contents = m.get("sourcesContent") or []
    written = 0
    vue = envf = 0
    for i, src in enumerate(sources):
        c = contents[i] if i < len(contents) and contents[i] else None
        low = src.lower()
        if low.endswith(".vue"):
            vue += 1
        if low.endswith(".env") or "config" in low and low.endswith((".ts", ".js")):
            envf += 1
        if c is None:
            continue
        rel = safe_name(src, i)
        path = os.path.join(outdir, tag, rel)
        try:
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8", errors="replace") as f:
                f.write(c)
            written += 1
            if written >= 300:
                break
        except OSError:
            continue
    return {"sources": len(sources), "with_content": sum(1 for c in contents if c), "written": written,
            "vue_files": vue, "env_or_config": envf}

def analyze_map(raw, js_url, map_url, outdir, tag):
    # 返回 (真实命中, 密钥命中数)：SPA fallback 会给 404 页回 200，"可达"≠"命中"。
    try:
        m = json.loads(raw.decode("utf-8", "replace"))
    except Exception:
        emit({"type": "sourcemap", "found": False, "js": js_url, "map_url": map_url,
              "note": "map 可达但不是 JSON"})
        return False, 0
    if not isinstance(m, dict) or "sources" not in m:
        emit({"type": "sourcemap", "found": False, "js": js_url, "map_url": map_url,
              "note": "JSON 但无 sources 字段"})
        return False, 0
    info = write_sources(m, outdir, tag)
    try:
        with open(os.path.join(outdir, tag, "__map__.json"), "wb") as f:
            f.write(raw)
    except OSError:
        pass
    emit({"type": "sourcemap", "found": True, "js": js_url, "map_url": map_url, **info,
          "dir": os.path.join(outdir, tag),
          "note": "CWE-540 源码泄露：sourcesContent 即原始源码；.vue/.env 计数见上。"
                  "含真实密钥可报 critical；仅源码结构泄露报 high（L3）"})
    n = secret_scan(raw.decode("utf-8", "replace"), "map:" + map_url)
    return True, n

def audit_js(url, outdir, seen_dirs):
    try:
        code, body, final = fetch(url)
    except Exception as e:
        emit({"type": "js", "url": url, "error": str(e)[:120]})
        return 0, []
    js = body.decode("utf-8", "replace")
    emit({"type": "js", "url": final, "status": code, "bytes": len(body)})
    hits = secret_scan(js, "js:" + final)
    n_ep = endpoint_scan(js, final)
    secrets_n = hits
    eps_n = 1 if n_ep else 0
    found = False
    total = hits
    m_smu = SMU_RE.search(js)
    tag = re.sub(r"[^A-Za-z0-9._-]", "_", urllib.parse.urlparse(final).path.replace("/", "_"))[-60:] or "js"
    if tag in seen_dirs:
        tag = tag + "_%d" % len(seen_dirs)
    seen_dirs.add(tag)
    if m_smu:
        mu = m_smu.group(1)
        if mu.startswith("data:application/json;base64,"):
            try:
                raw = base64.b64decode(mu.split(",", 1)[1])
                ok, total_s = analyze_map(raw, final, "(inline)", outdir, tag)
                total += total_s
                found = found or ok
            except Exception:
                pass
        else:
            map_url = urllib.parse.urljoin(final, mu)
            ok_u, reason = check_url_allowed(map_url, is_sub_or_redirect=True)
            if not ok_u:
                emit({"type": "blocked", "js": final, "url": map_url, "reason": reason})
            else:
                try:
                    code, raw, _ = fetch(map_url, MAX_MAP_BYTES)
                    if code == 200:
                        ok, total_s = analyze_map(raw, final, map_url, outdir, tag)
                        total += total_s
                        found = found or ok
                    else:
                        emit({"type": "sourcemap", "found": False, "js": final, "map_url": map_url,
                              "status": code})
                except Exception as e:
                    emit({"type": "sourcemap", "found": False, "js": final, "map_url": map_url,
                          "error": str(e)[:120]})
    if not found:
        map_url = final + ".map"
        ok_u, reason = check_url_allowed(map_url, is_sub_or_redirect=True)
        if not ok_u:
            emit({"type": "blocked", "js": final, "url": map_url, "reason": reason})
        else:
            try:
                code, raw, _ = fetch(map_url, MAX_MAP_BYTES)
                if code == 200:
                    ok, total_s = analyze_map(raw, final, map_url, outdir, tag)
                    total += total_s
                    found = found or ok
                else:
                    emit({"type": "sourcemap", "found": False, "js": final, "map_url": map_url,
                          "status": code, "note": "无 sourceMappingURL 提示，兜底探测 <js>.map"})
            except Exception as e:
                emit({"type": "sourcemap", "found": False, "js": final, "map_url": map_url,
                      "error": str(e)[:120]})
    return (1 if found else 0), chunk_candidates(js, final), secrets_n + total, eps_n

def main():
    global BLOCK_RULES, INITIAL_ORIGINS
    args = sys.argv[1:]
    outdir = os.path.join(os.getcwd(), "jsmap")
    block_file = ""
    urls = []
    i = 0
    while i < len(args):
        if args[i] == "--out" and i + 1 < len(args):
            outdir = args[i + 1]
            i += 2
            continue
        if args[i] == "--block-file" and i + 1 < len(args):
            block_file = args[i + 1]
            i += 2
            continue
        urls.append(args[i])
        i += 1
    if block_file and os.path.isfile(block_file):
        try:
            with open(block_file, "r", encoding="utf-8") as bf:
                loaded = json.load(bf)
                if isinstance(loaded, list):
                    BLOCK_RULES = loaded
        except Exception:
            BLOCK_RULES = []
    if not urls:
        emit({"type": "summary", "error": "没有给 URL"})
        return
    INITIAL_ORIGINS = set(_origin(u) for u in urls[:6])
    os.makedirs(outdir, exist_ok=True)
    queue, fetched, seen_dirs = [], set(), set()
    for u in urls[:6]:
        ok_u, reason = check_url_allowed(u, is_sub_or_redirect=False)
        if not ok_u:
            emit({"type": "blocked", "url": u, "reason": reason})
            continue
        queue.append(u)
    maps = js_n = secrets_total = ep_total = 0
    html_urls = set(u for u in queue if not u.endswith(".js"))
    while queue and js_n < 12:
        url = queue.pop(0)
        if url in fetched:
            continue
        fetched.add(url)
        if url in html_urls or url.endswith((".html", ".htm")) or ".js" not in url:
            try:
                code, body, final = fetch(url)
            except Exception as e:
                emit({"type": "html", "url": url, "error": str(e)[:120]})
                continue
            html = body.decode("utf-8", "replace")
            emit({"type": "html", "url": final, "status": code, "bytes": len(body)})
            found = 0
            for m in SCRIPT_RE.findall(html) + PRELOAD_RE.findall(html):
                u = urllib.parse.urljoin(final, m)
                ok_u, reason = check_url_allowed(u, is_sub_or_redirect=True)
                if not ok_u:
                    emit({"type": "blocked", "url": u, "reason": reason})
                    continue
                if u not in fetched and u not in queue and len(queue) < 14:
                    queue.append(u)
                    found += 1
            emit({"type": "html_scripts", "url": final, "queued": found})
            continue
        found_n, chunks, sec_n, ep_n = audit_js(url, outdir, seen_dirs)
        maps += found_n
        secrets_total += sec_n
        ep_total += ep_n
        js_n += 1
        for cu in chunks:
            if cu not in fetched and cu not in queue and len(queue) < 14:
                queue.append(cu)
    emit({"type": "summary", "js_audited": js_n, "sourcemaps_found": maps,
          "secret_signals": secrets_total, "endpoint_signals": ep_total, "dir": outdir,
          "next": "sourcemap found=true 的站点按 CWE-540 报 high（含真实密钥 critical）；"
                  "接口/路由清单 record_fact 并按 api-security-testing 继续"})

if __name__ == "__main__":
    main()
`

// ensureJsAuditScript 幂等落盘审计脚本；内容变更时自动刷新（避免复用 runDir 时保留旧版本）。
func (t *ToolSet) ensureJsAuditScript() string {
	if strings.TrimSpace(t.runDir) == "" {
		return ""
	}
	p := filepath.Join(t.runDir, "blade-jsaudit.py")
	if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, []byte(jsAuditScript)) {
		return p
	}
	if err := os.WriteFile(p, []byte(jsAuditScript), 0o755); err != nil {
		return ""
	}
	return p
}

func (t *ToolSet) writeJsAuditBlockFile(rules []db.AssetInterceptRule) string {
	type slimRule struct {
		Kind    string `json:"kind"`
		Pattern string `json:"pattern"`
	}
	var active []slimRule
	for _, r := range rules {
		if r.Enabled && strings.TrimSpace(r.Pattern) != "" {
			active = append(active, slimRule{Kind: r.Kind, Pattern: strings.TrimSpace(r.Pattern)})
		}
	}
	if len(active) == 0 || strings.TrimSpace(t.runDir) == "" {
		return ""
	}
	raw, err := json.Marshal(active)
	if err != nil {
		return ""
	}
	p := filepath.Join(t.runDir, ".jsaudit-block.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		return ""
	}
	return p
}

// jsAudit SPA/webpack 源码泄露审计：给入口 URL，返回一条可直接执行的命令。
func (t *ToolSet) jsAudit() actool.CoreTool {
	return readTool(
		"js_audit",
		"SPA/webpack 前端源码泄露审计（Vue/React 单页应用必跑）：给入口 URL（HTML 或 JS），"+
			"脚本自动提取 <script> 与 chunk、检测 sourceMappingURL 与 <js>.map 兜底探测、"+
			"还原 sourcesContent 原始源码落盘、扫描密钥（AK/SK/私钥/JWT/内网段）并抽取"+
			"接口与路由清单。本工具返回一条命令，交给 Bash 执行即可（流量录制/审批照常生效）。\n"+
			"结论口径：sourcemap 命中=CWE-540 源码泄露 high（L3）；还原出真实凭据=critical；"+
			"接口/路由清单 record_fact 后按 api-security-testing 继续。",
		obj(map[string]any{
			"urls": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "入口 URL 列表（1-6 个：入口 HTML 或具体 .js 文件）",
			},
		}, "urls"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				URLs []string `json:"urls"`
			}
			_ = json.Unmarshal(in, &a)
			var blockRules []db.AssetInterceptRule
			if t.as != nil && t.taskID > 0 {
				blockRules, _, _ = t.as.TaskInterceptRulesSplit(t.taskID)
			}
			urls := make([]string, 0, len(a.URLs))
			seen := map[string]bool{}
			for _, rawU := range a.URLs {
				rawU = strings.TrimSpace(rawU)
				if rawU == "" || seen[rawU] {
					continue
				}
				pu, perr := url.Parse(rawU)
				if perr != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Hostname() == "" {
					continue
				}
				if len(blockRules) > 0 {
					var ips []string
					if ip := net.ParseIP(pu.Hostname()); ip != nil {
						ips = []string{ip.String()}
					}
					if rule, _, hit := db.MatchAssetInterceptRules(blockRules, []string{pu.Hostname()}, ips, []string{rawU}); hit {
						return actool.Errorf(fmt.Sprintf("url %s 命中拦截规则: %s", rawU, rule.Reason())), nil
					}
				}
				seen[rawU] = true
				urls = append(urls, rawU)
				if len(urls) >= 6 {
					break
				}
			}
			if len(urls) == 0 {
				return actool.Errorf("urls 至少要有一个合法的 http(s) 绝对地址"), nil
			}
			script := t.ensureJsAuditScript()
			if script == "" {
				return actool.Errorf("js_audit 未启用: 本次 run 没有可用的工作目录"), nil
			}
			quoted := make([]string, len(urls))
			for i, u := range urls {
				quoted[i] = "'" + strings.ReplaceAll(u, "'", `'\''`) + "'"
			}
			out := filepath.Join(t.runDir, "jsmap")
			blockArg := ""
			if bf := t.writeJsAuditBlockFile(blockRules); bf != "" {
				blockArg = " --block-file '" + strings.ReplaceAll(bf, "'", `'\''`) + "'"
			}
			return jsonResult(map[string]any{
				"run_with_bash": fmt.Sprintf("python3 %s --out %s%s %s", script, out, blockArg, strings.Join(quoted, " ")),
				"count":         len(urls),
				"note": "输出每行一个 JSON 事件（html/js/sourcemap/secrets/endpoints/summary）。" +
					"sourcemap found=true → CWE-540：还原源码落盘在 --out 目录，save_evidence 后报 high；" +
					"secrets 有真实凭据 → critical；endpoints 照常 record_fact 并按 api-security-testing 继续。",
			})
		})
}
