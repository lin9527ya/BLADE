# Clash 出口切换配置（BLADE 按任务隔离出口）

> 适用版本：`0.9.11-3-g8bd6541` 及以后。
>
> 目标：目标 WAF 封禁某个出口 IP 时，让 BLADE 只切换**当前任务**绑定的区域组，
> 不影响其它任务；同一区域最多连续试 3 个节点，再换到下一个地区；当前任务组全部失败时
> 自动回退默认组，并优先选择尚未尝试过的地区。**任何情况下都不允许直连真实 IP。**

---

## 1. 架构

```
Task A ──► 亚洲 listener 17891 ──► Selector 🌏 亚洲节点
Task B ──► 欧洲 listener 17892 ──► Selector 🌍 欧洲节点
Task C ──► 美洲 listener 17893 ──► Selector 🌎 美洲节点

未分组任务 / 手动切换 / 组耗尽回退
          └─► 默认 listener 17890 ──► Selector 🧭 默认节点（全部节点）
```

关键点：

- **隔离粒度是任务**，同一个任务的多个 worker 共享同一个组和出口 IP。
- 不同任务绑定不同组时，可以同时使用不同地区的出口 IP。
- 每个组有自己的切换状态、冷却和最近出口 IP。
- 默认组包含全部节点，只作为手动入口和任务组耗尽后的回退，不放入任务组池。
- 任务组内按地区分桶：同一地区最多连续尝试 3 个节点，然后换下一个地区。
- 任务组全部地区都失败时，自动回退默认组；回退时跳过已经试过的地区。

---

## 2. Clash / mihomo 配置

### 2.1 节点与区域组

下面的节点名只是占位符，实际使用时换成订阅里的节点名。

```yaml
mixed-port: 7897            # 日常入口，BLADE 不使用
allow-lan: true
bind-address: "*"
mode: rule

external-controller: 0.0.0.0:9090
secret: "换成足够长的随机口令"

proxies:
  - { name: "🇭🇰 HK-01", type: ss, server: 203.0.113.10, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇭🇰 HK-02", type: ss, server: 203.0.113.11, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇹🇼 TW-01", type: ss, server: 203.0.113.12, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇯🇵 JP-01", type: ss, server: 203.0.113.13, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇸🇬 SG-01", type: ss, server: 203.0.113.14, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇬🇧 UK-01", type: ss, server: 203.0.113.15, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇩🇪 DE-01", type: ss, server: 203.0.113.16, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇺🇸 US-01", type: ss, server: 203.0.113.17, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  - { name: "🇧🇷 BR-01", type: ss, server: 203.0.113.18, port: 443, cipher: aes-128-gcm, password: "xxxx" }
  # …其余节点

proxy-groups:
  # 默认组：全部实际节点；不要放 DIRECT / REJECT / 信息占位节点
  - name: "🧭 默认节点"
    type: select
    proxies:
      - "🇭🇰 HK-01"
      - "🇭🇰 HK-02"
      - "🇹🇼 TW-01"
      - "🇯🇵 JP-01"
      - "🇸🇬 SG-01"
      - "🇬🇧 UK-01"
      - "🇩🇪 DE-01"
      - "🇺🇸 US-01"
      - "🇧🇷 BR-01"

  # 区域组：BLADE 的任务组池从这里取
  - name: "🌏 亚洲节点"
    type: select
    proxies:
      - "🇭🇰 HK-01"
      - "🇭🇰 HK-02"
      - "🇹🇼 TW-01"
      - "🇯🇵 JP-01"
      - "🇸🇬 SG-01"

  - name: "🌍 欧洲节点"
    type: select
    proxies:
      - "🇬🇧 UK-01"
      - "🇩🇪 DE-01"

  - name: "🌎 美洲节点"
    type: select
    proxies:
      - "🇺🇸 US-01"
      - "🇧🇷 BR-01"
```

> 需要四区域方案时，可以把不归属亚洲/欧洲/美洲的节点单独放进
> `🌐 其他节点`，并在下方多开一个 listener。当前 BLADE 部署把土耳其/乌克兰
> 并入了欧洲组。

