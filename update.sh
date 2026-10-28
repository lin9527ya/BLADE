#!/usr/bin/env bash
# BLADE 更新脚本：① Docker 更新（重建本地镜像）  ② 本地编译更新（重建二进制）
# 与 install.sh 对应：install 负责首次落地，update 负责升级到新版本。
# DB 迁移无需手动执行——blade 每次启动都会幂等重跑 schema.sql（含 ADD COLUMN/CREATE
# INDEX IF NOT EXISTS），所以“重启即迁移”。数据（pgdata 卷、./data、./skills）不受影响。
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
# 本地编译使用的版本号：优先 BLADE_BUILD_VERSION，否则取最近的 git tag。
# 不用 git describe 的提交后缀，避免未打 tag 的本地构建显示成 dev/长哈希。
build_version(){
  if [ -n "${BLADE_BUILD_VERSION:-}" ]; then printf '%s' "${BLADE_BUILD_VERSION#v}"; return; fi
  if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    local v
    v="$(git describe --tags --abbrev=0 2>/dev/null || true)"
    if [ -n "$v" ]; then printf '%s' "${v#v}"; return; fi
  fi
  printf 'dev'
}

# ── 可选：同步仓库到最新代码（compose/脚本/本地编译源码都靠它更新）───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "非 git 工作副本，跳过 git pull"; return; }
  [ "$(ask '拉取最新代码 (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull 未能快进（本地有改动或分支分叉）——请手动处理后重试，本次沿用当前代码"
  fi
}

# ── ① Docker 更新 ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "未检测到 docker / docker compose，请先用 ./install.sh 安装部署"
  [ -f .env ] || die "未找到 .env，请先运行 ./install.sh 完成首次部署"

  # 可选：本地镜像 tag（不填沿用 .env 的 BLADE_TAG；compose 默认 local）
  local tag; tag="$(ask '本地镜像 tag（回车沿用 .env / local）' '')"
  if [ -n "$tag" ]; then
    if grep -q '^BLADE_TAG=' .env; then
      sed -i.bak "s|^BLADE_TAG=.*|BLADE_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nBLADE_TAG=%s\n' "$tag" >> .env
    fi
    ok "已将 BLADE_TAG 设为 ${tag}"
  fi

  # 本仓 compose 使用本地 build，不再 pull 外部镜像。二进制由 build.sh --docker
  # 按 Dockerfile 期望的 dist/<arch>/blade 产出。
  if command -v go >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
    info "重建本地镜像二进制（前端 + Go）…"
    ./build.sh --docker
  else
    arch="$(go env GOARCH 2>/dev/null || uname -m)"
    case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
    [ -f "dist/${arch}/blade" ] || die "本机没有 Go/npm，且未找到 dist/${arch}/blade；请先安装工具链或准备预编译二进制"
    info "复用已存在的 dist/${arch}/blade"
  fi
  info "重建并启动（blade 重启时自动迁移 schema）…"
  docker compose up -d --build blade
  ok "更新完成 → http://localhost:8787"
  info "查看日志：docker compose logs -f blade"
  info "清理旧镜像（可选）：docker image prune -f"
}

# ── ② 本地编译更新 ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "未检测到 Go（>=1.26）：https://go.dev/dl/"
  [ -f config.json ] || warn "未找到 config.json——若首次部署请改用 ./install.sh"
  ok "Go: $(go version)"

  local version
  version="$(build_version)"
  ok "本地版本: ${version}"
  if command -v npm >/dev/null 2>&1; then
    info "重建前端静态产物…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "重新编译内嵌单二进制…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -ldflags "-s -w -X main.version=${version}" -o blade ./cmd/blade
  else
    warn "未检测到 npm：编译**不内嵌前端**的后端（前端需另跑 npm run dev）"
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${version}" -o blade ./cmd/blade
  fi
  ok "编译完成 → ./blade（${version}）"
  warn "请重启正在运行的 blade 进程以生效（重启时会自动迁移 schema）"
}

echo "=============================="
echo "  BLADE 更新"
echo "  1) Docker 更新（拉新镜像重建）"
echo "  2) 本地更新（go 重新编译）"
echo "=============================="
case "$(ask '选择' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "无效选择" ;;
esac
