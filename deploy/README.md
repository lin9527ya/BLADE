# 公网部署：隐身与加固

把一个渗透平台放到公网上，被扫到本身就是一个问题——**一台"看起来像个攻击平台"的机器，
对防守方、测绘引擎的持有者、以及扫全网找跳板的人来说，都是值得记录的目标**。

这份文档讲两件事：**怎么让它不被扫到**，以及**扫到了也进不去**。

---

## 一、先理解三件事（否则会做错方向）

**1. 测绘引擎收录的前提是"扫到你的端口"。**
FOFA / Shodan / ZoomEye / Censys 都是全 IPv4 扫描 + 协议指纹。所以：
**"绑到 loopback"或"只对 Cloudflare 开放"是唯一有效的手段**；改标题、换 favicon 只是
"扫到了认不出是什么"的辅助——它救不了"端口开着"这件事。

**2. 用公开 CA 的证书 = 域名必然公开。**
证书透明日志（CT log）是公开的，Censys 这类引擎直接读它。Cloudflare 的**边缘证书**是公开 CA
签的 → **你用的域名会进 CT 日志**（改不掉，除非不用域名）。
但**源站 IP 可以不公开**——只要没人能从你的域名或历史记录推出它。

**3. 隐身的强弱顺序（照着选）**

| 方案 | 源站在公网上开端口吗 | 强度 |
|---|---|---|
| **Cloudflare Tunnel** | **一个都不开**（cloudflared 主动外连） | ★★★★★ |
| SSH 隧道 / WireGuard / Tailscale | 只开 SSH（或 UDP） | ★★★★ |
| CF 代理 + 防火墙只放 CF 段 | 开 443，只对 CF 可见 | ★★★ |
| 公网直连 + 改标题换端口 | 开 8787，全网可见 | ★（只是"不那么显眼"） |

> **SSH 也是被测绘的目标。** Shodan 对 22 端口的 banner 收录得很勤，所以"SSH 隧道"这一档
> 只解决"应用不可见"，**不解决"这台机器不可见"**。要把 SSH 也藏起来：让它只监听 tailnet
> （`tailscale up --ssh --shields-up`）或只对 WireGuard 内网开放，公网 22 一律关闭。

**BLADE 平台侧已经默认按最安全的一档做**（见下面第五节）：不显式写地址就只绑 loopback。

---

## 二、方案 A（推荐）：源站零入站端口

### A-1 Cloudflare Tunnel

```bash
# VPS 上
cloudflared tunnel login
cloudflared tunnel create blade
cp deploy/cloudflared.yml /etc/cloudflared/config.yml   # 填上 tunnel id 与凭据路径
cloudflared tunnel route dns blade console.example.com
cloudflared tunnel ingress validate
sudo cloudflared service install
```

**这样 VPS 上一个入站端口都不需要开**（防火墙全关也行）。再在 Cloudflare Zero Trust 里给这个
hostname 加一条 **Access 策略**（邮箱 OTP 或 Service Token）——域名被人知道也进不来。

### A-2 SSH 隧道（最省事）

```bash
# 本机
ssh -N -L 8787:127.0.0.1:8787 you@your-vps
# 浏览器打开 http://127.0.0.1:8787
```

VPS 上什么都不用改（BLADE 默认就绑 loopback）。想更省心就用 Tailscale/WireGuard，把它当内网机器用。

---

## 三、方案 B：公网域名直连（走 Cloudflare）

必须**三件都做**，缺一件就白做：

1. **防火墙只放 Cloudflare 段**（否则扫描器直连你的真实 IP，CF 完全白搭）：
   ```bash
   sudo deploy/ufw-cloudflare.sh
   ```
2. **源站证书用 Cloudflare Origin Certificate**（15 年、**不进 CT 日志**）。
   **不要**用 Let's Encrypt——那会把域名送进 CT 日志。
3. **反代按 `deploy/nginx-cloudflare.conf` 配**：`set_real_ip_from` + `CF-Connecting-IP`，
   并把 `proxy_buffering off`（BLADE 的活动流是 SSE，开着缓冲会"跑一会儿就断"）。

> 域名本身别带业务含义（`pentest.`、`attack.`、公司名都别用）：域名是公开的。

---

## 四、进程守护（systemd）

