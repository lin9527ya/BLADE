#!/usr/bin/env bash
# VPS 基础加固（配合 deploy/README.md 第八节）。
#
# 只做"关掉不该有的入口"这类低风险动作；**不碰 sshd_config**（改错会把人锁在外面），
# 也不替你开任何业务端口——访问层（Cloudflare Tunnel / Tailscale）按 README 第八节配。
#
# 用法（root）：
#   sudo ./vps-hardening.sh                     # 默认：ufw 拒绝入站 + 放行 sshd 端口、fail2ban、自动更新、UTC
#   sudo ./vps-hardening.sh --hostname srv-01   # 顺带把主机名改成中性值（别写 blade/pentest）
#   sudo ./vps-hardening.sh --disable-ipv6      # 彻底关掉 IPv6（很多 VPS 带公网 v6，忘了它就是后门）
#   sudo ./vps-hardening.sh --no-ssh-allow      # 不自动放行 SSH（仅当 SSH 只走 tailnet/WireGuard 时用）
set -euo pipefail

HOSTNAME_NEW=""
DISABLE_IPV6=0
SSH_ALLOW=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --hostname) HOSTNAME_NEW="${2:-}"; shift 2 ;;
    --disable-ipv6) DISABLE_IPV6=1; shift ;;
    --no-ssh-allow) SSH_ALLOW=0; shift ;;
    -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
    *) echo "未知参数：$1（--help 看用法）" >&2; exit 1 ;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "需要 root：sudo $0" >&2
  exit 1
fi

step(){ printf '\n\033[36m[*]\033[0m %s\n' "$*"; }
ok(){ printf '\033[32m[+]\033[0m %s\n' "$*"; }

step "安装依赖（ufw / fail2ban / unattended-upgrades）"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ufw fail2ban unattended-upgrades >/dev/null

step "时区 → UTC（日志与 {{.Now}} 用服务器本地时区，UTC 不泄露操作者时区）"
timedatectl set-timezone UTC
ok "$(timedatectl show -p Timezone --value)"

step "ufw：默认拒绝入站、允许出站（IPv6 一并管）"
if grep -q '^IPV6=' /etc/default/ufw; then
  sed -i 's/^IPV6=.*/IPV6=yes/' /etc/default/ufw
else
  echo 'IPV6=yes' >> /etc/default/ufw
fi
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null

# 放行 sshd 实际监听的端口：sudo 会清掉 SSH_CONNECTION，所以从 sshd 自己的配置里取。
# 不放行的话，ufw 一启用，**新**的 SSH 连接就进不来了（现有连接靠 conntrack 还在，
# 等你断开就再也连不上——这是这类脚本最常见的事故）。
if [[ "$SSH_ALLOW" -eq 1 ]]; then
  ssh_port="$(sshd -T 2>/dev/null | awk '/^port /{print $2; exit}')"
  ssh_port="${ssh_port:-22}"
  ufw allow "${ssh_port}/tcp" >/dev/null
  ok "已放行 TCP/${ssh_port}（sshd 端口）。改走 tailnet/隧道后撤掉：ufw delete allow ${ssh_port}/tcp"
else
  echo "！未放行 SSH（--no-ssh-allow）。请确认你还有别的入口，再开新连接。"
fi
ufw --force enable >/dev/null
ok "ufw 已启用（已有规则保留）"

step "fail2ban：sshd jail"
cat > /etc/fail2ban/jail.d/sshd.local <<'JAIL'
[sshd]
enabled = true
maxretry = 5
findtime = 10m
bantime = 1h
JAIL
systemctl enable --now fail2ban >/dev/null 2>&1 || systemctl restart fail2ban
ok "已启用（SSH 不在公网时它也不碍事）"

step "自动安全更新"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'APT'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT
systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true
ok "已启用"

if [[ "$DISABLE_IPV6" -eq 1 ]]; then
  step "关闭 IPv6"
  cat > /etc/sysctl.d/99-disable-ipv6.conf <<'SYSCTL'
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
SYSCTL
  sysctl --system >/dev/null
  ok "已关闭（重启后仍生效）"
fi

if [[ -n "$HOSTNAME_NEW" ]]; then
  step "主机名 → $HOSTNAME_NEW"
  if [[ "$HOSTNAME_NEW" =~ ^[a-z0-9][a-z0-9-]{0,62}$ ]]; then
    hostnamectl set-hostname "$HOSTNAME_NEW"
    sed -i "s/^127\.0\.1\.1.*/127.0.1.1\t${HOSTNAME_NEW}/" /etc/hosts 2>/dev/null || true
    ok "已设置（别在主机名里写用途）"
  else
    echo "！主机名只允许小写字母/数字/短横线：$HOSTNAME_NEW" >&2
  fi
fi

step "体检：监听与入站规则"
echo "--- 非 loopback 监听（应为空，或只有你计划中的入口）---"
ss -tlnp | awk 'NR==1 || $4 !~ /^(127\.|\[::1\])/'
echo "--- ufw 规则 ---"
ufw status numbered

cat <<'TODO'

还需要你**手工**确认（脚本刻意不碰）：
  · 云厂商的安全组 / 安全列表（ufw 之外还有一层，两家都要配）
  · sshd_config：只允许密钥登录（见 deploy/README.md 第八节）
  · 反向解析 PTR 别写业务名（VPS 控制台里设）
  · 从**另一台机器**按 README 第六节的自检清单验一遍（全端口扫描 + 测绘引擎按 IP 查）
TODO
