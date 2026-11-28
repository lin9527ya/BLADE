# 05 · 微信开放能力滥用

| 能力 | 关键接口/参数 | 滥用方式 | 测试要点 | 证据 |
|---|---|---|---|---|
| 小程序码 | `wxacode.getUnlimited`：`scene`(≤32)、`page`、`check_path`、`env_version` | `scene/page` 注入 SQL/NoSQL/URL；`check_path:false` 打开未发布页；`env_version:develop/trial` 打开测试版 | 服务端是否拼用户输入；`page` 白名单；`env_version` 是否客户端可控 | 生成的码/请求、打开的页面 |
| URL Link | `urllink.generate`：`path`、`query`、`expire_*`、`env_version` | 开放重定向、钓鱼、长期链接、配额滥用 | `query` 是否被目标页信任；任意 path；过期过长 | 链接、落地页、请求 |
| URL Scheme | `generatescheme`：`jump_wxa.path/query`、`expire_*` | scheme 劫持、钓鱼、测试版入口 | `env_version` 是否可控；来源校验 | scheme、打开效果 |
| Short Link | `shortlink.generate`：`page_url`、`is_permanent` | 永久短链钓鱼、绕过域名检测 | 任意 page_url；永久 | 短链、落地页 |
| 订阅消息 | `subscribeMessage.send`：`touser`、`template_id`、`page`、`data` | 给任意 openid 发消息、`page` 钓鱼、模板数据注入、重复订阅 | `touser` 限当前用户；`page` 白名单；数据转义 | 发送请求、收到截图 |
| 客服消息 | `customerServiceMessage.send`：`touser`、`msgtype`、`miniprogrampage.pagepath` | 冒充客服、钓鱼页、骚扰 | `touser` 可控；`pagepath` 可控；48h 窗口 | 发送请求、消息截图 |
| 微信支付 | `requestPayment`、下单/回调/退款/发货 | 金额/数量篡改、回调伪造、重放、退款超额、0 元购 | 服务端按 DB 价重算；回调验签；退款累计校验；状态机 | 订单详情、支付参数、回调日志 |
| 发货信息 | `uploadShippingInfo`、`notifyConfirmReceive`、`setMsgJumpPath` | 虚假发货、订单归属篡改、物流注入 | 订单归属；他人订单发货；物流单号 | 请求、订单状态 |
| 内容安全 | `msgSecCheck`、`mediaCheckAsync` | 绕过检测、滥用配额 | 编码/分段/图片变体；仅前端调用 | 请求、检测结果 |
| 分享/朋友圈 | `onShareAppMessage`、`onShareTimeline`、`shareTicket` | 参数注入、CPRF、群信息泄露 | `path` 参数是否被信任；`shareTicket` 解密过度收集 | 分享路径、受害者状态 |
| 跨小程序跳转 | `navigateToMiniProgram`、`openEmbeddedMiniProgram`：`appId/path/extraData/envVersion` | 任意 appId/path 钓鱼、extraData 信任、测试版 | 用户可控 appId/path；envVersion；目标页信任 extraData | 跳转请求、落地页 |
| 微信运动 | `wx.getWeRunData` | 步数伪造、隐私过度收集 | 服务端解密；watermark 校验 | 解密结果 |
| 生物认证 | `wx.startSoterAuthentication` | challenge 重放、结果伪造 | challenge 一次性；服务端验签 | 请求、认证结果 |

## 小程序码/URL Link 签发链

1. 找到后端签发接口（`getUnlimited`/`urllink.generate` 的封装）。
2. 观察 `page/path/query/scene` 是否来自用户输入。
3. 构造自己的测试值（不注入真实 SQL；用无害标记如 `test123`）。
4. 打开生成的码/链接，看落地页是否把参数当可信输入（跳转、查询、渲染）。
5. 测 `check_path:false`、`env_version:develop/trial` 是否能打开未发布/测试版页面。
6. 测 `expire_type/time` 是否可设为长期/永久。

## 支付检查清单

- [ ] 下单价格来自服务端数据库，不来自客户端。
- [ ] 商品 ID/数量/价格/优惠券一致性。
- [ ] 金额单位（分/元）、负数/0/小数/超大数、币种。
- [ ] 订单归属 openid；不能支付他人订单；不能改 `out_trade_no`。
- [ ] `prepay_id` 绑定 appid/mchid/openid；`requestPayment` 参数签名正确。
- [ ] 回调验签（`Wechatpay-Signature` + 平台证书/公钥）、时间戳窗口、`Wechatpay-Serial`、APIv3 密钥解密。
- [ ] 回调校验 `mchid/appid/out_trade_no/amount.total/payer.openid`。
- [ ] 不接受 `return_code=SUCCESS` 作为唯一依据；不接受客户端“支付成功”。
- [ ] 退款：权限、金额≤实付、累计≤实付、原路退回、回调验签、状态机、并发。
- [ ] 发货：订单归属、物流单号、虚拟发货、`notifyConfirmReceive`。
- [ ] 对账：日账单、异常订单、重复支付、掉单。

## 订阅/客服消息检查

- `touser` 是否只能是当前用户。
- `page` 是否白名单。
- 模板数据是否转义（防止钓鱼文案）。
- 是否有频控/去重。
- 客服消息 `pagepath` 是否可控（可指向外部/钓鱼页面）。
- 48 小时窗口/会话状态是否被绕过。