### 2.2 五个专用 listener

每个 listener 必须用 `proxy:` 强制绑定自己的 Selector 组；这样即使 Clash 是
`global` 模式，也不会走错组。

```yaml
listeners:
  - name: blade-default
    type: mixed
    listen: 0.0.0.0          # BLADE 在另一台机器时用 0.0.0.0；同机可用 127.0.0.1
    port: 17890
    udp: true
    proxy: "🧭 默认节点"

  - name: blade-asia
    type: mixed
    listen: 0.0.0.0
    port: 17891
    udp: true
    proxy: "🌏 亚洲节点"

  - name: blade-europe
    type: mixed
    listen: 0.0.0.0
    port: 17892
    udp: true
    proxy: "🌍 欧洲节点"

  - name: blade-americas
    type: mixed
    listen: 0.0.0.0
    port: 17893
    udp: true
    proxy: "🌎 美洲节点"
```

如果开了 `🌐 其他节点`，再加一个 `blade-other`，例如端口 `17894`。

### 2.3 订阅 / provider 写法

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
  - name: "🧭 默认节点"
    type: select
    use: [airport]        # 若订阅里含 DIRECT/REJECT，BLADE 会自动跳过

  - name: "🌏 亚洲节点"
    type: select
    use: [airport]
    # 现实配置里通常按节点名前缀拆成多个组，见下文“按名称分组”
```

如果 provider 无法直接按国家拆分，可以用 Clash 的 `filter` 或生成脚本，把节点分别写进
亚洲/欧洲/美洲三个 Selector 组。**不要让任务组包含 DIRECT/REJECT 节点**。

---

## 3. 在 VPS 上部署 Clash

### 3.1 安装与启动（mihomo）

```bash
mkdir -p /etc/mihomo /var/lib/mihomo
# 将 mihomo 二进制放到 /usr/local/bin/mihomo
mihomo -v

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

修改配置后先校验，再重启：

```bash
mihomo -t -d /etc/mihomo -f /etc/mihomo/config.yaml
systemctl restart mihomo
```

### 3.2 防火墙

只允许 BLADE 所在主机访问控制器和 5 个 listener：

```bash
# 示例：BLADE 在 192.168.1.50
for port in 9090 17890 17891 17892 17893; do
  ufw allow from 192.168.1.50 to any port "$port" proto tcp
done
# 不要对公网开放 9090 / 17890-17893
```

- `9090` 是控制器：拿到 secret 就能改节点/读配置，**绝不能公网暴露**。
- `17890-17893` 是代理入口：公网开放等于开放代理，会被滥用。
- 如果 BLADE 和 Clash 在同一台 VPS：把 listener 的 `listen` 改成 `127.0.0.1`，
  控制器也绑 `127.0.0.1:9090`，BLADE 填 `http://127.0.0.1:...`。

---

## 4. BLADE 侧配置

进入：**系统设置 → 网络与代理**。

### 4.1 全局代理池

填写默认 listener：

```
http://<clash-host>:17890
```

它负责未绑定任务组时的出口、非任务工具和基础连通性；任务实际出口由下面的任务专属
listener 决定。

### 4.2 Clash 出口切换

| 字段 | 值 |
|---|---|
| 控制器地址 | `http://<clash-host>:9090` |
| secret | 与 Clash `secret` 一致（只写不回显） |
| 默认 / 回退 Selector 组 | `🧭 默认节点` |
| 任务出口组池 | `🌏 亚洲节点`、`🌍 欧洲节点`、`🌎 美洲节点`（每行一个） |
| 任务专属出口 listener | 见下方 |
| 自动切换 | 开启 |
| 切换冷却 | 60–300 秒（建议 120） |

任务 listener 格式为 `组名=代理地址`：

```text
🧭 默认节点=http://<clash-host>:17890
🌏 亚洲节点=http://<clash-host>:17891
🌍 欧洲节点=http://<clash-host>:17892
🌎 美洲节点=http://<clash-host>:17893
```

**组池只放区域组，不要放默认组。** 默认组只用于手动切换和组耗尽回退；如果把默认组
写进组池，任务可能被分配到“全部节点”，失去按区域限制的意义。

