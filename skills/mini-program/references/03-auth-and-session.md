# 03 · 登录态与越权

## 标准登录链路

```
小程序                          开发者服务器                      微信服务器
  | wx.login() -> code(5min,一次性) |                                |
  | ---- code --------------------> |                                |
  |                                | GET /sns/jscode2session        |
  |                                | appid+secret+js_code+grant_type|
  |                                | -----------------------------> |
  |                                | <---- openid, session_key, unionid
  |                                | 生成自定义 token（绑定 openid） |
  | <---- token ------------------- |                                |
  | 存 storage，后续请求带 token     |                                |
```

- `code` 5 分钟、一次性；`code2Session` 必须服务端调用。
- `session_key` 是开放数据 AES 密钥，**不应下发客户端**；泄露 + `encryptedData/iv` 可解密手机号等。
- `unionid` 用于同开放平台多应用打通；泄露可跨应用关联。
- 自定义登录态：随机 token 绑定 openid + 过期时间；不要把 openid/unionid 当 token；不要放 URL。

## 手机号快速验证

新流程：
1. `<button open-type="getPhoneNumber">` 回调拿 `e.detail.code`（5 分钟、一次性）。
2. 服务端 `POST https://api.weixin.qq.com/wxa/business/getuserphonenumber?access_token=...`，body `{"code":"..."}`。
3. 返回 `phone_info.phoneNumber / purePhoneNumber / countryCode / watermark`。
4. 服务端校验 `watermark.appid` 后绑定。

旧流程：`encryptedData + iv + session_key`，AES-128-CBC/PKCS#7：
```
aeskey = Base64_Decode(session_key)
iv     = Base64_Decode(iv)
plain  = AES-128-CBC-Decrypt(aeskey, iv, Base64_Decode(encryptedData))
```
解密后校验 `watermark.appid`。

测试点：
- 请求/响应出现 `session_key`/`sessionKey`/`sk` → 高危；验证能否解密/重加密。
- 绑定接口是否只信客户端 `phoneNumber`；换成第二个手机号/他人手机号。
- `code` 是否可重放；服务端是否只消费一次；是否校验 appid。
- 是否前端解密、前端内置 AES key。
- 换绑手机号是否免旧验证。
- 是否用手机号作为登录/鉴权凭证。

## 未授权/越权矩阵（每敏感接口至少 10 态）

| # | 身份/凭证 | 目的 |
|---|---|---|
| 1 | 无 token | 未授权 |
| 2 | 空 token / `Bearer ` / `null` | 鉴权空值绕过 |
| 3 | 过期 token | 过期校验 |
| 4 | A 的 token + A 的资源 | 正常基线 |
| 5 | A 的 token + B 的资源 ID | 水平越权 |
| 6 | B 的 token + A 的资源 ID | 双向校验 |
| 7 | 普通用户 token + 管理员接口 | 垂直越权 |
| 8 | 伪造/篡改 token（alg none、弱密钥、kid/jku） | 令牌伪造 |
| 9 | 删 token 参数/改参数名/改方法/改 Content-Type | 鉴权绕过 |
| 10 | 重放旧请求/旧 code/旧签名 | 重放 |

补充：
- `userId/openid/unionid/member_id/uid/customerId/phone` 替换。
- `role/isAdmin/vip/level/status` 客户端可控位。
- `X-WX-OPENID`/`X-WX-UNIONID`/`X-Forwarded-For` 是否被后端误用。
- token 在 URL/Referer/日志；退出失效；多端并发。
- GraphQL：`node(id:)`、`viewer`、`me`、批量查询、introspection。
- 批量/导出：`pageSize` 调大、`export`、`download`、`listAll`。

## 账号接管常见路径

1. session_key 泄露 → 解密 `encryptedData` → 改手机号 → 重加密重放。
2. 手机号绑定只信客户端 → 直接改请求里的 `phoneNumber`。
3. 验证码回显/可爆破/可删除/可复用。
4. 换绑手机号不需要旧手机号验证。
5. 前端签 token（SecretKey 硬编码）→ 伪造任意 userId/openid。
6. 响应包 `code=0`/`false→true` 被后端信任。

## JWT 检查

- 解码 header/payload；检查 `alg`、`exp`、`aud`、`iss`。
- `alg:none`、弱 HMAC 密钥、`kid` 注入、`jku/x5u`。
- 服务端是否只 decode 不 verify；token 是否在 URL。
- 过期 token 是否仍可用；退出后是否失效。

## 会话管理

- 退出后旧 token 是否还能访问。
- 多端同时登录是否互踢。
- token 熵/可预测性。
- 会话固定/并发登录。
