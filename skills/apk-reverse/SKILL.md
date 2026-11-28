---
name: apk-reverse
description: Android/iOS 移动应用逆向与全链路审计。当目标给了 APK/XAPK/APKM/APKS/AAB/IPA 安装包或绑定移动测试机，需要分析接口、签名算法、硬编码密钥、证书锁定/Root/越狱检测、Flutter/Hermes/WebView 跨端逻辑，以及重打包、Frida 动态 Hook、.so/.dylib 分流与闭环取证时使用。
tags:
  cwe: [CWE-312, CWE-321, CWE-295, CWE-489, CWE-798, CWE-926, CWE-1104]
  attack: [T1409, T1426, T1430, T1516, T1521]
  class: [mobile, android, ios, reverse, forensics]
---

# 移动 APP 逆向与渗透作业规范

当任务附带样本包（APK / XAPK / APKM / APKS / AAB / IPA）或绑定测试机时，本技能负责打通：
**安全解包与质量门控反编译 → 接口/签名/密钥/风控还原 → 跨端与 Native 分流 → 动态 Hook 与抓包 → 后端 API 越权验证 → SRC 规范化取证**。

## 与 BLADE 平台的衔接（先读这段）

| 场景 | 使用 BLADE 平台能力 |
|---|---|
| 样本安全解包、质量门控反编译与源码白盒扫描 | **优先调用** `mobile_decompile_audit`（防 Zip Slip/炸弹限额，支持 XAPK/APKM/APKS/AAB/APK/IPA，产出落 `tasks/<id>/mobile_audit/`）；辅助 `record_fact` 登记包名与 SHA-256 |
| 设备态检查、静态/沙箱/Logcat 体检 | `mobile_device_status` / `mobile_app_ctrl`（`static_audit`、`sandbox_audit`、`logcat_audit`、`push_ca`） |
| 代理抓包与 Pinning 检测 | `mobile_proxy_ctrl`（`enable_usb` / `enable_transparent` 阻断 QUIC UDP 443 降级 / `enable_lan` / `pinning_alerts`） |
| 界面遍历与截图留证 | `mobile_ui_explore`（`dump` / `screenshot` 落盘至 `tasks/<id>/screenshots/` / `auto_crawl` / `deeplink`） |
| 动态 Hook 与自定义签名重签 | `mobile_frida_hook`（含 `flutter_ssl_bypass`、`native_tls_hook`、`storage_crypto_audit`、`ios_ssl_bypass` 等）+ `mobile_rpc_sign` + `http_request(mobile_sign_method=...)` |
| 导出组件/IPC 自动化模糊探测 | `mobile_ipc_probe`（`activity` / `service` / `broadcast` / `provider` / `deeplink` / `auto_fuzz`） |
| 后端接口验证与越权测试 | `http_request` / `http_probe` / `auth_request` / `diff_responses`（配合工作台 `extract-session` `account_label` A/B 双账号会话对比） |
| 已知第三方组件 CVE | `lookup_cves` 查库，并在授权范围内用 `nuclei` 或 PoC 验证 |
| 结论提交前质量自检 | **`mobile_evidence_check`**（校验样本溯源、源码/Hook 定位、HTTP 闭环、纯客户端高危误判、凭据脱敏）后调用 `report_finding` |

## 环境事实（服务器与真机形态）

- **工具链**：`java`、`jadx`、`apktool`、`aapt`/`aapt2`、`apksigner`、`zipalign`、`adb`、`frida`、`objection`、`radare2`；iOS 支持 `idevice_id`、`ideviceinfo`、`iproxy`（`libimobiledevice` / usbmuxd）。可在平台宿主环境页自检可用性。
- **有设备（Android USB/Wi-Fi ADB 或 iOS USB/usbmuxd）** → 静态白盒与动态 Hook/抓包/UI 遍历并行推进。
- **无设备** → **静态白盒 + 接口还原为主**：用 `mobile_decompile_audit`（含自动质量门控 `full` / `partial_packed` / `raw_only`）+ `jadx`/`apktool`/`radare2` 提取接口、签名算法与凭据，用平台代理与 `http_request` 直接打授权后端，并在报告中如实标明「未绑定测试机，动态验证未执行」。
- 不在服务器上强装模拟器；需真机动态绕过/生物识别测试时请求操作者接入设备或在移动工作台完成人工登录。