仓库里的 `start.sh` 只做一件事：把 blade 跑起来、按退出码决定要不要再拉起。注册成服务用
`deploy/blade.service`：

```bash
sudo cp deploy/blade.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now blade
journalctl -u blade -f
```

它与 `start.sh` 的退出码约定是配套的：`0` = 停（不重启）、`75` = "换新版本再跑"（自更新用）。

---

## 五、平台侧已经替你做的加固

| 项 | 行为 | 怎么改 |
|---|---|---|
| **默认只绑 loopback** | `-addr` 默认 `127.0.0.1:8787`；Docker 镜像与 compose 同样只映射到 127.0.0.1 | 要暴露必须显式写 `-addr :8787`，日志会提醒你 |
| **开放代理护栏** | 流量代理（`-proxy`）绑到非 loopback 时**拒绝启动**（go-mitmproxy 不校验任何来源，绑出去就是无认证开放代理） | 确需如此时显式加 `-allow-open-proxy` |
| **`/api/health` 不自报家门** | 只回 `{"ok":true}`（原来自报 service/version，一请求就是明牌指纹） | 版本走需登录的 `GET /api/system/info` |
| **CORS 默认不发头** | 同源前端不需要；只对设置 `cors_allowed_origins` 里显式列出的来源发 | 填 `*` 才是真全开放，得自己写出来 |
| **显示名可配** | 设置 `site_name` 覆盖运行时标题与显示名 | 空 = 内置名；**静态 HTML 里的名字**是构建期常量，见下 |

### 想连"爬虫抓到的 HTML"里的名字也换掉

扫描器抓的是**未执行 JS 的静态 HTML**，那里的名字来自
`web/src/config/app-config.ts`（`meta.title` / `meta.description` / `name`）与 `web/public/logo.*`。
改完重新构建（`npm run build:static` + `-tags embedui` 重编译），或直接用 `-tags` 不带 `embedui`
的构建（那时前端是外部部署的）。

---

## 六、部署后自检（照这个清单核一遍）

```bash
# ① VPS 上到底开了哪些入站端口？（应该是：只有你计划开的那些）
sudo ss -tlnp

# ② 从**另一台机器**扫一下你的 VPS 公网 IP（不是域名——域名走 CF 看不出问题）
nmap -Pn -p 8787,8788,5432 <你的VPS IP>       # 三个都应该是 filtered/closed

# ③ 直接访问源站 IP 的 API：应该连不上（而不是返回 {"ok":true}）
curl -m 5 http://<你的VPS IP>:8787/api/health

# ④ 免认证端点不该说出自己是谁
curl -s https://你的域名/api/health             # 期望：{"ok":true} —— 没有 service/version

# ⑤ 数据库不该对外
nmap -Pn -p 5432 <你的VPS IP>

# ⑥ 全端口：不只是 8787——任何一个监听都算分（-p- 扫 65535 个端口）
nmap -Pn -p- --min-rate 2000 <你的VPS IP>

# ⑦ IPv6（VPS 若带公网 v6，很多人只配了 v4 防火墙）
ss -tlnp6                                     # 不该有非 loopback 监听
nmap -6 -Pn -p- --min-rate 2000 <你的VPS IPv6>

# ⑧ 反向解析与域名：PTR 不该是业务名；域名必须解析到 Cloudflare，不能落回源站
host <你的VPS IP>
dig +short console.example.com

# ⑨ 证书透明日志：只该有 CF 的边缘证书，且没有多余的子域
#    https://crt.sh/?q=example.com

# ⑩ 测绘引擎按 IP 查：应该"查无此机"
#    Shodan: ip:<IP>        Quake: ip:"<IP>"       Censys: ip=<IP>
#    FOFA:   ip="<IP>"      ZoomEye: ip:<IP>
#    没有开放端口就不会被收录；上线满 48 小时后再复查一次，确认"仍然查不到"。
```

**过了 ①–⑩ 才算"默认不暴露"**；① 里若出现 `*:8787` 或 `0.0.0.0:8787`，就是没做对。

---

## 七、还值得加（运维层，平台不管）

