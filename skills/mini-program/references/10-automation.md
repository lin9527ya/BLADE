# 10 · 自动化审计

## 流水线

```
采集（主包+全部分包）→ 安全提取/反编译 → 质量门禁
→ 静态提取（路由/接口/密钥/云环境/签名/web-view/插件）
→ AST/污点 → LLM 语义复核（分片、证据优先）
→ 动态 auth matrix → 人工复核 → 证据/报告
```

## 静态提取规则

```python
PATTERNS = {
  "url":        r"https?://[^\s\"'<>]+",
  "api_path":   r"[\"'](/[a-zA-Z0-9_\-/{}.:]+)[\"']",
  "wx_request": r"wx\.(?:request|uploadFile|downloadFile|connectSocket)\s*\(\s*\{[^}]*url\s*:\s*[\"']([^\"']+)",
  "cloud_fn":   r"wx\.cloud\.callFunction\s*\(\s*\{[^}]*name\s*:\s*[\"']([^\"']+)",
  "cloud_env":  r"env\s*:\s*[\"']([a-zA-Z0-9\-]+)[\"']",
  "secret":     r"(?i)(appsecret|accesskey|secretkey|api[_-]?key|mch[_-]?key|aes[_-]?key|private[_-]?key)\s*[:=]\s*[\"'][^\"']{8,}",
  "jwt":        r"eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+",
  "sign":       r"(?i)(sign|signature|hmac|md5|sha1|sha256)\s*[:=]",
}
```

- 密钥加熵检测（Shannon ≥4.0 且长度 ≥20）与上下文判断。
- 接口提取要处理字符串拼接、模板字符串、常量折叠；纯正则有漏报。
- 封装层：`request.js/http.js/api.js` 中的 `request.get('/api')`、`request({url})`。
- `baseURL/apiHost/globalData.baseUrl/config.js` 常量单独提取，再与路径拼接。

## AST / 污点

- 用 `@babel/parser` + `@babel/traverse`（或 acorn/esprima）解析 `app-service.js`。
- Source：`options.query`、`event.detail`、`wx.getStorageSync`、`wx.getUserProfile`、`this.data`。
- Sink：`wx.request`、`wx.uploadFile`、`wx.cloud.callFunction`、`web-view src`、`wx.navigateTo`、`wx.setStorage`、`eval`。
- 关注：url 拼接、data 直接来自 source、sign 是否覆盖全参数、token 硬编码。
- 页面路由：`Page({onLoad(options)})` 的 options 是否流入 sink（CPRF 线索）。

## LLM 分层

- 脚本全量扫保证规则覆盖；LLM 只做语义复核/误报过滤/上下文关联。
- 分片按文件/函数；每片输出 JSON：`{file,line,type,confidence,evidence,missing_evidence}`。
- 证据优先：无请求/响应/代码行号不算 confirmed；区分 `hypothesis/confirmed/rejected/blocked`。
- 不要把含 PII/密钥的代码发给外部 LLM；优先本地模型或脱敏。

## 动态 auth matrix

- 从 `app.json` 提取路由，用 CDP/自动化逐页访问，记录所有请求。
- 每请求 4 态：无凭证、真 ID+空 token、真 ID+假 token、随机 ID。
- 响应分类：认证缺失、水平越权、垂直越权、跨租户、404、公开资源。
- 工具：Burp AuthMatrix/Authz、mitmproxy 脚本、Playwright/CDP。

## 签名还原流程

1. 在反编译 JS 中搜 `sign/signature/hmac/md5/sha`、`CryptoJS`、`encrypt`。
2. 找签名函数定义：参数列表、排序、拼接、时间戳、随机数、密钥。
3. 用减法验证：删一个参数看签名是否仍被接受；改 body 不重算看是否通过。
4. 在本地用 Python/Node 复现签名；与真实请求对比。
5. 改包重放：修改业务参数 → 重算签名 → 发送。
6. 记录：算法、参数顺序、密钥来源（硬编码/接口下发）、时间戳窗口。

## 去混淆

| 工具 | 适用 | 许可 | 注意 |
|---|---|---|---|
| webcrack | obfuscator.io、webpack/browserify | MIT | 隔离环境 |
| synchrony | javascript-obfuscator | GPL | 旧版本可能失败 |
| restringer | obfuscator.io | MIT | 部分模块 eval |
| decode-js | jsjiami v6/v7、sojson | GPL | 会 eval，输入必须纯混淆代码 |
| Babel AST 自研 | 字符串数组/常量折叠 | — | 最可控 |

原则：去混淆会执行不可信代码，必须在无网络、无凭据容器中运行；保留原始文件；产物标注“机器还原”。

## 混淆检测

```python
obf = bool(re.search(r'_0x[0-9a-f]{4,6}', js)) and (
    'while (!![])' in js or 'debugger' in js or
    'constructor("return this")' in js
)
```

## 加固/新模板

- `__wxCodeSpace__`/`batchAddCompiledTemplate`：传统 WXML 工具会失败/占位；标记 `partial`，优先动态。
- JSVMP/WASM/SM4：静态还原有限；动态 Hook/LLM 辅助；报告标注加固等级。
- `.wasm.br`：brotli 解压；wabt/Ghidra/wasmtime 分析。
