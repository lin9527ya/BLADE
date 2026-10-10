---
name: src-wordlist
description: 字典/词表选择与使用技能（授权测试用）。回答「这个场景该用哪本字典、怎么用、多大、什么速率」，并自带一组中文语境小字典（姓氏/中文姓名拼音/API 路径/参数名/服务默认口令/弱口令）。覆盖目录与内容发现、参数发现、上传绕过、用户名与口令猜测（授权内）、子域枚举、API/JS 路径、漏洞专用 payload 的字典路由，给出 ffuf/dirsearch/feroxbuster/gobuster/hydra 可直接粘贴的命令。当用户提到以下任意关键词时必须触发：字典、词表、wordlist、爆破、弱口令、口令字典、密码字典、用户名枚举、用户名字典、目录扫描字典、目录爆破、参数发现、参数名字典、fuzz 字典、payload 列表、top100、top1000、SecLists、fuzzDicts、用什么字典、字典选哪个、默认口令、中间件默认密码、子域字典。
---

# 字典与词表（src-wordlist）

> **定位**：把「要打什么」翻译成「用哪本字典 + 多大 + 什么速率 + 什么命令」。
> **边界**：授权范围内使用。**口令类有硬红线，见 §三**。节奏与范围认 `src-dig/rules/dig-scope-workflow.md`；发现的可疑面记 `资产/`，不写报告（`rules/vuln-report-format.md`）。

## 一、选字典四原则（按顺序，禁止跳步）

1. **先指纹后字典**：先确认栈（中间件/框架/语言/设备），再挑**栈专属**字典。不知道栈 = 还不该掏大字典。
2. **先窄后宽**：`top100 → 栈专属 → 定制 → 大而全`。**禁止**一上来就上 10 万行通用字典（噪声、封禁、低危堆料）。
3. **能推就推**：从已获信息生成候选（公司名+年份、域名+数字、接口回包里的 id 格式、JS 里的路由前缀）。**生成 > 枚举**。
4. **要能解释**：每次爆破都要能回答「为什么这本、为什么这个速率、期望证明什么」。答不出就不打。

## 二、场景路由表

| 场景 | 首选 | 次选 | 自带小字典 |
|---|---|---|---|
| 目录/内容发现 | 栈专属（tomcat/php 等）→ `top1000` | fuzzDicts `directoryDicts/`、SecLists `Discovery/Web-Content/` | — |
| 参数发现 | 接口样本 diff 出的字段名 | `params-common.txt` → SecLists `burp-parameter-names.txt` | `params-common.txt` |
| API 路径 | 静态 harvest 的前缀 + `api-paths-top200.txt` | fuzzDicts `apiDict/api.txt` | `api-paths-top200.txt` |
| 上传绕过 | 扩展名/`;`/大小写/双写**组合自生成** | fuzzDicts `upload_fuzz` 系列 | — |
| 用户名（中文语境） | 姓氏 + 姓名拼音 + 工号格式（自生成） | `cn-surnames.txt`、`cn-names-pinyin.txt` | ✔ |
| 口令（**授权内**） | 目标相关（`域名+年份`、`公司@123`）+ `weak-pass-top100.txt` | `service-default-creds.txt`（**默认口令先于通用爆破**） | ✔ |
| 子域枚举 | 证书透明（CT）+ 被动源 | SecLists `Discovery/DNS/` | — |
| 漏洞 payload | 先确认漏洞类型，再取对应 payload | fuzzDicts `rcePayloads`/`sqlDict`/`xssPayloads` | — |
| JS 文件发现 | 从已下载 chunk 反推命名规律（自生成） | fuzzDicts `JsFile` | — |

**获取大字典（不入库，clone 后用路径引用）**：
```bash
git clone --depth 1 https://github.com/TheKingOfDuck/fuzzDicts  ~/dicts/fuzzDicts
git clone --depth 1 https://github.com/danielmiessler/SecLists  ~/dicts/SecLists
```
**禁止**把 10 万行字典塞进对话上下文——只给路径。

## 三、口令类硬红线（不可违反）

1. **只在授权目标上做**；对登录接口的任何尝试**默认低速**：≤5 请求/秒、单账号 ≤10 次、总尝试 ≤200。出现锁定/验证码/风控 → **立即停**，记为「有防爆破」，不是换字典继续。
2. **禁止撞库**：不得使用任何真实泄露库（user:pass 对）打目标。
3. **禁止拖垮**：不并发轰炸登录口（禁 `-t 100` 一类）、不压测。
4. **不堆低危**：目标是证明「存在弱口令/默认口令」这一条事实，**拿到一个可复现的即停**。
5. **凭据脱敏**：试出的口令写进任务目录时按 `rules/vuln-report-format.md` 脱敏。

## 四、自带小字典（`wordlists/`）

| 文件 | 用途 |
|---|---|
| `cn-surnames.txt` | 常见姓氏（中文用户名/姓名拼音起点） |
| `cn-names-pinyin.txt` | 中文常见姓名拼音（`zhangwei`/`liuyang` 型） |
| `api-paths-top200.txt` | 常见 API 路径词（配合 `src-api-recon` 抽出的前缀） |
| `params-common.txt` | 常见参数名（参数发现起点） |
| `service-default-creds.txt` | 中间件/服务默认口令（`user:pass`，**先用它再用通用爆破**） |
| `weak-pass-top100.txt` | 中文语境弱口令（数字/年份/键盘序/公司变体） |

```bash
# 定制用户名表（自生成优于枚举）
while read s; do echo "$s"; echo "${s}2026"; echo "$s.admin"; done < wordlists/cn-surnames.txt | sort -u > /tmp/users.txt
# 目录发现（窄→宽）
ffuf -u https://target/FUZZ -w wordlists/api-paths-top200.txt -rate 20 -mc 200,301,302,401,403
```

## 五、命令模板（速率参数是纪律的一部分）

| 工具 | 模板 | 速率纪律 |
|---|---|---|
| ffuf | `ffuf -u URL/FUZZ -w dict -rate 20 -mc 200,301,302,401,403 -fs 0` | `-rate` 必填；禁 `-t 100` |
| dirsearch | `dirsearch -u URL -w dict --random-agent -t 10 --delay 0.2` | `-t ≤10` |
| feroxbuster | `feroxbuster -u URL -w dict --rate-limit 20 --scan-limit 2` | `--rate-limit` 必填 |
| gobuster | `gobuster dir -u URL -w dict -t 10 --delay 100ms` | `-t ≤10` |
| hydra（授权内） | `hydra -L users -P pass -t 2 -W 3 -f host http-post-form "..."` | **`-t 2`、`-f` 命中即停** |

## 六、与相邻技能的关系

| 场景 | 去哪 |
|---|---|
| 找接口/参数面（不是口令） | `src-api-recon`（harvest + 进壳 + 参数 diff） |
| 资产测绘/子域/指纹 | `src-recon` |
| 打到弱口令之后怎么利用、怎么写 | `src-dig`（`知识库/cn-fingerprint-creds.md` 指纹→默认口令对照） |
