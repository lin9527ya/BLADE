#!/usr/bin/env bash
# build-killwxapkg.sh — 交叉编译 MIT 许可的 KillWxapkg v2.4.1 Linux 二进制。
set -euo pipefail

VER="${KILLWXAPKG_VERSION:-v2.4.1}"
OUT_DIR="${1:-dist}"
mkdir -p "$OUT_DIR"

TMP_SRC="$(mktemp -d)"
trap 'rm -rf "$TMP_SRC"' EXIT

git clone --depth 1 --branch "$VER" https://github.com/Ackites/KillWxapkg "$TMP_SRC"
(
  cd "$TMP_SRC"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$OLDPWD/$OUT_DIR/killwxapkg_linux_amd64" .
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o "$OLDPWD/$OUT_DIR/killwxapkg_linux_arm64" .
)
cp "$TMP_SRC/LICENSE" "$OUT_DIR/KillWxapkg-LICENSE"
sha256sum "$OUT_DIR"/killwxapkg_linux_* > "$OUT_DIR/KILLWXAPKG-SHA256SUMS"
echo "[+] Built killwxapkg $VER into $OUT_DIR"
