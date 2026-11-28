# 08 · 案例库（脱敏）

| # | 案例 | 类型 | 关键手法 | 可复用点 |
|---|---|---|---|---|
| 1 | 某培训小程序 | 未授权 + 管理员密码泄露 | 反编译找 `user/getUserInfoByUsername`；构造 `username/school` 直接拿管理员 MD5 密码；再用 `loginByUsernameAndPassword` 登录 | 路由命名 `xxxByUsername/ByPhone/ById` 是未授权高发点 |
| 2 | 同上 | 鉴权绕过 + 逻辑 | 删除 `token` 参数绕过；改响应 `code=0` 登录；Cookie `NetId` 遍历 | 删除鉴权参数、响应包篡改、Cookie 参数越权 |
| 3 | 同上 | 任意密码重置 + AppSecret 泄露 | 改密码处验证码可绕过；`%%` 触发报错回显 appid/secret | 错误回显、验证码逻辑 |
| 4 | 某公益 SRC 四小程序 | 信息泄露/前端绕过/越权 | `phone/mobilePhone` 置空返回全部订单；响应 `code=0`、`false→true` 进后台；`member_id` 遍历收货地址 | 空参数模糊查询、响应篡改、`member_id` IDOR |
| 5 | 某小程序任意用户登录 | session_key 泄露 | 抓 `encryptedData/iv/sessionKey`；AES 解密改 `phoneNumber`；重加密重放；用接口查用户信息 | `sessionKey` 出现在请求即高危；AES-128-CBC/PKCS#7 |
| 6 | 某 sign 加密小程序 | 签名逆向 + 账号接管 + IDOR | 减法确定 sign 参数；反编译找 HMAC-MD5；Python 复现；改 sessionKey 登录他人；`addressId` 遍历订单 | 签名算法逆向、参数排序、URL 解码 |
| 7 | 某云开发小程序 | 云数据库越权 | 反编译见 `wx.cloud.database().collection("checkUser").where({userId})`；重打包改查询；`inotifywait` 替换缓存包 | 云 DB 前端直查、重打包/替换包 |
| 8 | 某众测小程序 | 未授权迂回 | 响应头 `Access-Control-Allow-Origin` 发现另一个域名；405 页面客服 QQ；得知仅某省 IP；换 IP 进入 H5 测试页 | 响应头/客服/地域限制迂回 |
| 9 | 某考研小程序 | 硬编码密钥 + IDOR + 付费绕过 | 云存储 SecretKey、短信 key；`GET /certifications/:uid` 读他人姓名/学校；`PUT /certifications/:id` 覆盖；本地缓存判 VIP | 云存储密钥、RESTful IDOR、客户端权益判断 |
| 10 | 某高校签到小程序 | 客户端密钥伪造 token | 教师/学生共用接口基座；前端 SecretKey 可签任意学号 token；替签到/伪造请假 | 共用 API 基座、客户端签 token |
| 11 | 某电商小程序 | 支付金额篡改 | 客户端传 `total_fee`；服务端未重算；0.01 元下单成功 | 服务端算价、订单金额一致性 |
| 12 | 某内容小程序 | 订阅消息滥用 | `touser` 可为任意 openid；`page` 可控；给测试账号发钓鱼页 | `touser/page` 白名单、频控 |

## 共性模式

- 入口在“正常页面 + 正常请求”，漏洞在“参数”和“后端信任”。
- 高发参数：`xxxByUserId/ByPhone/ByUsername`、`member_id`、`addressId`、`orderId`、`uid`、`openid`、`role`、`status`。
- 高发根因：
  - 前端隐藏按钮当鉴权
  - 响应包当权限
  - 客户端算价/判 VIP
  - 云函数只校验登录
  - DB 规则 `read:true`
  - 密钥硬编码
- 高发证据：A/B 双账号、响应差异、订单金额对比、session_key 解密、云函数返回他人数据。

## 快速突破口

1. 搜 `ByUserId/ByPhone/ByUsername/ByOpenId` 路由。
2. 搜 `role/isAdmin/vip/level/status` 客户端可控字段。
3. 搜 `session_key/sessionKey/encryptedData/iv`。
4. 搜 `appsecret/mch_key/aesKey/iv/privateKey`。
5. 搜 `cloud.callFunction` 函数名，尤其是 `admin/delete/export/listAll`。
6. 搜 `collection(` 集合名，配合 `read:true` 规则。
7. 搜 `getPhoneNumber` 绑定逻辑。
8. 搜 `sign/hmac/md5/sha` 签名函数。
9. 搜 `web-view src` 与 `navigateToMiniProgram`。
10. 搜 `check_path/env_version/trial/develop`。
