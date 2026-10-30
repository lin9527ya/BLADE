# Clash 出口切换配置（BLADE WAF 自动换 IP）

> 适用版本：`0.7.3-12-g51e859e` 及以后。
>
> 目标：目标 WAF 封禁出口 IP 时，让 BLADE 通过 Clash 的 **external-controller** 在
> Selector 组内换一个节点，并验证出口 IP 真的变化。**任何情况下都不允许直连真实 IP。**

---

## 1. 原理与安全模型

```
BLADE worker/planner
  │  全局代理池 = http://<clash>:7898（专用 listener）
  ▼
Clash listener blade-in (port 7898, proxy: BLADE)
  │  被 listener 强制绑定到 BLADE 组，规则/global 模式都绕不开
  ▼
Selector 组 BLADE
  ├─ 节点 A  ← BLADE 自动切换其中之一
  ├─ 节点 B
  └─ 节点 C
```

BLADE 的安全约束（代码内置）：

- 只选 **非 DIRECT / REJECT / PASS** 的**叶子节点**；嵌套组不会被选为切换目标。
- 切换后经**当前生效代理**查询出口 IP：必须变化、且不等于本机网卡地址。
- 全部候选失败则恢复原节点并报错；没有可用代理可验证出口时**拒绝切换**。
- 不存在任何直连回退。

Clash 侧必须做对两件事：

1. 开 `external-controller`（BLADE 用它查询和切换节点）。
2. 给 BLADE 一个**专用 listener**，用 `proxy: BLADE` 强制走 Selector 组。只把 BLADE 的代理池指向这个 listener。

> 为什么必须专用 listener：如果 BLADE 走 Clash 的规则口（如 7897）或 global 入口，
> 即使 BLADE 切了组，Clash 仍可能把目标判成 `DIRECT`——换节点不会改变目标出口，
> 真实 IP 照样泄漏。专用 listener 把这条入口锁死在 Selector 组上。

---

## 2. Clash / mihomo 配置示例

### 2.1 最小可用配置（直连节点列表）

```yaml
# /etc/mihomo/config.yaml（路径按发行版调整）
mixed-port: 7897            # 你自己的日常入口，BLADE 不使用它
allow-lan: true
bind-address: "*"
mode: rule                  # global 也可以；专用 listener 会覆盖为 BLADE 组

external-controller: 0.0.0.0:9090
secret: "换成足够长的随机口令"   # 必填；BLADE 设置页填同一个值

proxies:
  - name: "node-a"
    type: ss
    server: 203.0.113.10
    port: 443
    cipher: aes-128-gcm
    password: "xxxx"
  - name: "node-b"
    type: vmess
    server: 203.0.113.11
    port: 443
    uuid: "xxxx"
    alterId: 0
    cipher: auto
  # …更多节点

proxy-groups:
  - name: BLADE
    type: select
    proxies:
      - "node-a"
      - "node-b"
      # 不要放 DIRECT / REJECT；也不要只放嵌套组（BLADE 只选叶子节点）

listeners:
  - name: blade-in
    type: mixed
    listen: 0.0.0.0
    port: 7898
    udp: true
    proxy: BLADE            # 关键：这条入口只能走 BLADE 组
```

### 2.2 订阅 / provider 写法

```yaml
proxy-providers:
  airport:
    type: http
    url: "https://your-subscription-url"
    interval: 3600
    path: ./providers/airport.yaml
    health-check:
      enable: true
      url: https://www.gstatic.com/generate_204
      interval: 300

proxy-groups:
  - name: BLADE
    type: select
    use:
      - airport
    # 如 provider 里含 DIRECT/REJECT，BLADE 会自动跳过；但建议在 provider 侧就过滤

listeners:
  - name: blade-in
    type: mixed
    listen: 0.0.0.0
    port: 7898
    udp: true
    proxy: BLADE
```

### 2.3 global 模式说明

