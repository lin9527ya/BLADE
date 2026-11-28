#!/usr/bin/env bash
# miniapp_unpack.sh — 微信小程序包编排解包（安全基线 + 质量阶梯 + 质量门禁）。
#
# 用法：
#   bash miniapp_unpack.sh --in <wxapkg|dir|zip> [--out <root>] [--appid wx...] [--quality auto|wedecode|unveilr|fast]
#
# 产物（默认 <out>/<appid>/）：
#   raw/            安全提取的原始文件（自研 parser，防路径穿越）
#   decrypted/      V1MMWX 解密后的包
#   src_wedecode/   wedecode 质量解包（若安装）
#   src_unveilr/    unveilr 质量解包（若安装 + UNVEILR_DIR）
#   src_killwxapkg/ KillWxapkg 兜底（沙箱/专用目录）
#   source ->       质量分最高的产物（符号链接）
#   manifest.json   包哈希/工具/质量分/警告
#
# 设计：不信任外部工具退出码；每个产物都过 quality_gate.py。
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PY="${PYTHON:-python3}"
IN=""
OUT=""
APPID=""
QUALITY="auto"

while [ $# -gt 0 ]; do
  case "$1" in
    --in) IN="${2:-}"; shift 2 ;;
    --out) OUT="${2:-}"; shift 2 ;;
    --appid) APPID="${2:-}"; shift 2 ;;
    --quality) QUALITY="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

[ -n "$IN" ] || { echo "缺少 --in" >&2; exit 2; }
[ -e "$IN" ] || { echo "输入不存在: $IN" >&2; exit 2; }
command -v "$PY" >/dev/null 2>&1 || { echo "缺少 python3" >&2; exit 2; }

OUT="${OUT:-$PWD/miniapp}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK_ROOT="$OUT/_work"
mkdir -p "$WORK_ROOT"

log() { printf '[miniapp] %s\n' "$*"; }
warn() { printf '[miniapp][warn] %s\n' "$*" >&2; }
run_with_timeout() {
  # macOS 默认没有 timeout；有 coreutils 的 gtimeout 就用，否则直接跑。
  local secs="$1"; shift
  if command -v timeout >/dev/null 2>&1; then timeout "$secs" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then gtimeout "$secs" "$@"
  else "$@"; fi
}
gate_score() {
  # 输出质量分；失败输出 0
  "$PY" "$SCRIPT_DIR/quality_gate.py" --dir "$1" 2>/dev/null \
    | "$PY" -c 'import sys,json;d=json.load(sys.stdin);print(d.get("score",0))' 2>/dev/null \
    || echo 0
}
gate_json() {
  "$PY" "$SCRIPT_DIR/quality_gate.py" --dir "$1" 2>/dev/null || true
}

# ── 1. 输入归一化（zip 安全解压） ─────────────────────────────
SRC="$IN"
if [ -f "$IN" ]; then
  case "$IN" in
    *.zip|*.ZIP)
      ZIPDIR="$WORK_ROOT/unzipped"
      rm -rf "$ZIPDIR"; mkdir -p "$ZIPDIR"
      "$PY" - "$IN" "$ZIPDIR" <<'PY'
import os, sys, zipfile
src, dst = sys.argv[1], sys.argv[2]
count = 0
total = 0
with zipfile.ZipFile(src) as z:
    for info in z.infolist():
        name = info.filename.replace("\\", "/")
        if name.startswith("/") or ".." in name.split("/"):
            raise SystemExit("unsafe zip entry: " + name)
        if info.is_dir():
            continue
        count += 1
        total += info.file_size
        if count > 20000 or total > 512 * 1024 * 1024:
            raise SystemExit("zip too many/large entries")
        out = os.path.join(dst, name)
        os.makedirs(os.path.dirname(out), exist_ok=True)
        with z.open(info) as r, open(out, "wb") as w:
            w.write(r.read())
print("unzipped %d files" % count)
PY
      [ $? -eq 0 ] || { echo "zip 解压失败/含不安全条目" >&2; exit 3; }
      SRC="$ZIPDIR"
      ;;
    *)
      SRC="$(cd "$(dirname "$IN")" && pwd)"
      ;;
  esac
fi

# ── 2. 收集包 ─────────────────────────────────────────────────
# 不用 mapfile（macOS bash 3.2 没有），用 while + 索引数组。
PKGS=()
while IFS= read -r p; do
  [ -n "$p" ] && PKGS+=("$p")
done < <(find "$SRC" -type f -name '*.wxapkg' -not -path "$OUT/*" | sort)
if [ "${#PKGS[@]}" -eq 0 ]; then
  echo "未找到任何 .wxapkg（输入: $SRC）" >&2
  exit 3
fi
log "发现 ${#PKGS[@]} 个 wxapkg"

