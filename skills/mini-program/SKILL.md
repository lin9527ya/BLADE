---
name: mini-program
description: 微信小程序渗透：wxapkg 安全解密与解包、接口/密钥/云开发提取、登录态与越权、云开发、支付与开放能力、证据与合规报告
license: MIT
when_to_use: 用户上传 .wxapkg / 含 wxapkg 的 .zip，或在任务中提到微信小程序、AppID、小程序码，要求安全测试、代码审计、接口挖掘或越权验证
tags:
  cwe: [CWE-306, CWE-639, CWE-798, CWE-862]
  attack: [T1190, T1552]
  class: [mobile, api, cloud, miniapp]
---

# 微信小程序渗透规范

小程序漏洞的主体是 **API 鉴权缺失、水平/垂直越权、业务逻辑、前端信任、云开发配置错误、硬编码密钥**，不是传统 Web 注入。核心思路：**包是入口，后端与云开发才是主战场**。

## 0. 输入判定与路径

- 安装包与样本通常位于 `tasks/<id>/samples/`（新建任务 / 移动端工作台上传）或 `tasks/<id>/uploads/` / `sessions/<id>/uploads/`（对话附件上传）。
- 支持单个 `.wxapkg`、多包（主包 + 分包 + 插件包）、含 `.wxapkg` 的 `.zip`、或已解包源码目录。
- AppID 提取规则：`wx[0-9a-f]{16}`；**绝不能把个人微信号 `wxid_...` 当作 AppID**（V1MMWX 解密填错会直接出乱码）。
- PC 微信 3.x/4.x 本地包通常带 `V1MMWX` 外壳；Android/iOS 端缓存多为 `0xBE` 明文包。

## 1. 安全解密与质量解包（二选一或联动）

1. **平台原生入口（推荐优先）**：直接调用 `mobile_decompile_audit` —— 自动校验防路径穿越、V1MMWX 解密并提取明文包至 `tasks/<id>/samples/decompiled/raw/`，输出目录级 `mobile_audit/{manifest.json,report.json,endpoints.txt,secrets.txt}`。
2. **脚本质量阶梯（支持 wedecode/unveilr WXML 还原时使用）**：

```bash
bash "${SKILL_DIR}/scripts/miniapp_unpack.sh" \
  --in "<.wxapkg 或 .zip 或目录的绝对路径>" \
  --out "<taskRoot>/miniapp" \
  --appid "<wx...>"          # 可省略，脚本会从路径推断
```

产出目录（`<taskRoot>/miniapp/<appid>/`）：

| 路径 | 内容 |
|---|---|
| `raw/` | 纯 Python `wxapkg_safe.py` 提取的原始文件（防 `../` 路径穿越；即使反编译工具失败仍可审计 JS/JSON） |
| `decrypted/` | V1MMWX 解密后的明文 `.wxapkg` |
| `src_wedecode/` | `wedecode` 质量解包（若已安装；WXML `{{}}` 绑定保留完整） |
| `src_unveilr/` | `unveilr` 质量解包（若已配置 `UNVEILR_DIR`） |
| `src_killwxapkg/` | `KillWxapkg` 兜底（专用子目录，防写入污染） |
| `source` | `quality_gate.py` 选出的最高分产物软链 |
| `manifest.json` | AppID、包 SHA-256、工具与质量分（`ok` / `partial` / `failed`） |

**注意**：不可信外部反编译器返回码 `0`，务必检查 `manifest.json` 中的 `quality.status`；遇到 `__wxCodeSpace__` / `batchAddCompiledTemplate` 标注为 `partial` 后优先通过抓包动态补充端点。

## 2. 离线目录静态审计（`miniapp_audit`）

对解包所得源码目录直接调用：

```text
miniapp_audit(dir="../miniapp/<appid>/source", appid="<wx...>")
# 或对 mobile_decompile_audit 产物目录：
miniapp_audit(dir="../samples/decompiled/raw", appid="<wx...>")
```

输出到 `runDir/miniapp-audit/`：
- `report.json`：`routes` / `endpoints` / `secrets` / `cloud` / `sign_candidates` / `summary`
- `endpoints.txt`：每行 `METHOD URL  # file:line`
- `secrets.txt`：前 4 后 4 脱敏值 + 类型 + `file:line` + 上下文

审计核查要点：
1. **接口与域名**：`wx.request` / `wx.uploadFile` / `wx.downloadFile` / `wx.connectSocket`、`wx.cloud.callFunction`、`collection`、`<web-view src>`。
2. **硬编码密钥**：AppSecret（32 位 hex）、云 AK/SK、腾讯地图/高德 Key、JWT、AES key/iv、私钥。
3. **云开发资产**：`env` ID、云函数名、数据库集合、云存储 fileID。
4. **签名与加密逻辑**：`sign` / `hmac` / `md5` / `sha` / `aes` / `CryptoJS` / `wx.requestPayment` 调用点。
5. **路由与高价值页面**：核对 `app.json` / `app-config.json` 中 `pages`、`subPackages`、`plugins`，重点跟踪登录、支付、订单、地址、会员、文件上传页面。

## 3. 与 BLADE 平台的衔接