- `mode: global` 时，普通入口（mixed-port）走 `GLOBAL` 组。
- 专用 listener 带 `proxy: BLADE` 时，这条入口仍走 **BLADE** 组，不受 GLOBAL 影响。
- 所以把 BLADE 的代理池指向 7898 后，BLADE 设置里的组名填 `BLADE` 即可，**不要填 `GLOBAL`**。
- 如果你没有专用 listener、只能走 global 入口，则组名必须填 `GLOBAL`，且切换会影响整台机器的所有流量——不推荐。

---

## 3. 在 VPS 上部署 Clash

### 3.1 安装与启动（mihomo 示例）

```bash
# 以实际发行版/架构替换下载地址
mkdir -p /etc/mihomo /var/lib/mihomo
# 将 mihomo 二进制放到 /usr/local/bin/mihomo
mihomo -v

# systemd
cat >/etc/systemd/system/mihomo.service <<'UNIT'
[Unit]
Description=mihomo
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mihomo -d /etc/mihomo
Restart=on-failure
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now mihomo
```

如果启用 TUN，需要额外 capability/内核模块；BLADE 只用专用 listener，**不要求开 TUN**。

### 3.2 防火墙（重要）

只允许 BLADE 所在主机访问：

```bash
# 示例：BLADE 在 192.168.1.50
ufw allow from 192.168.1.50 to any port 7898 proto tcp
ufw allow from 192.168.1.50 to any port 9090 proto tcp
# 不要对公网开放 7898/9090
```

- `9090` 是 Clash 控制器：拿到 secret 就能改节点/读配置，**绝不能公网暴露**。
- `7898` 是代理入口：公网开放等于开放代理，会被滥用。
- 如果 BLADE 和 Clash 在同一台 VPS，把地址换成 `127.0.0.1`，并把 `listen` /
  `external-controller` 绑到 `127.0.0.1`。

---

## 4. BLADE 侧配置

进入：**系统设置 → 网络与代理**。

### 4.1 全局代理池

```
http://<clash-host>:7898
```

只填这一个地址；不要填 7897/7890 等规则口。健康检查 URL 默认
`https://www.gstatic.com/generate_204` 即可。

### 4.2 Clash 出口切换

| 字段 | 值 |
|---|---|
| 控制器地址 | `http://<clash-host>:9090` |
| secret | 与 Clash `secret` 一致（只写不回显） |
| Selector 组名 | `BLADE`（不是 GLOBAL，除非你没有专用 listener） |
| 自动切换 | 开启 |
| 切换冷却 | 120 秒（建议 60–300） |

保存后点「立即切换出口」应返回：

```
Clash 出口已切换：node-a → node-b（组 BLADE，出口 IP x.x.x.x）
```

---

## 5. 验证步骤

### 5.1 控制器可达

```bash
curl -H "Authorization: Bearer <secret>" http://<clash-host>:9090/version
curl -H "Authorization: Bearer <secret>" http://<clash-host>:9090/proxies \
  | jq '.proxies | to_entries[] | select(.value.type=="Selector") | {name:.key, now:.value.now, all:.value.all}'
```

### 5.2 确认专用 listener 真的绑定了 BLADE 组

打开一条长连接，再从 Clash 的 connections 看链：

```bash
(curl -s -m 8 -x http://<clash-host>:7898 -o /dev/null \
  'https://speed.cloudflare.com/__down?bytes=50000000' &)
sleep 2
curl -H "Authorization: Bearer <secret>" http://<clash-host>:9090/connections \
  | jq '.connections[].chains' | head
```

应看到类似：

```json
["node-a", "BLADE"]
```

如果看到 `["DIRECT"]` 或链里没有 `BLADE`，说明 listener 没绑对，**先修这个再谈切换**。

### 5.3 切换前后出口 IP

```bash
echo "before: $(curl -s -x http://<clash-host>:7898 https://api.ipify.org)"
# 在 BLADE 设置页点「立即切换出口」
echo "after:  $(curl -s -x http://<clash-host>:7898 https://api.ipify.org)"
```

两个 IP 必须不同，且都应等于 BLADE 状态里显示的出口 IP。

### 5.4 手动 API（可选）

```bash
TOKEN="<BLADE 登录 JWT>"
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"reason":"手动验证"}' \
  http://127.0.0.1:9898/api/settings/proxy/egress/rotate
```

