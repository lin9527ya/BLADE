# 移动端隐私合规、第三方 SDK 与组件攻击面检查清单

> 用于移动端渗透中的高价值敏感信息采集面、权限滥用、第三方 SDK 供应链及 Android/iOS 特有组件安全核查。

---

## 1. 常见隐私违规与敏感数据采集面（静态 + Frida + Logcat 验证）

| 检查项 | 静态定位线索 | 动态验证方法 |
|---|---|---|
| 隐私弹窗前超前采集 | `Application.attachBaseContext` / `onCreate` 过早初始化第三方统计/推送 SDK | `mobile_frida_hook` 拦截设备标识读取 API，配合清数据冷启动时间线 |
| 设备敏感标识明文收集/上报 | `getDeviceId` / `getImei` / `Settings.Secure.ANDROID_ID` / `WifiInfo.getMacAddress` / `ASIdentifierManager` | 在抓包 `traffic` 中搜索设备号、OAID、MAC、GPS 坐标明文上传请求 |
| 剪贴板静默读取 | `ClipboardManager.getPrimaryClip` / `UIPasteboard.general.string` | 启动 APP 前置入哨兵标记文本，观察抓包/logcat 是否被静默上传 |
| 敏感数据落盘与日志泄漏 | `SharedPreferences` / `SQLiteDatabase` / `NSUserDefaults` / `Log.d`/`NSLog` | 调 `mobile_app_ctrl(action="sandbox_audit")` 与 `logcat_audit` 一键核查 |
| 后台截图保护缺失 | 涉及支付/密码/身份页面未设置 `FLAG_SECURE` | 使用 `mobile_ui_explore(action="screenshot")` 验证关键界面是否可被直接截屏 |

---

## 2. 第三方 SDK 供应链与硬编码凭证分类审计

拿到 `mobile_decompile_audit` 产出的 `secrets.txt`、`endpoints.txt` 与 `sbom_hints` 后，需按归属区分处理，避免误把低风险公开统计 Key 当作高危系统漏洞：

| 类别 | 典型特征 | 风险判定标准 |
|---|---|---|
| 高危 · 对象存储/云控 Master AK/SK | 阿里云 `LTAI...`、AWS `AKIA...`、腾讯云 `AKID...`、私有 OSS/COS Upload Policy + SecretKey | 必须实际尝试只读列举或构造预签名校验，确认桶写/列权限或接口权限才可定中高危；**报告正文仅保留前 4 后 4 位掩码** |
| 高危 · 自有后端加解密/签名私钥 | AES Key/IV 固定写死用于核心登录/交易报文、HMAC-SHA256 固定 Secret、非对称加密私钥（PKCS#8 `BEGIN PRIVATE KEY`） | 验证可离线重算请求签名或解密敏感响应体，或配合 `mobile_rpc_sign` 证明服务端不防重放/可伪造任意参数 |
| 中低 · IM / 地图 / 统计 / 推送 AppKey | 友盟、极光、高德、百度地图、Sentry DSN | 若公开 AppKey 无法单独调用付费配额管理后台或窃取他人隐私数据，仅作信息收集或提示限制 Referer/包名签名绑定 |
| 组件 CVE 与 原生 Binary Hardening | `sbom_hints` 命中的旧版 `OpenSSL`、`libcurl`、`FFmpeg`、`expat`、`libpng` | 用 `lookup_cves` 关联已知 RCE/内存破坏漏洞；确认 `.so` / Mach-O 是否缺 `PIE`/`NX`/`Stack Canary`/`Full RELRO` |

---

## 3. Android 组件特化高频漏洞清单

### A. FileProvider 与 ContentProvider 路径穿越 / 任意私有文件读写
- 检查 `<provider android:exported="true">` 或临时 `FLAG_GRANT_READ_URI_PERMISSION` / `FLAG_GRANT_WRITE_URI_PERMISSION`。
- 若自定义 `openFile(Uri uri, String mode)` 未规范化校验 `canonicalPath.startsWith(allowedRoot)`，通过 `content://<authority>/../../../shared_prefs/auth.xml` 可跨出目录读取 Token（配合 `mobile_ipc_probe(kind="provider")` 验证）。
- 若 `<paths><root-path name="..." path="" /></paths>` 对外可被其他组件触发临时授权 Intent，可导致私有目录任意读写。

### B. 可变隐式 PendingIntent 劫持
- 在通知、小组件、系统广播中调用 `PendingIntent.getActivity(...)` / `getBroadcast(...)` 创建了带空的基底隐式 `Intent` 且使用 `FLAG_MUTABLE`（或 Android 12 前未加 `FLAG_IMMUTABLE`），恶意同机 APP 拦截广播后可填充 `Component`/`ClipData` 以宿主身份访问非导出 `ContentProvider` 或私有组件。

### C. StrandHogg / TaskHijacking & Exported Activity 认证绕过
- `android:launchMode="singleTask"` 且未声明 `android:taskAffinity=""`；
- 内部敏感 Activity（设置支付密码、二次验证后的转账页、内置管理员工具页）被标为 `android:exported="true"` 或带 `<intent-filter>`，可通过 `adb shell am start -n pkg/.SensitiveActivity` 直接绕过前置登录或手势密码。

---

## 4. iOS 特化审计 checklist

1. **`Info.plist` & ATS**：是否全局开启 `NSAllowsArbitraryLoads = YES`，或 `CFBundleURLTypes` 注册了未校验来源参数的 Custom URL Scheme。
2. **Entitlements & Provisioning**：`aps-environment`、`get-task-allow=true`（开发环境调试标记泄露至发布包）、`keychain-access-groups` 是否过宽。
3. **二进制加密与保护 (`FairPlayEncrypted` & Hardening)**：App Store 发布包正常应带 cryptid=1，内测/企业签名 IPA 若 `cryptid=0` 且无 PIE / 含大量明文 Symbol 便于直接逆向 `class-dump` / `radare2`。
4. **Keychain 存储属性**：使用 `kSecAttrAccessibleAlways` 或未在卸载后/退登后清理敏感 Token，导致旧凭据仍保留在设备 Keychain 中（使用 `mobile_frida_hook` `ios_keychain_dump` 验证）。
5. **Pasteboard / Snapshot**：退后台未覆盖遮罩图层导致 `/Library/Caches/Snapshots/` 缓存敏感明文界面快照。
