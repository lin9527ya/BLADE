# 04 · 云开发（CloudBase）安全

## 攻击面

1. **envId 泄露**：`wx.cloud.init({env:"xxx"})`；envId 非密钥，但可定位环境。
2. **云函数名枚举**：`wx.cloud.callFunction({name})` 全提取；函数名暴露业务（`adminDeleteUser`、`getAllOrders`）。
3. **云函数鉴权缺失**：默认规则 `{"*":{"invoke":"auth != null"}}` 只要求登录；函数内不校验角色/归属即未授权/越权。安全规则只支持登录/匿名判断，细粒度权限必须在函数内实现。
4. **云函数 IDOR**：参数直接拼 `openid/userId/orderId`；可信身份是 `getWXContext().OPENID`。
5. **云函数注入/SSRF/RCE**：用户可控 URL → SSRF（`http://metadata.tencentyun.com/latest/meta-data/`、内网）；`child_process.exec`/`eval`/`new Function`/`fs.readFile` 拼接 → 命令执行/任意文件读写；NoSQL 注入（`$ne/$gt/$where`）。
6. **数据库规则**：
   - `read:true` → 所有登录用户可读全表；`write:true` → 可写全表。
   - `auth.openid != null` → 只校验登录，不校验归属。
   - 规则要求查询条件是规则子集；`doc.get()` 只带 `_id` 常不满足；须 `where({_id,_openid:'{openid}'})`。
   - `get()` 跨文档引用可遍历。
   - `aggregate/watch/count` 也要测规则约束。
7. **云存储规则**：`read:true` 任意下载 fileID；`write:true` 任意上传/覆盖；服务端/控制台始终全权限；fileID 可预测可枚举。
8. **HTTP API**：`tcb/invokecloudfunction` 需 access_token；AppSecret 泄露即可调用；云函数 HTTP 触发器可能公网。
9. **环境共享/多小程序**：A 用户可能访问 B 应用数据；测跨 appid 边界。
10. **未登录模式/单页模式**：`auth == null`；规则 `read:true` 时匿名可读。

## 测试步骤

1. 反编译 → 提取 `wx.cloud.init`/`env`/`callFunction name`/`collection(...)`/`uploadFile` 路径。
2. 自己账号走正常业务，记录每函数调用与参数。
3. 每函数参数最小化/置空/类型混淆/换 ID/加 openid；观察错误是否泄露函数名/集合名/堆栈。
4. 数据库：客户端 SDK 直查 `db.collection('x').get()`；全量 → `read:true`；再测 `where({_openid})`/`doc(id).get()`/`update/remove`/`count/aggregate/watch`。
5. 存储：`downloadFile({fileID})`；他人 fileID、可预测路径、任意上传、覆盖、删除。
6. 云函数内部：SSRF（collaborator 域名）、命令注入（无害 echo）、路径遍历、NoSQL 注入（`$ne`）。
7. 拿到 AppSecret/AK/SK：先做最小化只读验证。
8. 证据：envId、函数名、集合名、请求/响应、A/B 数据对比、规则配置截图。

## 云数据库规则速查

| 规则 | 风险 |
|---|---|
| `"read": true` | 所有登录用户可读全表 |
| `"write": true` | 所有登录用户可写全表 |
| `"read": "auth != null"` | 只校验登录，不校验归属 |
| `"read": "doc._openid == auth.openid"` | 查询必须带 `_openid` 条件，否则可能绕过/报错 |
| `"read": "auth.openid in get('database.collection.roles').where({...}).get().data.map(...)"` | 复杂规则要测 `get()` 遍历与性能 |

## 云函数安全清单

- [ ] `getWXContext().OPENID` 是否用于身份，而不是入参 `openid`。
- [ ] 角色/归属是否在函数内校验。
- [ ] 管理员函数是否只允许管理员调用。
- [ ] 入参是否进入 SQL/NoSQL/命令/URL/文件路径。
- [ ] 错误是否回显堆栈/集合名/内网地址。
- [ ] 环境变量/日志是否泄露密钥。
- [ ] HTTP 触发器是否公网可直接调用。
- [ ] 是否有未使用的测试函数/调试函数。

## 云存储清单

- [ ] `read/write` 规则。
- [ ] fileID 是否可枚举（时间戳/自增/用户 ID）。
- [ ] 临时 URL 有效期。
- [ ] 上传类型/大小/后缀限制。
- [ ] 覆盖/删除权限。
- [ ] 是否可上传 HTML/SVG/JS 并被 CDN 以可执行类型返回。

## 证据口径

- 未授权云函数：envId + 函数名 + 请求参数 + 返回数据（他人字段）。
- 开放数据库：集合名 + 查询 + A/B 数据对比。
- 开放存储：fileID + 下载内容 + 上传结果。
- 规则错误：规则配置截图（授权情况下）+ 复现请求。