- **fail2ban**：BLADE 自身有登录节流（**按来源 IP** 指数退避：5 次失败起锁 1 分钟、每次翻倍
  到 30 分钟；30 分钟无失败自动清零；另有全局失败预算 10 分钟 60 次兜住换 IP 的分布式爆破；
  内存态、重启清零）。真实来源取自 `CF-Connecting-IP` / `X-Real-IP` / `X-Forwarded-For`，
  **只在直连方是 loopback（即确有本机反代）时才信这些头**——直连暴露 8787 时按 TCP 来源计数。
  公网形态下
  按 IP 封禁更有意义——反代日志 + fail2ban 是标准做法。
- **Cloudflare Access**：Tunnel 或代理形态下都建议加，等于在登录页之前再放一道门。
- **只跑需要的服务**：VPS 上别顺手装一堆东西；每一个开放端口都是给自己加的一分可见度。

---

## 八、从零到上线（VPS 版，按顺序做）

> 只要一份可勾选的规格/依赖/必配项/验收表：见 **[VPS-CHECKLIST.md](VPS-CHECKLIST.md)**。
>
> **目标形态先定：公网上一个入站端口都不开。** 浏览器访问走 Cloudflare Tunnel（+ Access），
> SSH/运维走 Tailscale（`--ssh --shields-up`）或 WireGuard。这样这台机器对全网扫描来说就是
> "什么都没开"——测绘引擎没有可收录的服务，后面那些"去指纹"才有意义。

### 1. 系统层（新机器，十分钟）

```bash
# 非 root 运维用户 + 密钥登录。**先验证密钥能登，再关密码**——顺序反了就把自己锁在外面。
adduser --disabled-password --gecos "" ops
install -d -m 700 -o ops -g ops /home/ops/.ssh
# 把本机公钥写进 /home/ops/.ssh/authorized_keys（600, ops:ops），另开一个终端验证 ssh ops@<ip>
sudo tee /etc/ssh/sshd_config.d/99-hardening.conf >/dev/null <<'EOF'
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
EOF
sudo systemctl reload ssh
```

```bash
sudo deploy/vps-hardening.sh --hostname srv-01
# 做：ufw 默认拒绝入站（并放行 sshd 端口，避免把自己锁死）、fail2ban(sshd)、
#     自动安全更新、时区 → UTC；最后打印监听与规则供核对。
```

- **云厂商的安全组 / 安全列表**：也要配（ufw 是机器内，云侧是网络层；两层都要）。
- **IPv6**：多数 VPS 默认带公网 v6。脚本已让 ufw 管 v6；想更彻底就 `--disable-ipv6`。
- **rDNS / PTR**：在 VPS 控制台设成中性值（或保持厂商默认）。**不要**设成 console 域名——那等于把两者绑在一起。
- **主机名**：中性（`srv-01` 这类），别写 blade / pentest。
- **SSH 端口**：改端口只减少噪音，全端口扫描一样能找到；真正的解法是第 3 步"22 也不开公网"。

### 2. 装 BLADE（发布包 + 容器化 Postgres，VPS 上不需要 Go/Node）

```bash
# ① 发布包（Releases 里选 linux-amd64；下载是 HTTPS 出站，不暴露任何端口）
#    资产名形如 blade-<ver>-linux-amd64.zip，<ver> 换成发布号（如 0.5.0）
curl -fL -o /tmp/blade.zip \
  https://github.com/lin9527ya/BLADE/releases/download/v<ver>/blade-<ver>-linux-amd64.zip
sudo apt-get install -y unzip
unzip -q /tmp/blade.zip -d /opt && sudo mv /opt/blade-<ver>-linux-amd64 /opt/blade
sudo useradd --system --home /opt/blade --shell /usr/sbin/nologin blade || true
sudo chown -R blade:blade /opt/blade

# ② Postgres 只在 loopback（数据库端口是全网扫描前几名）
sudo apt-get install -y docker.io     # 或跑仓库里的 install.sh 让它装
PW="$(openssl rand -hex 24)"
docker run -d --name blade-pg -p 127.0.0.1:5432:5432 \
  -e POSTGRES_USER=blade -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=blade \
  -v blade-pg:/var/lib/postgresql/data --restart unless-stopped postgres:16-alpine

# ③ config.json（600）——密码就是上面的 $PW，别写进 shell history 以外的任何公开处
sudo tee /opt/blade/config.json >/dev/null <<JSON
{ "database": { "host": "127.0.0.1", "port": 5432, "user": "blade", "password": "$PW", "dbname": "blade", "sslmode": "disable" } }
JSON
sudo chmod 600 /opt/blade/config.json && sudo chown blade:blade /opt/blade/config.json

# ④ systemd（单元里 User=blade，与上面一致）
#    发布包**不含 deploy/**，单元从仓库取：
sudo curl -fsSL -o /etc/systemd/system/blade.service \
  https://raw.githubusercontent.com/lin9527ya/BLADE/main/deploy/blade.service
sudo systemctl daemon-reload && sudo systemctl enable --now blade
journalctl -u blade -f
```

