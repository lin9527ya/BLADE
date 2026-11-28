# 09 · 检查清单

## 信息收集

- [ ] 小程序名称、AppID、主体、开发者、客服、版本、更新时间、基础库版本。
- [ ] “更多资料”的服务及数据网址（request/upload/download/socket 域名白名单）。
- [ ] 关联公众号/视频号/APP/网站/备案/ICP/商标/软著。
- [ ] 小程序码/URL Link/Short Link/URL Scheme；分享卡片路径。
- [ ] 主包/分包/独立分包/插件；`app.json` 路由全集；`sitemap.json`。
- [ ] 云开发 envId、云函数名、集合名、存储路径。
- [ ] 接口域名/子域/C 段/端口/指纹/WAF/CDN/测试环境（dev/test/uat/pre）。
- [ ] GitHub/Gitee 泄露、sourceMap、`.git`、Swagger、错误页、调试页。
- [ ] 隐私政策、第三方 SDK 清单、收集字段、权限申请。
- [ ] 企业信息：子公司、邮箱、员工、供应链。
- [ ] 历史版本：第三方小程序商店、抓包缓存、旧版分包。
- [ ] 业务模式：电商/金融/医疗/教育/政务，锁定高价值接口。

## 接口测试

- [ ] 认证：无/空/过期/他人 token、JWT 篡改、alg none、弱密钥、kid/jku。
- [ ] 授权：水平/垂直/跨租户；`userId/openid/unionid/member_id/orderId/addressId` 替换。
- [ ] 方法/Content-Type：GET↔POST、JSON↔form、参数污染（`id=1&id=2`）。
- [ ] 参数：空/null/0/负数/小数/超大/数组/对象/Unicode/截断/重复。
- [ ] 注入：SQLi、NoSQLi、命令、SSTI、SSRF、XXE、反序列化。
- [ ] 上传：后缀/MIME/magic/SVG/HTML/zip/路径穿越/覆盖/大小/云 fileID。
- [ ] 下载：`fileId/path/url` 替换、路径遍历、签名 URL 过期、批量导出。
- [ ] 支付：金额/数量/币种/优惠券/订单归属/prepay_id/回调/退款/发货。
- [ ] 消息：短信/邮件频控、验证码复用/回显/爆破、用户枚举。
- [ ] 业务：步骤跳过、状态篡改、响应篡改、并发、库存、积分、抽奖、会员。
- [ ] 云开发：函数名/鉴权/DB 规则/存储规则/HTTP API/环境共享。
- [ ] 开放能力：小程序码/URL Link 参数、订阅 touser/page、客服消息、跳转 appId/path。
- [ ] 隐私：PII 明文、未同意先收集、过度收集、本地存储、日志。

## 云开发专项

- [ ] envId 泄露；测试/生产是否区分。
- [ ] `callFunction` 函数名全集；每函数是否只校验登录。
- [ ] 安全规则 `*` 是否 `invoke:true`；是否排除匿名；函数级例外。
- [ ] 函数内部是否用 `getWXContext().OPENID` 而非入参 openid；角色/归属校验。
- [ ] 输入：SQL/NoSQL/命令/SSRF/路径遍历/eval/`require(userInput)`。
- [ ] 集合清单；每集合 read/write/create/update/delete 规则。
- [ ] 是否 `read:true`/`write:true`；是否只校验 `auth != null`。
- [ ] `doc.get/update/remove`；`where` 批量；`aggregate/watch/count` 绕过。
- [ ] `get()` 是否可遍历；`{openid}` 变量正确使用。
- [ ] 云存储 read/write；fileID 枚举；临时 URL 有效期；上传类型/大小；覆盖/删除。
- [ ] HTTP API access_token 来源；AppSecret 泄露；HTTP 触发器公网。
- [ ] 环境共享跨小程序；未登录模式 `auth == null`。
- [ ] 云函数日志/环境变量泄露密钥/内网地址/连接串。

## 支付专项

- [ ] 下单价格来自服务端 DB；商品/数量/价格/优惠券一致。
- [ ] 金额单位（分/元）、负数/0/小数/超大/币种。
- [ ] 订单归属 openid；不能支付他人订单；不能改 `out_trade_no`。
- [ ] `prepay_id` 绑定 appid/mchid/openid；`requestPayment` 签名正确。
- [ ] 回调：验签、时间窗口、`Wechatpay-Serial`、APIv3 密钥解密、金额/商户号/订单号校验、幂等。
- [ ] 不接受 `return_code=SUCCESS` 作为唯一依据；不接受客户端“支付成功”。
- [ ] 退款：权限、金额≤实付、累计≤实付、原路退回、回调验签、状态机、并发。
- [ ] 发货：订单归属、物流单号、虚拟发货、`notifyConfirmReceive`。
- [ ] 对账：日账单、异常订单、重复支付、掉单。
- [ ] 沙箱/测试商户/0.01 元；不真实发货。

## 隐私合规

- [ ] 首次运行隐私弹窗；非默认勾选；拒绝后基本功能可用。
- [ ] 收集前告知目的/方式/范围/期限；敏感信息单独同意。
- [ ] 实际抓包收集项与隐私政策/SDK 清单一致。
- [ ] 权限：位置/相机/麦克风/相册/剪贴板/通讯录/设备信息；最小必要。
- [ ] 传输 HTTPS；PII 不明文在 URL/Header/Body；字段级加密（AES-GCM/ChaCha20-Poly1305 带 iv/tag）；签名/HMAC 抗重放。
- [ ] 存储：本地不存明文 token/PII；服务端加密/脱敏；保留期限。
- [ ] 用户权利：查询/更正/删除/撤回/注销/投诉。
- [ ] 第三方 SDK：清单/目的/共享字段/单独同意/匿名化。
- [ ] 未成年人/敏感个人信息/出境。