# 版本目录：包数量最多的目录（同版本主包+分包通常在一起）。
# 不用关联数组（bash 3.2 不支持），用临时文件做计数；路径可含空格。
DIRS_TMP="$(mktemp)"
for p in "${PKGS[@]}"; do dirname "$p"; done | sort | uniq -c | sort -rn > "$DIRS_TMP"
VERSION_DIR="$(sed -n '1s/^ *[0-9][0-9]* *//p' "$DIRS_TMP")"
rm -f "$DIRS_TMP"
[ -n "$VERSION_DIR" ] || VERSION_DIR="$(dirname "${PKGS[0]}")"

# ── 3. AppID ─────────────────────────────────────────────────
if [ -z "$APPID" ]; then
  for cand in "$VERSION_DIR" "${PKGS[0]}"; do
    if [[ "$cand" =~ (wx[0-9a-f]{16}) ]]; then APPID="${BASH_REMATCH[1]}"; break; fi
  done
fi
[ -n "$APPID" ] || APPID="unknown"
if [ "$APPID" != "unknown" ] && ! [[ "$APPID" =~ ^wx[0-9a-f]{16}$ ]]; then
  echo "AppID 格式不合法（应为 wx+16 位小写十六进制）: $APPID" >&2
  exit 2
fi
log "AppID=$APPID 版本目录=$VERSION_DIR"

WORK="$OUT/$APPID"
rm -rf "$WORK"
mkdir -p "$WORK"/{raw,decrypted,src_wedecode,src_unveilr,src_killwxapkg}

# ── 4. 安全基线：逐个包 info/decrypt/extract ─────────────────
UNSAFE=0
HAS_ENC=0
: > "$WORK/packages.tsv"
for pkg in "${PKGS[@]}"; do
  base="$(basename "$pkg")"
  info="$("$PY" "$SCRIPT_DIR/wxapkg_safe.py" info --in "$pkg" 2>/dev/null || true)"
  if ! printf '%s' "$info" | "$PY" -c 'import sys,json;d=json.load(sys.stdin);raise SystemExit(0 if d.get("ok") else 1)' 2>/dev/null; then
    UNSAFE=1
    warn "包校验失败，跳过外部解包器: $base :: $info"
    printf '%s\t%s\n' "$base" "UNSAFE" >> "$WORK/packages.tsv"
    continue
  fi
  sha="$(printf '%s' "$info" | "$PY" -c 'import sys,json;print(json.load(sys.stdin).get("sha256",""))' 2>/dev/null)"
  kind="$(printf '%s' "$info" | "$PY" -c 'import sys,json;print(json.load(sys.stdin).get("kind",""))' 2>/dev/null)"
  [ "$kind" = "v1mmwx" ] && HAS_ENC=1
  printf '%s\t%s\t%s\n' "$base" "$kind" "$sha" >> "$WORK/packages.tsv"

  plain="$WORK/decrypted/$base"
  if [ "$kind" = "v1mmwx" ] && [ "$APPID" = "unknown" ]; then
    warn "V1MMWX 包但 AppID 未知，无法解密: $base"
    continue
  fi
  dec="$("$PY" "$SCRIPT_DIR/wxapkg_safe.py" decrypt --in "$pkg" --appid "$APPID" --out "$plain" 2>/dev/null || true)"
  if ! printf '%s' "$dec" | "$PY" -c 'import sys,json;d=json.load(sys.stdin);raise SystemExit(0 if d.get("ok") else 1)' 2>/dev/null; then
    warn "解密失败: $base :: $dec"
    continue
  fi
  ext="$("$PY" "$SCRIPT_DIR/wxapkg_safe.py" extract --in "$plain" --out "$WORK/raw" 2>/dev/null || true)"
  printf '%s' "$ext" | "$PY" -c 'import sys,json;d=json.load(sys.stdin);raise SystemExit(0 if d.get("ok") else 1)' 2>/dev/null \
    || warn "安全提取失败: $base :: $ext"
done

# ── 5. 质量解包阶梯 ─────────────────────────────────────────
run_quality_tool() {
  local name="$1" outdir="$2"; shift 2
  [ "$QUALITY" = "auto" ] || [ "$QUALITY" = "$name" ] || return 1
  rm -rf "$outdir"
  mkdir -p "$(dirname "$outdir")"
  if run_with_timeout 900 "$@" >"$WORK/${name}.log" 2>&1; then
    local score; score="$(gate_score "$outdir")"
    log "$name: score=$score log=$WORK/${name}.log"
    return 0
  else
    warn "$name 执行失败（exit != 0），仍需看产物；log=$WORK/${name}.log"
    local score; score="$(gate_score "$outdir")"
    [ "$score" != "0" ] && return 0
    return 1
  fi
}

# 明文包不需要真实 AppID；KillWxapkg 的 -id 只是解密参数，用占位值即可。
# 只要有一个加密包且 AppID 未知，就不能用占位值（会把加密包解成乱码）。
KILLWX_ID="$APPID"
if [ "$APPID" = "unknown" ] && [ "$HAS_ENC" -eq 0 ]; then
  KILLWX_ID="wx0000000000000000"
  log "明文包且 AppID 未知：KillWxapkg 使用占位 -id（不影响明文解包）"
fi

