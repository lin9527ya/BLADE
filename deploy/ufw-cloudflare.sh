#!/usr/bin/env bash
# 只放行 Cloudflare 的来源段访问 443（方案 B：公网直连 + CF 代理）。
#
# 为什么必须这么做：用了 Cloudflare 代理后，攻击者/扫描器**仍然可以直接扫你 VPS 的真实 IP**——
# CF 只挡了"从域名进来的人"。所以源站必须只对 Cloudflare 开放，否则一台上着 CF 的机器
# 照样会因为"8787 端口开着"被测绘引擎收录（而且它比普通机器更"值得"收录：CF 后面常常有真东西）。
#
# 用途：在新装的 VPS 上跑一次（需要 root）。
#   sudo ./ufw-cloudflare.sh
# 之后：只有 Cloudflare 能连 443；SSH 端口按需自己放行（示例里保留 22）。
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "需要 root：sudo $0" >&2
  exit 1
fi

# Cloudflare 的段是**会变**的：从官方端点拉，别把一份写死的清单抄进仓库（它迟早过期，
# 而过期那天表现为"网站突然 403/超时"，很难查）。
echo "拉取 Cloudflare 官方 IP 段…"
v4="$(curl -fsSL https://www.cloudflare.com/ips-v4)"
v6="$(curl -fsSL https://www.cloudflare.com/ips-v6)"
if [[ -z "$v4" ]]; then
  echo "拉取失败——不要在这种情况下继续（会把站点关死）" >&2
  exit 1
fi

ufw --force reset
ufw default deny incoming
ufw default allow outgoing

# SSH：按需改端口；建议同时上 fail2ban（见 deploy/README.md）。
ufw allow 22/tcp comment 'ssh'

# 443 只对 Cloudflare 开。
while read -r cidr; do
  [[ -z "$cidr" ]] && continue
  ufw allow proto tcp from "$cidr" to any port 443 comment 'cloudflare'
done <<< "$v4"
while read -r cidr; do
  [[ -z "$cidr" ]] && continue
  ufw allow proto tcp from "$cidr" to any port 443 comment 'cloudflare'
done <<< "$v6"

ufw --force enable
ufw status verbose

cat <<'NOTE'

完成。接下来：
  1. 源站证书用 **Cloudflare Origin Certificate**（15 年、不进 CT 日志），别用 Let's Encrypt
     —— 后者的证书会进证书透明日志，域名与 IP 就被 Censys 这类引擎收录了。
  2. 反代按 deploy/nginx-cloudflare.conf 配（含 set_real_ip_from 与 SSE 免缓冲）。
  3. BLADE 自己保持默认 loopback 监听（`-addr 127.0.0.1:8787`），反代再用 proxy_pass 接。
NOTE