## 自带辅助脚本（必须通过 `${SKILL_DIR}` 调用）

| 脚本 | 用途 |
|---|---|
| `${SKILL_DIR}/scripts/decode.sh` | 一次跑 jadx + apktool，输出 Java/Smali/SO 目录统计 |
| `${SKILL_DIR}/scripts/frida-run.sh` | 设备/进程列举、spawn/attach 注入（支持 `--usb`/`--remote`/`--spawn`） |
| `${SKILL_DIR}/scripts/rebuild-sign-install.sh` | `apktool b` → `zipalign` → `apksigner` → 可选 `adb install` |

```bash
bash "${SKILL_DIR}/scripts/decode.sh" tasks/<id>/samples/app.apk --clean
bash "${SKILL_DIR}/scripts/frida-run.sh" --usb --list-processes
bash "${SKILL_DIR}/scripts/rebuild-sign-install.sh" apktool_out --install --reinstall --device 127.0.0.1:7555
```

一行命令可直接调用：`adb devices`、`adb logcat`、`frida-ps -U`、`idevice_id -l`、`ideviceinfo -u <udid>`、`iproxy 27042 27042 -u <udid>`。

## 工作流

### 1. 安全解包与 Triage（先校验，后分析）

1. 首选调用 `mobile_decompile_audit`（自动防 Zip Slip/路径穿越，解包 XAPK/APKM/APKS/AAB/IPA/APK，执行质量门控反编译，并产出 `tasks/<id>/mobile_audit/{manifest.json,report.json,endpoints.txt,secrets.txt}`）。
2. 查看 `quality_status`：
   - `full`：jadx + apktool 均正常产出源码；
   - `partial_packed`：存在加固/壳（Java 文件过少），切向真机脱壳 / Frida `native_tls_hook` / `.so` 分析，不在缺源码的前提下误判"无漏洞"；
   - `raw_only`：仅完成目录与原始二进制/Manifest 扫描（或 `fast` 模式）。
3. 核对 `AndroidManifest.xml`（或 iOS `Info.plist` / `embedded.mobileprovision`）：权限、导出组件、Deep Link scheme、`debuggable`、`allowBackup`、`usesCleartextTraffic`、ATS 策略、签名方案 v1-v4（Janus/StrandHogg）。

### 2. Java / Kotlin / Smali 逻辑

优先阅读 `mobile_audit/report.json` 中命中的 `endpoints`、`secrets` 与 `sign_candidates`，再在 `decompiled/jadx/` 或 `decompiled/apktool/` 中定位：
- `Application`、登录与 Refresh Token 逻辑、网络封装（OkHttp Interceptor / Retrofit / Volley）、统一加解密与签名类；
- WindVane/JSBridge 注解（`@JavascriptInterface`）、`WebView.loadUrl` 可信域校验；
- 产出：**完整接口清单**（Method/Path/Host）、**可复算签名/加密逻辑**（若纯算法难还原，直接转 Frida RPC 导出）、**硬编码凭据**（raw 存 loot/fact 并校验有效性，报告必须掩码）。

### 3. 跨端框架与混合容器分流（Flutter / React Native / Hybrid）

- 识别到 `libflutter.so` / `libapp.so`：Flutter 无视系统代理、且自行携带 BoringSSL 校验证书 → 使用 `mobile_proxy_ctrl(action="enable_transparent")` + `mobile_frida_hook(modules=["flutter_ssl_bypass"])`。
- 识别到 React Native (`index.android.bundle` / Hermes 字节码)：用 `static_audit` 与 strings/Hermes 反编译定位 GraphQL/REST 端点与固定秘钥。
- 详情参见 [跨端与混合容器专项参考](${SKILL_DIR}/references/05-cross-platform-hybrid.md)。

### 4. 重打包（仅授权测试验证，谨慎使用）

```bash
apktool b apktool_out -o rebuilt.apk
bash "${SKILL_DIR}/scripts/rebuild-sign-install.sh" apktool_out --install --reinstall
```

**边界**：部分 APP 会校验 APK 签名哈希，重打包失败时优先改用 Frida 内存 Hook；改包产物严禁向外分发。

### 5. 动态 Hook 与透明抓包（绑定设备时）