| 小程序发现 | BLADE 平台工具与动作 |
|---|---|
| AppID / 小程序主体 | `insert_assets(type="app", bundle_id=<AppID>, app_name=<名称>)` |
| API 接口与业务域名 | `insert_assets(type="endpoint")` 或 `record_fact` 登记来源 `file:line` |
| 硬编码凭据 / 密钥 | `record_credential` / `record_fact` 验证有效性（报告中严格前 4 后 4 脱敏） |
| 云开发环境 / 函数 / 集合 | `record_fact` 落盘，并按 `references/04-cloudbase-security.md` 验证访问规则 |
| 抓包请求与响应 | `traffic_search` / `traffic_get` / `traffic_blob`，并通过 `bind_finding_traffic` / `traffic_refs` 绑定发现 |
| A/B 双身份越权测试 | 工作台「提取实时会话」(`account_label`: `user_a`/`user_b`) 或 `session_login` → `http_request` → `diff_responses` |
| 提交漏洞前自检 | **必须调用** `mobile_evidence_check`（校验 AppID、SHA-256、源码锚点、HTTP 请求响应与脱敏）及 `verify_evidence`，再调 `report_finding` |

平台既有高频小程序打法条目（配合 `src-hunting` 深化）：
- `skills/src-hunting/references/authbypass-test.md`（小程序码/跳转码报错泄露 `access_token`、`loginByOpenId` 空参越权）
- `skills/src-hunting/references/idor-test.md`（云函数 Container Registry `_catalog`、前端打包写死 token 批量越权）
- `skills/src-hunting/references/打穿短表.md`（小程序云 Registry、客服链接参数拼接、空 openId 进已有号）

## 4. 动态验证

### 4.1 抓包与代理（PC 微信 / 模拟器 / 真机）
PC 微信 3.4.5+ 小程序容器 `WeChatAppEx.exe` / WMPF **不读取系统代理**，需指导操作者按进程转发流量到平台 MITM 地址并信任 CA：
- Windows/macOS Proxifier 进程规则：`WeChat.exe;WeChatAppEx.exe;WeChatPlayer.exe;WechatBrowser.exe;WeChatAppEx*.exe`（勾选解析目标域名）；
- Android：Root 设备可通过工作台「透明分流 (iptables+QUIC)」一键接管，或使用系统 CA (`push_ca`) + `mobile_proxy_ctrl`；
- iOS：Wi-Fi 指向平台 LAN 代理 + 安装并完全信任平台根证书；
- 无法抓包时按 `references/02-traffic-interception.md` 排查 WMPF 网络栈、QUIC、Pinning 与证书问题。

### 4.2 登录态与 10 态越权矩阵
- 对提取到的 `Authorization` / `token` / `openId` / 自定义签名字头，建立至少两个测试账号（`user_a`、`user_b`），核查无令牌、空令牌、A→B 水平越权、普通→管理垂直越权及签名防重放校验（详见 `references/03-auth-and-session.md`）。

### 4.3 云开发 / 支付 / 开放能力
- **云开发**：测试公开读写数据库规则、`doc.get`/`aggregate`、`callFunction` 隐式信任前端传入 `openid`、云存储路径遍历（`references/04-cloudbase-security.md`）。
- **支付与开放能力**：核查服务端算价与回调验签、小程序码 `scene`/`page` 越界、URL Link/Scheme 传参注入、`web-view` 业务域白名单绕过与 `postMessage` 来源检查（`references/05-open-capability-abuse.md`、`references/07-webview-plugin-worker.md`）。

## 5. 参考文件（按需打开，禁止通读）

| 文件 | 何时看 |
|---|---|
| `references/00-overview.md` | 首次评估小程序，查阅全流程 10 阶段与分工 |
| `references/01-recon-and-unpack.md` | PC/Android/iOS 缓存取包路径、V1MMWX 解密与解包失败排查 |
| `references/02-traffic-interception.md` | 抓不到包 / 证书 / WMPF 网络栈排查 |
| `references/03-auth-and-session.md` | `code2Session`、`session_key`、手机号新旧流程与 10 态越权矩阵 |
| `references/04-cloudbase-security.md` | CloudBase 云函数、数据库规则与云存储测试 |
| `references/05-open-capability-abuse.md` | 小程序码、URL Link、订阅/客服消息、支付回调安全 |
| `references/06-vuln-catalog.md` | 43 类小程序漏洞触发条件、步骤、证据口径与常见误报 |
| `references/07-webview-plugin-worker.md` | `web-view`、插件供应链、Worker、跨小程序跳转与 CPRF |
| `references/08-casebook.md` | 常见绕过案例与突破思路参考 |
| `references/09-checklists.md` | 信息收集 / 接口 / 云开发 / 支付 / 隐私五张核查清单 |
| `references/10-automation.md` | AST/污点、批量对比与签名重算自动化模式 |
| `references/11-reporting.md` | 移动端报告写法与 SRC 常见驳回原因 |
| `references/12-sources.md` | 官方安全文档与工具来源索引 |

## 6. 完成自检

- [ ] AppID（非 `wxid_`）、包 SHA-256、解包工具及质量状态已写入 `manifest.json` / `record_fact`
- [ ] 页面路由、接口清单与 CloudBase 资产已沉淀，端点带精确 `file:line`
- [ ] 硬编码凭据验证有效才上报，正文仅含脱敏前后缀
- [ ] 越权测试至少覆盖无 token、空 token、A/B 账号对比
- [ ] 每条漏洞已通过 `mobile_evidence_check`（`src_ready: true`）且附带流量/HTTP 复现闭环
