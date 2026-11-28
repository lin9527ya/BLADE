# 02 · 抓包方案与排障

## 方案对比

| 方案 | 适用 | 优点 | 限制 |
|---|---|---|---|
| PC 微信 + Proxifier + Burp/Yakit | Windows/macOS 桌面小程序 | 最稳；可抓小程序+内置浏览器；不依赖 root | 需按进程转发；微信更新可能改进程名 |
| PC 微信 + Fiddler + Burp 二级代理 | Proxifier 不可用 | Fiddler 系统代理，上游 Burp | 需装 Fiddler 根证书；重启微信 |
| 安卓模拟器 + Burp/Charles | 需 root/系统证书/旧版微信 | 可装系统证书；可改包/重打包 | 安卓 7+ 用户证书不信任；新版微信只信任自带证书列表 |
| 安卓真机 + Postern/SOCKS | 真机行为 | 接近真实环境 | 需 root 或 VPN 模式；证书问题 |
| iOS + Burp/Charles | iOS 小程序 | 系统代理通常生效；证书简单 | iOS 15+ 需手动“证书信任设置”；部分 pinning |
| 透明代理/Clash TUN/mitmproxy | 不走系统代理 | 不依赖应用代理设置 | 需网关/DNS/证书；排障复杂 |
| r0capture/Frida/objection | SSL pinning/双向认证 | Java 层抓明文；绕过 pinning | 需 root/越狱；有封号风险 |
| CDP/WMPF 调试 | 需 F12/看路由/改 JS | 动态调试、看 storage、执行 JS | 版本敏感；封号风险 |

## PC 微信（推荐）

1. 平台开启录制 MITM，取得 MITM 地址与 CA。
2. Windows 安装 Proxifier：
   - 代理服务器指向平台 MITM 地址。
   - 规则进程：`WeChat.exe;WeChatAppEx.exe;WeChatPlayer.exe;WechatBrowser.exe;WeChatAppEx*.exe`。
   - 勾选 `Use target hostname in proxy request if available`（避免目标看到代理 IP 的 SNI 差异）。
3. 安装平台 CA 到“受信任的根证书颁发机构”，重启微信。
4. 基线验证：先浏览器访问一个 HTTPS 站点确认能抓到，再打开小程序。
5. 每次改规则/证书后重启微信。

## 安卓

- 优先 Android 5/6 模拟器（用户证书仍被信任）。
- 新版：root + Magisk 系统证书；或 Postern 全局 SOCKS；或 Frida/JustTrustMe/r0capture。
- Android 11+ 分区存储可能限制 `/sdcard/Android/data`，优先 `/data/data`。

## iOS

- Wi-Fi 代理指向 Burp；安装描述文件；到“关于本机 > 证书信任设置”开启完全信任。
- iOS 15+ 每装新证书都要重新开启。

## 抓不到包原因树（按顺序）

1. **PC 微信 3.4.5+ 小程序由 `WeChatAppEx.exe`/WMPF 承载，不读系统代理** → Proxifier/ProxyCap 按进程强制转发。
2. **安卓 7+ 不信任用户证书；微信只信任自带证书列表** → 系统证书/Android 5-6/Frida/r0capture。
3. 证书未安装/未信任/装错位置；Burp 监听 `127.0.0.1` 而非 `0.0.0.0`；防火墙；不同网段。
4. 代理软件冲突（Clash/Proxifier 循环检测）；DNS 污染；IPv6；QUIC/HTTP3。
5. 应用层 SSL pinning/双向认证 → Frida/objection/r0capture/客户端证书。
6. WebSocket/长连接、私有协议（微信 mmtls）不经过 Burp；通常不是业务接口。

## 请求样本导入

- 抓到请求后可直接用平台 `http_request` 重放；HAR 可用工具转换。
- 小程序的 `Referer`/`User-Agent` 可能与浏览器不同；重放时保持原头，避免触发风控。
- 登录态：从 storage/请求头取 token；用 `session_login` 建立会话，后续 `http_request(session=...)` 自动带 Cookie。
- 签名：见 `10-automation.md` 的签名还原流程。