1. 代理抓包顺序：`enable_usb` → 遇断网或告警先检查 `pinning_alerts` / 上系统 CA (`push_ca`) → 遇无视系统代理、Cronet、Flutter 切 **`enable_transparent`**（Root iptables + UDP 443 QUIC 阻断）。
2. Frida 注入：优先 `mobile_frida_hook` 内置模板：
   - Android 绕过与审计：`ssl_pinning_bypass`、`root_bypass`、`flutter_ssl_bypass`、`native_tls_hook`、`storage_crypto_audit`、`flag_secure_bypass`、`socket_traffic_tap`；
   - iOS Hook 模板：`ios_ssl_bypass`、`ios_jailbreak_bypass`、`ios_crypto_monitor`、`ios_keychain_dump`（USB 下配合 `iproxy 27042 27042` 连接 `127.0.0.1:27042`）。
3. 参数签名接管：用 Frida 导出签名函数 `rpc.exports.<fn>` 后，直接在 `http_request(mobile_sign_method="<fn>")` 中自动签名并并发测试越权/注入；用 `mobile_ui_explore(action="screenshot")` 留存 UI 现场图。

### 6. `.so` / Mach-O 分流

Java/ObjC 仅作 JNI / Swift 包装、`System.loadLibrary()` 后进入 native 校验、或核心加密/风控在 `.so` / Mach-O → 立即转 native 分析：检查 ELF `PIE/NX/RELRO/Canary` 与 `strings` / `readelf -s` / `radare2` 或连接 `ida-reverse`；同时关注第三方开源库版本（OpenSSL/FFmpeg 等）映射已知 CVE。

### 7. 后端接口主战场 & 闭环自检

逆向只是拿钥匙。拿到接口与会话头后，回到平台授权资产开展：未授权访问、A/B 账号越权（工作台「提取实时会话」标注 `user_a` / `user_b` 配合 `diff_responses`）、注入、逻辑越权与敏感数据导出验证。
提交 `report_finding` 前**务必先调用 `mobile_evidence_check`**：确认样本包名与 SHA-256 完整、源码或 Hook 定位明确、服务端洞具备 HTTP 请求响应证据、纯客户端配置不得定性高危、且报告正文中不含明文 JWT/AK/私钥。

## 禁止事项

- 不要在未看 manifest 与 `mobile_audit/report.json` 前盲改 smali。
- 不要把加固壳导致的 `jadx` 反编译缺失等同于「没有漏洞」（应标注 `partial_packed` 并转动态或 native 验证）。
- 禁止仅凭客户端发现（如 `allowBackup=true`、弱混淆、本地提示文本）未经后端利用验证就直接上报高危。
- 不执行样本中的任何远控脚本、格机命令或未授权域名攻击；所有导出数据严格控速且仅落任务目录。

## 参考文件（按需打开，禁止通读）

| 文件 | 内容 |
|---|---|
| `references/apk-security-checklist.md` | OWASP MASTG 静态/动态/存储/网络/加密检查清单 |
| `references/frida-cookbook.md` | 常用 Hook 脚本（Java/Native/OkHttp/加密参数捕获） |
| `references/frida-bypass-kit.md` | 反调试 / 反 Root / SSL Pinning / 模拟器绕过合集 |
| `references/android-advanced.md` | 加固脱壳、JNI/Native、动态加载等进阶主题 |
| `references/05-cross-platform-hybrid.md` | Flutter / React Native (Hermes) / UniApp / WebView JSBridge 专项 |
| `references/06-privacy-sdk-checklist.md` | 合规隐私权限、第三方 SDK 风险与 FileProvider/PendingIntent 审计清单 |
| `references/07-mobile-reporting-and-src.md` | 移动端报告写作、OWASP MASVS v2 映射、SRC/企业双口径与降噪标准 |

## 完成自检

- [ ] 已运行 `mobile_decompile_audit` 并核对样本包名、SHA-256 与 `quality_status`
- [ ] 已检查 Manifest/Plist、导出组件、签名方案、跨端容器与原生二进制 hardening
- [ ] 接口清单、签名位置、硬编码凭据提示已落 `record_fact`
- [ ] 有设备时已完成 CA/透明代理、Frida 绕过、UI 遍历截图与 IPC 探针；无设备时已写明边界
- [ ] 每条拟上报发现已通过 `mobile_evidence_check`（`src_ready: true`）且凭据已脱敏