先别急着开隧道：在 VPS 上 `curl -s 127.0.0.1:8787/api/health` 应回 `{"ok":true}`。

> **初始化必须发生在任何公网入口打开之前。** `POST /api/auth/init` 是免认证的（首次设置密码的
> 设计如此），未初始化的实例被谁先摸到，谁就能把管理员密码设掉——这是个接管窗口，不是提权。
> 顺序：起服务 → 在本机（或 SSH 端口转发）完成初始化 → 再开 Tunnel/Access。
> 在 VPS 上直接初始化：
> ```bash
> curl -sS -X POST 127.0.0.1:8787/api/auth/init \
>   -H 'Content-Type: application/json' -d '{"password":"<你的强密码>"}'
> ```
> （返回的 token 可以丢掉，浏览器里再登录一次即可。）
> 至少要保证 **Access 策略先于第一次公网访问生效**。

> 想跑自己编的版本：本机 `./build.sh`，把 `dist/blade-linux-amd64/blade`、`start.sh`、`skills/`、
> `config.example.json` scp 到 `/opt/blade/`。小 VPS 上现编 Next 前端很慢，不推荐。

### 3. 访问层：零入站

**3a. Cloudflare Tunnel（浏览器访问，推荐）**

```bash
# cloudflared 按 Cloudflare 官方文档安装（apt 仓库或单二进制）
cloudflared tunnel login
cloudflared tunnel create console
cloudflared tunnel route dns console <你的域名>
sudo install -d /etc/cloudflared
sudo cp deploy/cloudflared.yml /etc/cloudflared/config.yml   # 改 tunnel id / 凭据路径 / hostname
cloudflared tunnel ingress validate
sudo cloudflared service install
```

然后在 **Zero Trust → Access → Applications** 给这个 hostname 加策略（One-time PIN 到你的邮箱，
或 Service Token）。**这一步别省**：域名会进证书透明日志（CF 边缘证书，改不掉），Access 才是
"域名被知道了也进不来"的那道门。

**3b. SSH/运维：Tailscale（公网 22 也不开）**

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --ssh --shields-up    # --shields-up：拒绝一切来自 tailnet 之外的入站
sudo ufw delete allow 22/tcp && sudo ufw reload
```

之后 `ssh ops@srv-01`（MagicDNS）。浏览器也可以直接开 `http://<tailnet-ip>:8787`，连域名都不需要。
WireGuard 同理；不想依赖第三方协调服务器可以自建 Headscale。

### 4. 验收与日常

- 从**另一台机器**按第六节 ①–⑩ 验一遍（全端口、IPv6、PTR、DNS、CT、测绘引擎）。
- 域名/URL 不外发；Access 常开；这个域名不要复用到别的项目（`crt.sh` 会把子域全列出来）。
- **出站提醒**：平台的扫描流量从这台机器的 IP 出去，目标侧看到的就是它。需要与 console 解耦时，
  用平台的全局代理设置换出口，或把"扫描执行"与"console"分机器。
- 更新：页面一键更新（`start.sh` 的 75 退出码），或 `systemctl restart blade`——重启后顺手看一眼
  `ss -tlnp`，确认没有多出来的监听。

---

## 九、信任模型：默认同权，可选降权（`BLADE_AGENT_USER`）

**默认（不设置 `BLADE_AGENT_USER`）**：agent 的 Bash 与文件工具都在服务进程内、以**同一个
OS 用户**执行。所以一条被提示注入的 worker 命令，能做管理员能做的任何事：

- 读 `jwt.key`（HS256 签名密钥）→ 自签一个 admin token；
- 读 `config.json`（数据库口令）→ 直连数据库读改数据；
- 直接 `curl 127.0.0.1:8787/api/...` 调管理 API（配合上面两条即可拿到合法 token）。