### 4.3 独占调度与排队

- **一个任务同一时刻只占一个组**；任务内的 planner/worker/主 Agent 共用这个组。
- **只有同时配置了专属 listener 的组才是可用组**。只填组名不填 listener 的组仅用于
  节点轮换，不参与独占调度；`TaskEgressCapacity` 也只统计可用组。
- **可用组数就是独占运行的任务上限**。即使“任务并发上限”开关关闭，这个上限仍然生效。
  例：3 个可用组时最多 3 个任务同时跑，第 4、5 个任务进入 FIFO 队列。
- **可用性按配置判定**：组名存在且对应 listener 地址非空、非黑洞即计入可用组；目前不做
  listener 主动连通性探测。某个 listener 临时故障时，该任务会报出口错误，不会自动换到
  其它任务的组。
- **暂停即释放**。任务暂停、进入排队、进入终态或删除时都会清空 `tasks.egress_group`
  并让出名额；空位出现后 `reconcileConcurrency` 按 FIFO 自动补位。
- **重启恢复绑定**。运行中任务的组落在 `tasks.egress_group`，重启后先恢复租约；
  仍超额的运行任务会在启动时转为排队，而不是退回全局共享出口。
- **流量捕获开启时不可用**：记录代理是进程级 MITM，必须看到所有请求，此时所有任务走
  全局代理，节点池不参与准入排队。
- 默认组回退仍然保留：某个区域组耗尽后任务可固定到默认组（默认组不在组池时，
  不占节点池名额）。

---

## 5. 轮换与回退规则

### 5.1 任务内节点轮换

以亚洲组为例：

1. 当前是香港节点；
2. 最多再试 3 个香港节点；
3. 香港 3 个都不行 → 台湾，最多 3 个；
4. 台湾不行 → 日本 → 新加坡 → 韩国 → 菲律宾 → 印度 → 马来西亚 → 泰国；
5. 每个地区最多 3 个候选，避免目标把某一个地区整片封禁后还继续在同一个地区消耗。

### 5.2 回退默认组

- 亚洲组所有地区都失败后，任务自动回退 `🧭 默认节点`；
- 回退时排除已经试过的地区，优先从默认组里选择未试过的地区（例如亚洲失败后优先欧洲/美洲）；
- 回退成功后，该任务固定使用默认组和默认 listener `17890`；
- 回退失败则返回错误，任务保持原出口，不会直连。

### 5.3 手动与自动

- `rotate_egress` 工具只切当前任务的组；
- WAF 自动检测按任务累计命中，不跨任务；
- 设置页「立即切换出口」切默认组；
- 同一任务/同一组同时只允许一个切换，冷却期内其它切换请求会被拒绝。

> 同一任务的多个 worker 共享一个组和出口 IP。一个 worker 触发切换，会影响同任务
> 其它 worker 的新连接；这是任务级隔离的预期行为。

---

## 6. 验证步骤

### 6.1 控制器可达

```bash
curl -H "Authorization: Bearer <secret>" http://<clash-host>:9090/version
curl -H "Authorization: Bearer <secret>" http://<clash-host>:9090/proxies \
  | jq '.proxies | to_entries[] | select(.value.type=="Selector") | {name:.key, now:.value.now, count:(.value.all|length)}'
```

应能看到四个组：

- `🧭 默认节点`
- `🌏 亚洲节点`
- `🌍 欧洲节点`
- `🌎 美洲节点`

### 6.2 listener 出口 IP

```bash
for p in 17890 17891 17892 17893; do
  printf '%s ' "$p"
  curl -s -x "http://<clash-host>:$p" https://api.ipify.org
  echo
done
```

不同区域端口应至少能看到不同的出口 IP；如果相同，说明 listener 绑定或节点选择有问题。

### 6.3 验证 listener 绑定

```bash
(curl -s -m 8 -x http://<clash-host>:17891 -o /dev/null \
  'https://speed.cloudflare.com/__down?bytes=50000000' &)
sleep 2
curl -H "Authorization: Bearer <secret>" http://<clash-host>:9090/connections \
  | jq '.connections[].chains' | head
```

