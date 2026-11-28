# 跨端框架与混合容器安全审计参考

> 覆盖 Flutter、React Native / Hermes、UniApp / Cordova / Electron-like H5 混合容器及 WebView JSBridge 的逆向、抓包绕过与漏洞验证要点。

---

## 1. Flutter 应用专项

### 特征识别
- `lib/<abi>/libflutter.so` 与 `lib/<abi>/libapp.so`
- `assets/flutter_assets/`（含 `AssetManifest.json`、`FontManifest.json`、配置 JSON/YAML）
- 提示：BLADE `mobile_decompile_audit` / `static_audit` 会自动标记框架 `Flutter`。

### 抓包痛点与解决路径
1. Flutter Dart `HttpClient` **忽略 Android/iOS 系统代理设置**，仅设系统 Wi-Fi 代理抓不到包。
2. Flutter 引擎内置独立 BoringSSL，不信任用户安装的系统级 CA 证书。
3. **BLADE 标准打通步骤**：
   - 第一步：`mobile_proxy_ctrl(action="enable_transparent")` 开启 Root `iptables` 透明重定向并自动阻断 UDP 443 迫使 HTTP/3/QUIC 降级至 TCP 443。
   - 第二步：`mobile_frida_hook(action="start", mode="spawn", modules=["flutter_ssl_bypass"])` 在 BoringSSL/x509 校验分支注入旁路。
   - 若 `libflutter.so` 被严重裁剪或偏移变化：`frida_hook` 失效时改用宿主机 BLUTTER / reFlutter 解析 `libapp.so` 函数名与硬编码端点字符串。

### 审计重点
- 检查 `assets/flutter_assets/` 下是否打包了测试环境 `env.json`、`config.json`、管理后台地址、OSS/COS STS 参数或内测开关。
- 用 `strings -n8 libapp.so` 提取 Dart 快照中的明文 URL 路径、GraphQL query 与秘钥前缀。

---

## 2. React Native 与 Hermes 字节码专项

### 特征识别
- Android：`assets/index.android.bundle`、或 `lib/<abi>/libhermes.so`（魔数 `\xc6\x1f\xbc\x03c\x1f\xb6\x03` / `HBC` 头）；iOS：`main.jsbundle` / `hermes.framework`。
- 若 bundle 为纯 JS 文本：可直接用 `prettier` 或正则表达式搜索 `baseURL`、`fetch(`、`axios`、路由表与硬编码 Secret。
- 若已编译为 **Hermes HBC 字节码**：
  - BLADE 内置 `ExtractHermesStrings`（在 `static_audit` 与 `mobile_decompile_audit` 中自动运行）会恢复明文域名与配置键值对。
  - 需要还原完整控制流时，可使用开源 Hermes 反编译器（`hermes-dec` / `hbctool`）。

### Native Module / TurboModule 桥接风险
- 搜索原生层 `@ReactMethod`（Java/Kotlin）或 `RCT_EXPORT_METHOD`（iOS ObjC/Swift）：若混合加载不可信 H5/Deep Link 参数并转调原生文件读取、WebView 跳转或签名接口，可能引发任意文件读取或静默接口签名调用。

---

## 3. H5 混合容器与 WebView 安全（UniApp / Cordova / 自定义 WebView）

### Android WebView 高危组合检查
在 `mobile_audit/report.json` 与 `decompiled/jadx/` 中重点审计以下组合同时出现的情况：

1. **JSBridge 越权暴露**：
   - `addJavascriptInterface(obj, "bridgeName")` + `@JavascriptInterface` 导出方法未校验 `webView.getUrl()` 域名白名单。
   - 一旦配合任意 URL 打开（Deep Link `url=` 参数未校验 `host` / 开放重定向），第三方网页即可调用 APP 原生接口窃取 Token、读取通讯录/位置、或触发敏感操作。
2. **本地文件域穿越 / UXSS**：
   - `settings.setAllowFileAccess(true)`
   - `settings.setAllowFileAccessFromFileURLs(true)` 或 `setAllowUniversalAccessFromFileURLs(true)`
   - 攻击链：通过导出 Activity 或 `intent://` scheme 传入 `file:///data/data/<pkg>/...` 或受控下载目录中的 HTML，利用同源绕过外泄私有沙箱 SQLite / SharedPreferences 数据。
3. **SSL 错误静默放行**：
   - 自定义 `WebViewClient.onReceivedSslError` 中无条件调用 `handler.proceed()`，导致中间人可直接篡改 WebView 加载的认证页与 JSBridge 脚本。
4. **Scheme 协议混淆**：
   - `shouldOverrideUrlLoading` 直接执行 `Intent.parseUri(url, Intent.URI_INTENT_SCHEME)` 且未清空 `selector` 与 `component`（可调起未导出内部 Activity / FileProvider）。

### iOS WKWebView 对照关注点
- `WKUserContentController.add(_:name:)` 注册 JS Message Handler 是否在回调里严格校验 `message.frameInfo.securityOrigin.host`；
- `WKPreferences.setValue(true, forKey: "allowFileAccessFromFileURLs")`（私有 KVC）是否被错误开启；
- URL Scheme / Universal Links 回调传参进入 `WKWebView.load(URLRequest)` 时是否校验协议与可信根域（禁止直接放行 `file://` 或任意第三方 `https://`）。

---

## 4. 验证与定性原则

- **严禁把"使用了 Flutter/RN"当成漏洞**。
- 对 WebView / Deep Link 问题，务必至少在测试机完成以下两项之一才上报中高危：
  1. 用 `mobile_ipc_probe(kind="deeplink")` / `adb shell am start` 传入测试页 URL 成功拉起 WebView 并执行可控 JS / 回连验证域名；
  2. 给出精确的 `AndroidManifest.xml` 导出组件配置 + `jadx` 无白名单校验源码 + 实际可触发的参数 Payload。
