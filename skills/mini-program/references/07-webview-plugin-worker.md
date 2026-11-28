# 07 · web-view / 插件 / Worker / 跨小程序跳转

## web-view

- 只能加载后台配置的业务域名；HTTPS、不能 IP/localhost。
- 风险：
  - `src` 由用户输入/接口返回控制 → 开放重定向/钓鱼；即使初始域名合法，H5 内部 302 到外部域也可能被微信拦截或放行，需实测。
  - H5 自身 XSS → 可调用 JSSDK（`wx.miniProgram.navigateTo`、`postMessage`、`getEnv`），影响小程序页面跳转/数据传递。
  - `postMessage` 数据未校验 → 小程序把 H5 数据当可信输入（`token`、`userId`、`redirect`）。
  - Cookie/localStorage 共享范围、退出清理。
- 测试：用自己的 H5/测试域名；构造 `src` 参数；在 H5 中 `console.log` 证明执行；检查 `bindmessage` 处理逻辑。

## 插件

- 插件是第三方小程序，框架保证数据隔离，除非主动传递。
- 风险：插件提供方被攻破/恶意更新；插件页面收集用户信息；插件调用 API 权限过大；插件版本未锁定；插件功能页（支付/收货地址/发票）参数篡改。
- 测试：梳理 `plugin.json`、`requirePlugin`、插件 AppID/版本；检查插件页面入参；检查插件是否回传敏感数据；查插件 CVE/公告。

## Worker

- `wx.createWorker` 独立线程，不能操作 UI；仍在小程序沙箱内；可 `postMessage`。
- 风险主要是 CPU/内存滥用、密钥/算法放在 worker 中（仍可被逆向）、worker 脚本被篡改（需重打包）。
- 不是常见漏洞点；测试以稳定性/资源为主。

## 跨小程序跳转

- `wx.navigateToMiniProgram({appId, path, extraData, envVersion})`；`wx.openEmbeddedMiniProgram`；`wx.navigateBackMiniProgram`。
- 风险：`appId/path` 由用户控制 → 钓鱼/跳转到测试版；目标页信任 `extraData` → 参数注入/越权；`envVersion: 'develop'/'trial'` 打开未发布版本。
- 测试：改 `appId` 为自建测试小程序；改 `path`；注入 `extraData`；观察目标页行为。

## CPRF（跨页面/跨小程序请求伪造）

- 原理：页面 `onLoad(options)` 接收参数，结合本地已登录 token/openid 自动发请求；攻击者构造分享路径/小程序码，让受害者在已登录状态下打开，完成一次请求。
- 测试：
  1. 用自己两个账号；账号 A 正常登录。
  2. 构造 `path?action=...&id=...` 的分享路径/小程序码。
  3. 账号 B 打开后检查账号 A 的状态是否变化（只做只读/可回滚操作）。
  4. 证据：受害者账号状态变化 + 请求日志；说明利用条件（需打开链接）。
- 误报：需要用户确认；无状态变更；token 不自动携带。

## 页面路由参数审计

- 从 `app-config.json`/`app.json` 提取全部页面。
- 搜索 `onLoad(options)`、`options.xxx`、`decodeURIComponent`、`navigateTo`、`redirectTo`、`switchTab`。
- 看参数是否直接进入：
  - `wx.request` 的 URL/data/header
  - `web-view src`
  - `wx.navigateToMiniProgram` 的 appId/path
  - `wx.setStorage`/`eval`
- 高价值参数：`redirect`、`url`、`path`、`id`、`userId`、`openid`、`token`、`action`、`type`。