---

## 6. 自动触发与 Agent 工具

### 6.1 Agent 工具

worker / planner / 主 Agent 都有 `rotate_egress`：

- `reason` 必填：目标、拦截现象（403/429/挑战页/厂商指纹）、为什么判断是出口 IP 被封。
- 回执：新节点 + 新出口 IP。
- 未配置控制器、组里没有安全节点、出口 IP 未变化时会返回明确错误，不会直连。

退避纪律提示词已写：确认出口 IP 被封时调用；切完做一次最小请求复测，仍被拦就换方向。

### 6.2 自动检测

- 扫描 worker 工具输出（Bash / 浏览器 MCP 等）。
- 强 WAF 指纹：Cloudflare / Incapsula / Akamai / Sucuri / AWS WAF / ModSecurity 等
  + 403/429。
- 同一任务 2 分钟内连续 2 次命中、且过了冷却 → 自动切一次，并在任务时间线留一条
  「检测到 WAF 拦截，已自动切换 Clash 出口」。
- 普通登录 403、鉴权失败不会触发。

> 自动切换只换出口，不改变攻击构造。如果 WAF 封的是账号、ASN、指纹或 payload，
> 换节点也可能继续被拦；这时应按退避纪律停手/换方向。

### 6.3 多节点轮转

连续切换会沿组内顺序**向前推进**（不会每次从头选，避免在两个节点之间来回跳）。
如果某个候选的出口 IP 与上次相同（例如多个节点共用 Cloudflare WARP 出口），会自动
跳过并继续尝试下一个；单个节点最长尝试 12 个候选。

---

## 7. 故障排查

| 现象 | 原因 / 处理 |
|---|---|
| `连接 Clash 控制器失败：context deadline exceeded` | 控制器未监听/防火墙拦截/地址端口错误。确认 `external-controller` 与 `curl /version`。 |
| `HTTP 401` | secret 为空或不一致。设置页重新填写。 |
| `Clash 代理组 "xxx" 不存在` | 组名拼错；先用 `/proxies` 查 Selector 名称。 |
| `类型是 URLTest，不是 Selector` | BLADE 只能手动切 Selector；把组改成 `type: select` 或另建一个 Selector。 |
| `组里没有可用的非 DIRECT 节点` | 组里只有 DIRECT/REJECT 或只有嵌套组；直接把真实节点加入 BLADE 组。 |
| `出口代理未配置或已阻断` | BLADE 代理池没有健康候选（7898 不通）。先修代理池，再切换。 |
| `出口刚切换过，请 N 秒后再试` | 冷却未到；等冷却或调小冷却时间。 |
| `节点 x 的出口 IP 仍为 y（未变化）` | listener 没绑该组、目标走的是别的入口，或节点共享同一出口 IP（如 Cloudflare WARP）。用 `/connections` 看 `chains`，换不同 ASN/地区的节点。 |
| 切换成功但目标仍被封 | WAF 封的可能不是 IP，而是账号/ASN/指纹/payload；按退避纪律换方向，不要重放同一 payload。 |
| 切完出口 IP 与本机公网 IP 相同 | 说明流量没走代理（规则/入口绕过）。立即停手，检查 listener 的 `proxy:` 与 BLADE 代理池地址。 |

---

## 8. 上线检查清单

- [ ] Clash `external-controller` 已开，secret 为强随机值。
- [ ] 9090 只对 BLADE 主机开放，未暴露公网。
- [ ] 7898 专用 listener 存在，`proxy: BLADE`，只对 BLADE 主机开放。
- [ ] BLADE 全局代理池只填 `http://<clash-host>:7898`。
- [ ] BLADE 设置里组名 = listener 绑定的组（如 `BLADE`）。
- [ ] `/connections` 里能看到 `["节点", "BLADE"]`，不是 `DIRECT`。
- [ ] 手动切换一次，出口 IP 前后不同，且与 BLADE 状态一致。
- [ ] 自动切换已开，冷却 60–300 秒。
- [ ] 知道真实 IP 泄漏的停止条件：发现出口 IP 等于本机公网 IP 时立即停手排查。
