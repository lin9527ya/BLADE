# 字典导航（大字典路径映射 + 组合配方）

> 大字典**不入库**。先 clone，再用**路径**引用（禁止把内容灌进上下文）。
> ```bash
> git clone --depth 1 https://github.com/TheKingOfDuck/fuzzDicts ~/dicts/fuzzDicts
> git clone --depth 1 https://github.com/danielmiessler/SecLists ~/dicts/SecLists
> ```

## 一、fuzzDicts（中文实战向，优先）

| 场景 | 路径（相对 `~/dicts/fuzzDicts/`） |
|---|---|
| 目录（通用） | `directoryDicts/Filenames_or_Directories_All.txt`、`dicc.txt`、`directoryDicts/top3000.txt` |
| 目录（栈专属） | `directoryDicts/` 下的 `phpFileName.txt`、`admin变种.txt` 等 |
| 参数名 | `AllParam.txt` |
| API 路径 | `apiDict/api.txt` |
| 上传绕过 | `*_upload_fuzz.txt`（apache/iis/tomcat/win/all，按指纹选） |
| 用户名字典 | `userNameDict/`：`ChinaUserName.txt`（大）、百家姓 top3000 拼音、`常用用户名.txt`、`top300_lastname.txt`、`top500.txt`、`test-phone.txt` |
| 弱口令 | `passwordDict/ServiceWeakPass/<服务>/user.txt`、`passwordDict/Middleware/<中间件>-user.txt` |
| RCE/SQL/XSS payload | `rcePayloads/`、`sqlDict/`、`xssPayloads/` |
| XXE / SSRF | `XXEDicts/`、`ssrfDict/`（含 CentOS/AIX `/etc/` 文件列表） |
| CTF 专用 | `ctfDict/ctf.txt`（非 CTF 场景不要用） |

## 二、SecLists（覆盖广、命名规范）

| 场景 | 路径（相对 `~/dicts/SecLists/`） |
|---|---|
| 目录/内容发现 | `Discovery/Web-Content/`：`raft-small-words.txt`（先）、`directory-list-2.3-medium.txt`（后） |
| 参数名 | `Discovery/Web-Content/burp-parameter-names.txt` |
| 子域 | `Discovery/DNS/subdomains-top1million-5000.txt`、`dns-Jhaddix.txt` |
| 用户名 | `Usernames/top-usernames-shortlist.txt`、`Names/` |
| 口令 | `Passwords/Common-Credentials/10-million-password-list-top-1000.txt`（**只用 top-1000，别上 million**） |
| API/GraphQL | `Discovery/Web-Content/api/`、`graphql.txt` |
| 上传扩展 | `Discovery/Web-Content/web-extensions.txt` |

## 三、选择顺序（对齐 SKILL.md §一）

```
本技能自带小字典（≤200 行）
  → 栈专属（指纹已知）
  → 自生成（公司/域名/年份/id 格式/接口样本 diff）
  → fuzzDicts 对应目录
  → SecLists（最后，且优先 small/medium，禁止上 huge）
```

**组合与去重**（定制表永远优先于通用表）：

```bash
# 1) 合并 + 去重 + 去空行（保持顺序：定制的在前）
cat /tmp/custom.txt ~/dicts/fuzzDicts/apiDict/api.txt | awk 'NF && !seen[$0]++' > /tmp/api_merged.txt
wc -l /tmp/api_merged.txt

# 2) 目标定制口令（公司/域名/年份变体）——生成优于枚举
DOMAIN=acme; YEAR=$(date +%Y)
for base in "$DOMAIN" "${DOMAIN^}" admin root; do
  for suf in "" "@123" "@${YEAR}" "${YEAR}" "123" "!@#"; do echo "${base}${suf}"; done
done | sort -u > /tmp/pass_custom.txt
```

## 四、构造 hydra/ffuf 的输入

本技能 `service-default-creds.txt` **本身就是** `hydra -C` 的格式（`user:pass` 一行一条）：

```bash
# 默认口令（先！）——单目标、慢速、命中即停
hydra -C wordlists/service-default-creds.txt -t 2 -W 3 -f <host> <service>

# 自定用户名 × 弱口令（授权内，低速）
hydra -L /tmp/users.txt -P wordlists/weak-pass-top100.txt -t 2 -W 3 -f <host> http-post-form "/login:u=^USER^&p=^PASS^:F=密码错误"
```

ffuf 直接吃本技能字典：

```bash
ffuf -u https://target/FUZZ -w wordlists/api-paths-top200.txt -rate 20 -mc 200,301,302,401,403 -fs 0
ffuf -u https://target/api/user?FUZZ=1 -w wordlists/params-common.txt -rate 20 -fs 0   # 参数发现
```

## 五、速率与停止（对齐 SKILL.md §三）

| 类别 | 速率 | 停止条件 |
|---|---|---|
| 目录/路径发现 | ≤20 req/s | 无（非口令类） |
| 参数 fuzz | ≤20 req/s | 无 |
| **口令/登录类** | **≤5 req/s，单账号 ≤10 次，总计 ≤200** | 锁定/验证码/风控 → **立即停** |
| 子域枚举 | 被动优先；主动 ≤50 req/s | 权威 DNS 限流 |

**记住**：字典不是越多越好。用大字典喷不出结论时，问题通常在**指纹没认准**或**目标没看全**，不是字典不够大。
