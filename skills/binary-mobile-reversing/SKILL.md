---
name: binary-mobile-reversing
description: 目标有 APP(APK/XAPK/APKM/APKS/AAB/IPA)、桌面客户端或需要逆向二进制找接口/密钥/漏洞时调用——静态解包、Jadx/apktool/radare2、移动端常见漏洞（硬编码密钥、导出组件、证书校验绕过、WebView、Flutter/Hermes），以及从逆向结果回到服务端测试与 SRC 闭环的路径。
tags:
  cwe: [CWE-798, CWE-312, CWE-926, CWE-295]
  attack: [T1409, T1552.001]
  class: [mobile, reverse, apk, ipa, binary]
---

# 移动端 / 二进制逆向速查

## 与 BLADE 移动工具链配合

- 当任务包含 APP 样本或绑定了测试机时：
  1. 先调用 `mobile_decompile_audit`（自动防 Zip Slip 安全解包、`full/partial_packed/raw_only` 质量门控、产出端点/密钥/签名算法候选列表于 `tasks/<id>/mobile_audit/`），再根据需要展开深度逆向技能 `apk-reverse`。
  2. 提交发现前用 `mobile_evidence_check` 校验包名/SHA-256 溯源、源码定位、HTTP 证据与凭据脱敏。

## APK / XAPK / IPA 静态分析

1. `mobile_decompile_audit` 一键产出解包与审计摘要（或手动 `apktool d app.apk -o out/`、`jadx -d src/ app.apk`）；
2. 找秘密（全文可写 credential 节点，报告中严格掩码）：
   ```bash
   grep -rnE "https?://|api[_-]?key|secret|token|password|AKID|AKIA|LTAI|accessKey" src/ | head -50
   ```
3. 抓接口与签名：`okhttp`/`retrofit` 注解、`/api/` 常量、加密与签名构造方法；Flutter 关注 `libflutter.so`/`libapp.so`，RN 关注 `index.android.bundle`/Hermes；
4. 导出组件：查 `AndroidManifest.xml` 中 `exported="true"` 或带 `<intent-filter>` 的 Activity/Service/Receiver/Provider，绑定设备后优先用 `mobile_ipc_probe` 探测。

## IPA / iOS / 桌面端

- **IPA**：解包后检查 `Payload/*.app/Info.plist`（ATS 与 URL Scheme）、`embedded.mobileprovision` 及主 Mach-O 的 `FairPlayEncrypted (cryptid)` / PIE / `strings`；
- **iOS 真机（`libimobiledevice`）**：`idevice_id -l`、`ideviceinfo -u <udid>`、`iproxy 27042 27042 -u <udid>` 配合 `mobile_frida_hook`（`ios_ssl_bypass` / `ios_jailbreak_bypass` / `ios_crypto_monitor` / `ios_keychain_dump`）；
- **Electron**：`app.asar` 解包（`npx asar extract`）→ 找 `preload`、`nodeIntegration`、`contextIsolation` 与 IPC 暴露面；
- **原生二进制（.so / ELF / PE / Mach-O）**：检查 NX/PIE/RELRO/Canary，`strings -n8` 找 URL/密钥，`readelf`/`radare2`/`ghidra` 逆向核心函数，并用 `lookup_cves` 对照开源库版本号。

## 移动端常见漏洞验证

| 漏洞 | 验证方式 |
|---|---|
| 硬编码密钥 | 从包内提取 AK/SK/token → 直接调 API 或云接口验证只读权限（不能利用的纯第三方统计 Key 不报高危） |
| 未授权 / A/B 越权 API | 逆向得到接口，或使用工作台「提取实时会话」标注 `user_a` / `user_b` 对同一资源重放对照 |
| 导出组件 / IPC | 绑定设备用 `mobile_ipc_probe(auto_fuzz)` 或 `adb shell am start -n pkg/.Activity --es key value` |
| 无视代理 / SSL Pinning | 使用 `mobile_proxy_ctrl(enable_transparent)` + `mobile_frida_hook`（`ssl_pinning_bypass` / `flutter_ssl_bypass` / `native_tls_hook`）拿到流量后攻打服务端 |
| WebView / DeepLink | 参数进入 `loadUrl`/`addJavascriptInterface`/`openFile` → 构造 Scheme URL 或 HTML 验证越权读取/调用 |
| 本地存储 / 日志 | `mobile_app_ctrl` 执行 `sandbox_audit` / `logcat_audit`，配合 `mobile_ui_explore(screenshot)` 留存现场 |

## 从客户端回到服务端

- 逆向得到的接口、参数与签名算法是**测试服务端的钥匙**：用脚本或 `http_request(mobile_sign_method=...)` 自动重签并批量打接口做越权与注入验证；
- 客户端校验（签名/滑块/加密/Root）服务端往往未强制校验——重点验证服务端边界；
- 最终结合 `api-security-testing` 完成完整业务 API 深度覆盖。
