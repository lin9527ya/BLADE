# VPS 配置清单（一页）

> 适用：BLADE 发布包 + 容器化 PostgreSQL，公网**零入站**（Cloudflare Tunnel / Tailscale）。
> 这是"要什么、配什么、怎么验"的勾选表；完整步骤与原理见 [`deploy/README.md`](README.md) §八。

## 1. 机器规格

| 场景 | CPU / 内存 | 磁盘 |
|---|---|---|
| 轻量（1–2 任务、3 worker） | 2 vCPU / 2 GB | 30–40 GB SSD |
| **常规（推荐）** | **4 vCPU / 8 GB** | **80 GB SSD** |
| 重度（多任务、大字典/大流量采集） | 8 vCPU / 16 GB | 150 GB+ |

- OS：Debian 12 / Ubuntu 22.04+（amd64 或 arm64）
- 出站：**TCP 443/80 必须通**（LLM、GitHub 更新、扫描目标），DNS 可用，NTP 同步
- 入站：**默认一个不开**（见 §5 的两个例外）

## 2. 软件依赖

| 组件 | 要求 |
|---|---|
| BLADE | Release 里的 `blade-<ver>-linux-amd64.zip`（静态二进制，VPS 不需要 Go/Node） |
| PostgreSQL | 16（Docker `postgres:16-alpine` 即可），**只绑 `127.0.0.1:5432`** |
| 渗透工具 | 平台不打包：nmap / nuclei（含 templates）/ ffuf / gobuster / sqlmap / curl 等按需安装 |
| 访问层 | cloudflared（推荐）或 Tailscale / SSH 隧道 |
| 可选 | Docker（跑 PG）、ufw/fail2ban（加固脚本会装） |

## 3. 目录与账号

```
/opt/blade/
├── blade            # 发布包二进制
├── start.sh         # 守护循环（退出码 75 = 自更新重启）
├── skills/          # 技能目录（随发布包/仓库）
├── data/            # 抓包、产物、工作区（持续增长，见 §6）
└── config.json      # 数据库 DSN，0600
```

```bash
sudo useradd --system --home /opt/blade --shell /usr/sbin/nologin blade
sudo chown -R blade:blade /opt/blade
```

## 4. 必配项（逐项勾）

| # | 项 | 值 / 命令 | 说明 |
|---|---|---|---|
| 1 | `config.json` | `{"database":{"host":"127.0.0.1","port":5432,"user":"blade","password":"…","dbname":"blade","sslmode":"disable"}}`，`chmod 600`，属主 `blade` | 密码用 `openssl rand -hex 24` 生成 |
| 2 | PostgreSQL | `docker run -d --name blade-pg -p 127.0.0.1:5432:5432 -e POSTGRES_USER=blade -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=blade -v blade-pg:/var/lib/postgresql/data --restart unless-stopped postgres:16-alpine` | 端口绝不能公网可达 |
| 3 | systemd | `deploy/blade.service` → `/etc/systemd/system/`，`systemctl enable --now blade` | `User=blade`、`RestartForceExitStatus=75`、`BLADE_CONFIG=/opt/blade/config.json` |
| 4 | 监听面 | `-addr` 保持 `127.0.0.1:8787`；`-proxy` 保持 `127.0.0.1:8788` | 默认即如此，**不要**改成 `:8787` |
| 5 | 初始化 | **开公网入口之前**：`curl -X POST 127.0.0.1:8787/api/auth/init -H 'Content-Type: application/json' -d '{"password":"<强密码>"}'` | `/api/auth/init` 免认证，暴露后再初始化 = 接管窗口 |
| 6 | 防火墙 | `sudo deploy/vps-hardening.sh --hostname srv-01` + 云厂商安全组 | ufw 管 v4/v6；两层都要配 |
| 7 | 访问层 | Cloudflare Tunnel + Zero Trust Access（邮箱 OTP / Service Token），或 Tailscale `--ssh --shields-up` | 域名会进 CT 日志，Access 才是那道门 |
| 8 | LLM | 登录后在 UI 配 LLM Profile（Anthropic / OpenAI-compatible） | key 存库，不落环境变量；确认能出站访问 |
| 9 | 工具 | 安装实际要用的扫描器与字典/templates | agent 通过 Bash 调用，缺工具只会在运行时失败 |
| 10 | 授权边界 | 设置 `intercept_scope_mode`（建议 `ask`/`deny`）；按需配拦截规则、凭据预算 | 默认 `ask` 才会真正拦范围外目标 |
| 11 | 可选降权 | `useradd --system --create-home --home-dir /var/lib/blade-worker blade-worker`；unit 改 `User=root` + `Environment=BLADE_AGENT_USER=blade-worker` | 降权要求 root 启动，与默认 unit 的 `User=blade` 冲突；`data/` 属主启动时自动交给 worker |
| 12 | 主机标识 | 中性主机名；PTR 用厂商默认/中性值，**不要**写成 console 域名 | 别把 IP 和业务域名绑在一起 |

## 5. 零入站的两个例外

1. **反弹 shell / 任意 TCP 回连**：零入站下 VPS 收不到回连。需要这类打法时另配回连节点/公网监听器（Cloudflare Tunnel 只代理 HTTP，不解决任意 TCP）。
2. **出站 IP = 扫描源**：平台的扫描流量从 VPS 的 IP 出去，目标侧看到的是它。需要与 console 解耦时，用全局代理换出口，或拆成独立执行节点。

## 6. 备份、磁盘与更新

- **备份**：`pg_dump`（含 JWT 签名密钥与全部业务数据）+ `/opt/blade/data` + `config.json` + `skills/`
- **磁盘**：`data/` 里的抓包与产物持续增长；用平台的存储清理或外部监控，别等磁盘满
- **更新**：页面一键更新（`start.sh` 退出码 75）或替换二进制后 `systemctl restart blade`；启动即幂等迁移，升级前先备份

## 7. 上线验收（过了才算完成）

- [ ] `ss -tlnp`：没有 `0.0.0.0:8787` / `*:8787` / 公网 5432
- [ ] 另一台机器 `nmap -Pn -p- <VPS IP>`：无开放端口（计划内的除外）
- [ ] `curl -m 5 http://<VPS IP>:8787/api/health`：连不上
- [ ] `curl https://<域名>/api/health`：只回 `{"ok":true}`，无 service/version
- [ ] IPv6：`ss -tlnp6` 无非 loopback 监听；`nmap -6 -p-` 同样无开放端口
- [ ] PTR 中性；域名只解析到 Cloudflare；`crt.sh` 无多余子域
- [ ] 上线 48 小时后复查 Shodan/FOFA/Quake/Censys/ZoomEye：仍"查无此机"
- [ ] 初始化已完成、Access 策略已生效、LLM 调用可用

> ①–⑩ 的完整命令与判据见 [`deploy/README.md` §六](README.md#六部署后自检照这个清单核一遍)。
