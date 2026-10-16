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

**ARTEX 平台侧已经默认按最安全的一档做**（见下面第五节）：不显式写地址就只绑 loopback。

---

## 二、方案 A（推荐）：源站零入站端口

### A-1 Cloudflare Tunnel

```bash
# VPS 上
cloudflared tunnel login
cloudflared tunnel create artex
cp deploy/cloudflared.yml /etc/cloudflared/config.yml   # 填上 tunnel id 与凭据路径
cloudflared tunnel route dns artex console.example.com
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

VPS 上什么都不用改（ARTEX 默认就绑 loopback）。想更省心就用 Tailscale/WireGuard，把它当内网机器用。

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
   并把 `proxy_buffering off`（ARTEX 的活动流是 SSE，开着缓冲会"跑一会儿就断"）。

> 域名本身别带业务含义（`pentest.`、`attack.`、公司名都别用）：域名是公开的。

---

## 四、进程守护（systemd）

仓库里的 `start.sh` 只做一件事：把 artex 跑起来、按退出码决定要不要再拉起。注册成服务用
`deploy/artex.service`：

```bash
sudo cp deploy/artex.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now artex
journalctl -u artex -f
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
```

**过了 ①–⑤ 才算"默认不暴露"**；① 里若出现 `*:8787` 或 `0.0.0.0:8787`，就是没做对。

---

## 七、还值得加（运维层，平台不管）

- **fail2ban**：ARTEX 自身有登录节流（5 次失败锁 5 分钟、全局计数、内存态），但公网形态下
  按 IP 封禁更有意义——反代日志 + fail2ban 是标准做法。
- **Cloudflare Access**：Tunnel 或代理形态下都建议加，等于在登录页之前再放一道门。
- **只跑需要的服务**：VPS 上别顺手装一堆东西；每一个开放端口都是给自己加的一分可见度。
