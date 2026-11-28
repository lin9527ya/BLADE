# 移动端漏洞报告写作、OWASP MASVS v2 映射与 SRC 双口径标准

> 指导如何在 BLADE 上产出既满足高标准 SRC/众测审核心要求、又可直接交付企业客户的移动端渗透测试报告。

---

## 1. 提交前必须过 `mobile_evidence_check`

在调用 `report_finding` 之前，应先调用 `mobile_evidence_check` 核验以下 4 条硬性基线（目标达成 `src_ready: true`）：

1. **样本溯源 (`sample_provenance`)**：报告必须可追溯至具体应用包名（如 `com.target.pay`）与文件 SHA-256（由 `mobile_decompile_audit` 自动落于 `mobile_audit/manifest.json`）；若是跨版本测试，必须写明版本号。
2. **客户端追踪锚点 (`source_or_hook_trace`)**：指出逆向定位的类名、方法名、Manifest 组件名、`.so` 函数偏移或 Frida Hook/UI hierarchy 现场。
3. **服务端请求响应闭环 (`backend_or_impact_proof`)**：涉及接口/鉴权/数据越权时，必须附上 `http_proof`（真实请求响应包摘录或绑定的 `traffic_id`）及**正常账号/对照组对比**。
4. **敏感凭据脱敏 (`secret_masking_clean`)**：严禁在 `report_finding` 摘要、复现步骤及最终交付报告中粘贴可用明文 JWT、Authorization Bearer、阿里云/AWS AK/SK 或 RSA/EC 私钥。完整凭据仅作为资产留存于凭据节点（`record_credential` / `extract-session`），正文与报告必须掩码（如 `eyJ***abcd` / `LTAI***wxyz`）。

---

## 2. 严重度校准与常见伪阳性清单（防止误报/夸大评级）

### 绝对不能直接报「高危 / 严重」的纯客户端发现
| 常见发现 | 常见误判 | BLADE 正确评级与处理方式 |
|---|---|---|
| `android:allowBackup="true"` | 直接报中/高危敏感信息泄漏 | 无实际敏感到达证据时定 **低危 / 加固建议 (Info/Low)**；仅当真机/沙箱提取到明文 Session 且可跨端重放劫持账号时升级 |
| `android:debuggable="true"` | 生产包直接定高危 | 测试/灰度内测包若客户已知悉仅供调试，需先核实来源；正式线上包定 **中危**，并附本地调试注入拿到内部会话/数据的实测证明 |
| 反调试/Root 检测/SSL Pinning 缺失或可被 Frida 绕过 | 在已 Root 测试机用 Frida 绕过 Pinning 就报"SSL 校验被破解（高危）" | 有系统 Root 权限的设备上 Frida 绕过属于客户端固有局限，**不算单独的高危漏洞**；核心价值在于**绕过后再截获到的后端 API 漏洞**。仅在**普通未 Root 用户日常网络场景下**APP 完全不校验证书（如 `onReceivedSslError` 无条件 `proceed()` 或自定义 `X509TrustManager` 空实现）时可独立定 **中危（中间人降级风险）** |
| 客户端本地 AES/RSA 密钥固定写死 | 直接给高危 | 仅静态存在密钥而后端无对应敏感能力时最多定 **中低危**；若以此还原接口签名成功刷接口、越权修改订单金额或解密他人响应体，则按后端业务危害定 **高危/严重** |
| 本地弱加密 / SharedPreferences 存 Token | 未结合设备场景 | 无实际本地/IPC 利用链时定 **低/中危（MASVS-STORAGE）**；结合导出组件 / WebView `file://` 读取 / 公共外部存储才升级 |

---

## 3. 多账号 A/B 越权取证标准流程（IDOR / 权限绕过）

1. 在任务详情页「**移动端工作台**」打开代理并由操作者登录 **账号 A**，选中角色 **`user_a`** 点击「**提取实时会话**」，记录接口路径与资源 ID（如 `/api/v2/orders/10086`）。
2. 在设备上切换登录 **账号 B**，选中角色 **`user_b`** 再次点击「**提取实时会话**」。
3. Agent 使用 `http_request` / `diff_responses`（如有 API 签名参数，传入 `mobile_sign_method`）：
   - 分别用 `user_a` 请求作为正常基线、用 `user_b` 令牌携带目标 `order_id=10086` 重放；
   - 将 A/B 请求的完整响应差异（状态码、敏感字段比对）与脱敏 Header 一并附入证据，即可产出不可争议的 SRC 一等奖级高危证据包。

---

## 4. OWASP MASVS v2 7 大验证域映射模板

导出最终报告时，BLADE 会结合样本静态属性与 `static_findings` 自动汇总 **OWASP MASVS v2** 概览表；撰写单条发现详细分析 (`FindingReports`) 时建议标明归属域：
- **MASVS-STORAGE**：沙箱权限、`allowBackup`、`SharedPreferences`/SQLite 明文凭据、外部存储泄漏、Logcat 敏感日志、剪贴板/截图缓存。
- **MASVS-CRYPTO**：弱算法 (`AES/ECB`, `DES`, `MD5` 作口令哈希)、硬编码秘钥/IV、不安全随机数 (`java.util.Random`)。
- **MASVS-AUTH**：会话有效期与退出失效、客户端本地认证绕过、生物识别 (`BiometricPrompt`) 未绑定 `CryptoObject` 仅布尔回调。
- **MASVS-NETWORK**：明文 HTTP 传输 (`usesCleartextTraffic` / ATS 豁免)、空实现的 `HostnameVerifier` / `TrustManager`、WebView `onReceivedSslError.proceed()`。
- **MASVS-PLATFORM**：导出组件 (`Activity`/`Service`/`Receiver`/`Provider`) 权限缺失、Deep Link URL 参数注入、WebView JSBridge 越权与文件域开放、可变隐式 `PendingIntent`。
- **MASVS-CODE**：`debuggable=true`、`.so`/Mach-O 缺 PIE/NX/RELRO/Canary、第三方旧版本原生库已知 CVE、动态加载 DEX 安全校验缺失。
- **MASVS-RESILIENCE**：反调试/完整性校验/Root 检出强度（注明：抗逆向属于纵深加固层面，不应与高危业务漏洞混为一谈）。
