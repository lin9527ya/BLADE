# 01 · 信息收集与取包解包

## 取包路径

### Windows 微信 3.x
```
C:\Users\<user>\Documents\WeChat Files\Applet\<appid>\<version>\
  __APP__.wxapkg        # 主包
  __FULL__.wxapkg       # 整包（部分缓存有）
  __SUBCONTEXT__.wxapkg # 子上下文
  _pages_xxx_.wxapkg    # 分包
  plugin.wxapkg         # 插件
```
- 通常 `V1MMWX` 加密；文件夹名即 AppID。
- `publicLib`/`publicTestLib` 是基础库，不是业务代码。

### Windows 微信 4.x（Radium）
```
C:\Users\<user>\AppData\Roaming\Tencent\xwechat\radium\users\<hash>\applet\packages\<appid>\<version>\
C:\Users\<user>\AppData\Roaming\Tencent\xwechat\radium\Applet\packages\<appid>\<version>\
C:\Users\<user>\AppData\Roaming\Tencent\WeChat\radium\Applet\packages\<appid>\<version>\
```
- `xwechat_files` 是聊天文件目录，通常不含 wxapkg。
- 仍普遍 `V1MMWX`；分包按需下载，**未进入过的页面不会落盘**——测试前先让用户遍历目标页面。
- 两处 radium 路径都可能存在，需递归扫描。

### macOS
```
~/Library/Containers/com.tencent.xinWeChat/Data/.wxapplet/packages/<appid>/<version>/__APP__.wxapkg  # 3.x
~/Library/Containers/com.tencent.xinWeChat/Data/Documents/app_data/radium/users/<hash>/applet/packages/<appid>/<version>/  # 4.x
~/Library/Containers/com.tencent.xinWeChat/Data/Documents/app_data/radium/Applet/packages/<appid>/<version>/
```
- 3.8.0+ 多数明文；<3.8.0 为 AES-ECB + 尾部 `WAPkgEncryptedTagForMac`，需 Frida 提 key；4.x 看文件头。

### Android
```
/data/data/com.tencent.mm/MicroMsg/<hash>/appbrand/pkg/*.wxapkg
/data/data/com.tencent.mm/MicroMsg/appbrand/pkg/general/*.wxapkg
/data/user/999/com.tencent.mm/MicroMsg/<hash>/appbrand/pkg/
```
- 需 root/root 模拟器；包名多为数字，不是 AppID；通常明文。

### iOS
```
/var/mobile/Containers/Data/Application/<UUID>/Library/WechatPrivate/<hash>/WeApp/LocalCache/release/<appid>/<n>.wxapkg
```
- 需越狱；文件夹名即 AppID；通常明文。

### 开发者工具
不存在“打开任意小程序就有明文 wxapkg”的稳定路径；`WeappFileSystem` 是运行时文件系统，`WeappVendor` 是框架代码。

## AppID

- 格式 `wx` + 16 位小写十六进制。
- PC/macOS/iOS：`packages/<appid>/<version>` 文件夹名。
- Android：包名不是 AppID；用“更多资料”/抓包/配置关联确认。
- **`wxid_...` 是微信账号，不是 AppID**；填错只出乱码。

## 包格式（明文）

```
0xBE + u32 info1 + u32 indexLen + u32 bodyLen + 0xED + u32 fileCount
+ entries(nameLen + name + offset + size) + data
```
兼容：
- `indexLen` 可能含/不含 4B fileCount → `14+indexLen+bodyLen` 或 `18+indexLen+bodyLen`。
- offset 可能绝对或相对数据区；用首文件 offset 判断。
- 数据区可能重叠；不要假设连续。
- 文件名可能以 `/` 开头，含 `__plugin__/`、`plugin-private://`。

## V1MMWX 解密

```python
key = PBKDF2-HMAC-SHA1(appid, b"saltiest", 1000, 32)
head = AES-256-CBC-decrypt(key, IV=b"the iv: 16 bytes", data[6:1030])[:1023]
xor = ord(appid[-2])
plain = head + bytes(b ^ xor for b in data[1030:])
```
解密后校验 `plain[0]==0xBE`、`plain[13]==0xED`、长度自洽。

## 解包工具阶梯

1. **自研安全提取**：`scripts/wxapkg_safe.py`——索引预校验、拒绝 `../`/绝对路径/UNC/NUL、安全提取。即使反编译失败，JS/JSON 仍可审计。
2. **wedecode**（若安装）：4.x/分包支持最好，WXML 绑定保留。
3. **unveilr**（若设置 `UNVEILR_DIR`）：WXML 质量交叉验证。
4. **KillWxapkg**（兜底）：快、MIT；但 WXML 可能丢 `{{}}/wx:if/wx:for`，失败也可能 exit 0，必须看产物。

## 质量门禁

- `app.json/app-config.json` 可解析、`pages` 非空。
- WXML 非空率、绑定率（`{{`）、占位符比例。
- JS 非空率、`Page({data:{}})` 占位比例。
- 检测 `batchAddCompiledTemplate`/`__wxCodeSpace__` → `partial`。
- 质量 `partial` 时，WXML 逻辑可能缺失，优先动态验证补齐。

## 常见失败原因

| 现象 | 原因 |
|---|---|
| 解出乱码/首标记错误 | AppID 错（用了 wxid/大小写错） |
| 未找到任何文件 | `-in` 传了 AppID 目录而非版本目录（KillWxapkg 不递归） |
| 文件数为 0 | 索引长度变体/offset 基准不兼容 |
| 路径穿越告警 | 包内文件名含 `../`（不可信包） |
| WXML 空壳 | 新模板/工具还原能力不足/分包未下载 |
| 插件配置丢失 | 部分工具会删 `plugins`，需从 `app-config.json` 或插件包恢复 |