应看到类似：

```json
["🇭🇰 HK-01", "🌏 亚洲节点"]
```

如果看到 `["DIRECT"]`，说明 listener 没绑对，**先修这个再谈切换**。

### 6.4 手动 API（可选）

```bash
TOKEN="<BLADE 登录 JWT>"
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"reason":"手动验证"}' \
  http://127.0.0.1:9898/api/settings/proxy/egress/rotate
```

---

## 7. 故障排查

| 现象 | 原因 / 处理 |
|---|---|
| `连接 Clash 控制器失败：context deadline exceeded` | 控制器未监听/防火墙拦截/地址端口错误。确认 `external-controller` 与 `curl /version`。 |
| `HTTP 401` | secret 为空或不一致。设置页重新填写。 |
| `Clash 代理组 "xxx" 不存在` | 组名拼错；先用 `/proxies` 查 Selector 名称。 |
| `类型是 URLTest，不是 Selector` | BLADE 只能手动切 Selector；把组改成 `type: select`。 |
| `代理组没有可用的非 DIRECT 节点` | 组里只有 DIRECT/REJECT 或只有嵌套组；把真实叶子节点加入区域组。 |
| `出口代理未配置或已阻断` | 当前组没有专属 listener，或全局代理池不可用；检查 `clash_task_endpoints` 与代理池。 |
| `出口刚切换过，请 N 秒后再试` | 冷却未到；等冷却或调小冷却时间。 |
| `节点 x 的出口 IP 仍为 y（未变化）` | listener 没绑该组、目标走的是别的入口，或节点共享同一出口 IP；用 `/connections` 看 `chains`。 |
| 切换成功但目标仍被封 | WAF 封的可能不是 IP，而是账号/ASN/指纹/payload；模型应换方向，不要重放同一 payload。 |
| 某个区域内所有节点都失败 | BLADE 会继续下一个地区；全组失败后回退默认组，并跳过已试地区。 |
| 默认组回退也失败 | 检查默认 listener 17890 和默认组节点；不会直连，任务会报错。 |
| 同一任务多个 worker 出口突然变化 | 同任务共享出口组；某个 worker 触发切换会影响同任务其它 worker。 |
| 任务一直“已排队：节点池出口组均已占用” | 可用组数（同时配置了组名与 listener 的组）少于运行任务数；暂停/结束一个任务会自动补位，或增加独立 listener。 |
| 配置了组但没有排队、仍在共用出口 | 该组没有对应 listener，只参与轮换；补上 `组名=代理地址` 后才是独占组。开启流量捕获时也会禁用按任务隔离。 |
| 切完出口 IP 与本机公网 IP 相同 | 说明流量没走代理；立即停手，检查 listener 的 `proxy:` 与任务 listener 映射。 |

---

## 8. 上线检查清单

- [ ] Clash `external-controller` 已开，secret 为强随机值。
- [ ] 9090 只对 BLADE 主机开放，未暴露公网。
- [ ] `🧭 默认节点` 包含全部实际节点，不含 DIRECT/REJECT/信息占位节点。
- [ ] 亚洲/欧洲/美洲三个区域组各自只含本区域叶子节点。
- [ ] 17890–17893 四个 listener 存在，`proxy:` 分别绑定四个组。
- [ ] BLADE 全局代理池指向默认 listener `17890`。
- [ ] BLADE 默认组 = `🧭 默认节点`，任务组池只列三个区域组。
- [ ] `clash_task_endpoints` 四个组都填了对应 listener 地址。
- [ ] 可用组数 ≥ 计划同时运行的任务数；否则超额任务会进入排队（这是预期行为）。
- [ ] 每个 listener 的出口 IP 不同，且 `/connections` 能看到对应组名。
- [ ] 手动切换一次，出口 IP 变化，且只影响当前任务/默认组。
- [ ] 自动切换已开，冷却 60–300 秒。
- [ ] 知道真实 IP 泄漏的停止条件：发现出口 IP 等于本机公网 IP 时立即停手排查。