`intercept` / `guard` 是命令层的软约束（规则 + 模型判断），挡不住"读文件 + 本地 HTTP"
这种由**两条正常命令**拼出来的链。默认没有 OS 级隔离——部署时请把"提示注入 = 控制台全权"
写进自己的风险评估。

### 9.1 开启降权

前提：Norma ≥ v0.5.3（`tool.SetExecHook` / `tool.SetPathGuard`），BLADE 以 **root** 启动，
且 `-data` 目录**不包含**可执行文件目录（`jwt.key` / `config.json` 在那里，落在 data 里
会直接拒绝启动——"看着隔离、实则没隔"比不隔离更危险）。

```bash
# 1) 建一个没有任何附加组的服务账号（家目录放在 /var/lib/blade-worker）
sudo useradd --system --create-home --home-dir /var/lib/blade-worker \
     --shell /usr/sbin/nologin blade-worker

# 2) systemd 里以 root 起服务，只加这一个环境变量
#    /etc/systemd/system/blade.service
[Service]
User=root
Environment=BLADE_AGENT_USER=blade-worker
# 可选：额外允许文件工具访问的根（逗号/冒号分隔）
# Environment=BLADE_AGENT_READ_ROOTS=/opt/wordlists:/srv/engagement

# 3) 重启
sudo systemctl daemon-reload && sudo systemctl restart blade
```

启动日志会出现一行 `[isolation] agent 以 blade-worker (...)`，说明生效。

**开启了什么**：

| 面 | 行为 |
|---|---|
| Bash（含后台任务、交互式 shell）、长任务 `run_execution`、自定义工具 | 以 `blade-worker` 身份执行（`SysProcAttr.Credential`，只保留主组） |
| 子进程环境 | 按"疑似凭据"黑名单清洗：`ANTHROPIC_*`/`OPENAI_*`/`BLADE_*`/`AWS_*`/`GH_*`… 以及名字含 `API_KEY`/`SECRET`/`TOKEN`/`PASSWORD`/`CREDENTIAL` 的变量；`HOME`/`USER`/`SSH_AUTH_SOCK` 等改写或剔除 |
| 文件工具（Read/Write/Edit/MultiEdit/LS/Glob/Grep） | 只允许 `data/`、skill 目录、`BLADE_AGENT_READ_ROOTS` 内的**真实路径**（软链解析后再比对；`Grep` 在隔离模式下不再走 rg 子进程，逐文件过守卫） |
| `data/` 树 | 启动时属主交给 `blade-worker`，目录 `2775`（setgid + 组可写）；run 目录由服务端创建为 `0775`，agent 写得进自己的产物目录 |

**仍然挡不住的（如实说明）**：

- 服务端以 root 跑（降权本来就需要），主机上**同 root 域**的其它进程、内核漏洞不在模型内；
- agent 依旧能访问网络、能读 `data/` 下的工作区与 `traffic/` 抓包记录（那是它自己的产物面）；
- 没被模式覆盖的新凭据环境变量会漏——新增带凭据的变量时把前缀补进
  `server/agent_isolation.go` 的 `agentEnvDenyPrefixes`；
- 容器化（把执行面整体放进独立容器、不挂 `/opt/blade`、不给 DB 凭据）仍是边界更硬的方案；
  数据与密钥分家（`data/` 与密钥/配置不同用户）已经由本机制自动完成。

### 9.2 验收

在任意 worker 的 Bash 里：

```bash
id -u                          # 期望 worker 的 uid（不是 0，也不是服务进程用户）
cat /opt/blade/jwt.key         # 期望 Permission denied
cat /opt/blade/config.json     # 期望 Permission denied
env | grep -iE 'key|token|secret|password'   # 期望为空（或只剩无关项）
cat /etc/passwd                # Bash 下仍可（shell 本来就是全盘读）；用文件工具 Read 才应被拒
```

文件工具 `Read /etc/passwd` 期望返回"拒绝访问…隔离模式下文件工具只允许访问…"；
在 `data/` 与 skill 目录内的读写必须照常工作（包括相对路径与工作区里的软链）。

**注意**：开启后不能再用 `sudo`/`apt` 等需要 root 的命令操作宿主机——agent 的扫描本身
（nmap/nuclei/ffuf）不需要 root；确实需要高权限工具时，把那台机器作为独立执行节点，
而不是放宽 worker 的权限。