if [ "$UNSAFE" -eq 0 ]; then
  if command -v wedecode >/dev/null 2>&1 && [ "$QUALITY" != "fast" ]; then
    run_quality_tool wedecode "$WORK/src_wedecode" wedecode "$VERSION_DIR" --out "$WORK/src_wedecode" --clear || true
  else
    log "wedecode 未安装（可选，安装后质量更好）"
  fi
  if [ -n "${UNVEILR_DIR:-}" ] && command -v node >/dev/null 2>&1 && [ "$QUALITY" != "fast" ]; then
    run_quality_tool unveilr "$WORK/src_unveilr" node "$UNVEILR_DIR/dist/index.js" wx "$VERSION_DIR" -o "$WORK/src_unveilr" --no-clear-save --no-clear-parsed || true
  fi
  if command -v killwxapkg >/dev/null 2>&1 && [ "$KILLWX_ID" != "unknown" ] && [ "$QUALITY" != "wedecode" ] && [ "$QUALITY" != "unveilr" ]; then
    # 关键：必须 -id；必须传版本目录（不递归）；预校验已由 wxapkg_safe 完成。
    # KillWxapkg 会在 CWD 落 config/rule.yaml，切到 $WORK 避免污染任务/仓库目录。
    ( cd "$WORK" && run_quality_tool killwxapkg "$WORK/src_killwxapkg" killwxapkg -id "$KILLWX_ID" -in "$VERSION_DIR" -out "$WORK/src_killwxapkg" -restore -pretty -noClean ) || true
  fi
else
  warn "存在不安全包，跳过所有外部解包器（只用安全提取）"
fi

# ── 6. 选最优产物 ───────────────────────────────────────────
BEST=""; BEST_SCORE=-1; BEST_GATE="{}"
for cand in "$WORK/src_wedecode" "$WORK/src_unveilr" "$WORK/src_killwxapkg" "$WORK/raw"; do
  [ -d "$cand" ] || continue
  score="$(gate_score "$cand")"
  score="${score:-0}"
  if "$PY" -c "import sys;raise SystemExit(0 if float('$score')>float('$BEST_SCORE') else 1)" 2>/dev/null; then
    BEST="$cand"; BEST_SCORE="$score"; BEST_GATE="$(gate_json "$cand")"
  fi
done
[ -n "$BEST" ] || { echo "没有可用产物" >&2; exit 4; }
if [ "$BEST_SCORE" = "0" ] && [ -z "$(ls -A "$WORK/raw" 2>/dev/null)" ]; then
  echo "解包失败：AppID 未知或包格式不支持（raw 为空，质量分 0）" >&2
  exit 4
fi
ln -sfn "$BEST" "$WORK/source"
log "最优产物: $BEST (score=$BEST_SCORE)"

# ── 7. 写 manifest ──────────────────────────────────────────
APPID="$APPID" BEST="$BEST" SCORE="$BEST_SCORE" GATE="$BEST_GATE" WORK="$WORK" \
  "$PY" - <<'PY'
import datetime, json, os
work = os.environ["WORK"]
gate = {}
try:
    gate = json.loads(os.environ.get("GATE") or "{}")
except Exception:
    pass
packages = []
try:
    with open(os.path.join(work, "packages.tsv"), encoding="utf-8") as f:
        for line in f:
            parts = line.rstrip("\n").split("\t")
            if len(parts) >= 3:
                packages.append({"name": parts[0], "kind": parts[1], "sha256": parts[2]})
except FileNotFoundError:
    pass
tools = {}
for name in ("wedecode", "unveilr", "killwxapkg"):
    p = os.path.join(work, name + ".log")
    if os.path.exists(p):
        tools[name] = {"log": p, "ok": True}
manifest = {
    "appid": os.environ.get("APPID", "unknown"),
    "best_source": os.environ.get("BEST", ""),
    "quality": gate,
    "packages": packages,
    "tools": tools,
    "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
}
with open(os.path.join(work, "manifest.json"), "w", encoding="utf-8") as f:
    json.dump(manifest, f, ensure_ascii=False, indent=2)
print(json.dumps({"appid": manifest["appid"], "best_source": manifest["best_source"],
                  "score": gate.get("score"), "status": gate.get("status"),
                  "packages": len(packages)}, ensure_ascii=False))
PY

# 控制台摘要（供 agent 直接引用）
ROUTES="$("$PY" - "$WORK/source" <<'PY'
import json, os, sys
root = sys.argv[1]
for dp, _, fs in os.walk(root):
    for fn in fs:
        if fn in ("app.json", "app-config.json"):
            try:
                cfg = json.load(open(os.path.join(dp, fn), encoding="utf-8", errors="replace"))
                n = len(cfg.get("pages") or [])
                for s in (cfg.get("subPackages") or cfg.get("subpackages") or []):
                    if isinstance(s, dict):
                        n += len(s.get("pages") or [])
                print(n); raise SystemExit
            except Exception:
                pass
print(0)
PY
)"
echo "APPID=$APPID WORK=$WORK SOURCE=$WORK/source SCORE=$BEST_SCORE ROUTES=$ROUTES"
