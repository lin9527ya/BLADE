// Package policy 实现 BLADE 的「任务级探索策略」：一份结构化的边界声明，告诉
// planner「哪些方向不要提」、告诉 worker「哪些手段不要做」，并在意图层与命令层
// 两个执行点上硬性生效。
//
// 设计纪律（与 P18 的约束编译同族）：
//   - 只做**确定性**判定：命中封闭词表里的词/形状就拒绝，不做语义推断；
//   - 全部是纯函数（本包不依赖 db / agent / server），可表驱动单测；
//   - 拒绝必须带**可操作的改写建议**——意图是模型自己写的，改写成本极低，
//     误伤会自然收敛（命令层才是硬底线，语义层允许宽松一点）。
//
// 重要边界：本包只做**加法**——平台内置的破坏性拦截规则是全局底线，策略放开某类
// 操作不会解除内置规则（见 docs/探索策略.md 与任务页策略卡的说明）。
package policy

import (
	"regexp"
	"strings"
)

// Technique 是一条「禁用手段」词表项。
type Technique struct {
	ID    string `json:"id"`
	Label string `json:"label"` // 中文标签（UI 与拒绝理由里显示）
	// Hint 是给模型的「合规改写建议」：被拒时附在理由里，让它知道往哪改。
	Hint string `json:"hint"`
	// Words 是匹配词：纯 ASCII 的走词边界匹配，含中文的走子串匹配。
	Words []string `json:"words"`
}

// Techniques 是封闭的禁用手段词表。加项 = 在这里加一行（ID 一旦发布不要改名，
// 它是策略 JSON 里的取值，也是被拒记录里的审计字段）。
var Techniques = []Technique{
	{
		ID: "bruteforce", Label: "口令爆破/认证猜测",
		Hint:  "改为只读评估认证强度：看失败锁定、验证码、登录速率限制、错误回显差异、默认口令仅作单次判断",
		Words: []string{"hydra", "medusa", "ncrack", "patator", "crowbar", "brutex", "brute force", "brute-force", "bruteforce", "password spray", "credential stuffing", "爆破", "弱口令", "撞库", "字典攻击", "口令猜测", "密码喷洒"},
	},
	{
		ID: "dos", Label: "拒绝服务/压力测试",
		Hint:  "改为单次或低频的健壮性观察（超大参数、畸形输入、异常长度的处理），不做并发轰炸",
		Words: []string{"hping3", "slowloris", "slowhttptest", "goldeneye", "ddos", "syn flood", "flood attack", "ab -n", "hey -n", "压测", "压力测试", "拒绝服务", "洪水攻击", "打瘫"},
	},
	{
		ID: "social_engineering", Label: "社工/钓鱼",
		Hint:  "改为技术面等价验证：账号枚举、找回流程逻辑缺陷、凭据泄露面（不接触真人）",
		Words: []string{"phishing", "spearphishing", "vishing", "smishing", "pretext", "钓鱼", "社工", "鱼叉", "水坑", "伪装客服", "短信轰炸"},
	},
	{
		ID: "webshell", Label: "WebShell 落地",
		Hint:  "改为证明「可写入且可执行」的最小只读证据（上传点回显、路径穿越读），不落地任何可执行的持久文件",
		Words: []string{"webshell", "web shell", "behinder", "godzilla", "antsword", "memshell", "eval($_post", "eval($_get", "system($_get", "冰蝎", "哥斯拉", "蚁剑", "菜刀", "内存马", "一句话木马"},
	},
	{
		ID: "reverse_shell", Label: "反弹/正向 Shell",
		Hint:  "改为回显式或无副作用验证：命令执行证据用 sleep/echo/时间差证明，不留通道、不建会话",
		Words: []string{"reverse shell", "bind shell", "msfvenom", "/dev/tcp/", "nc -e", "ncat -e", "socat exec", "bash -i >&", "反弹", "正向shell"},
	},
	{
		ID: "persistence", Label: "持久化/后门驻留",
		Hint:  "SRC 场景一律不需要驻留：证明「可写且有权限」即可，不写 crontab、不改 authorized_keys、不建计划任务",
		Words: []string{"crontab -e", "systemd unit", "authorized_keys", "schtasks /create", "runonce", "持久化", "后门账号", "开机自启", "计划任务驻留"},
	},
}

// WriteItem 是一条「禁止的写操作」词表项。
type WriteItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
	// Words 是关键词（ASCII 词边界 / 中文子串）。
	Words []string `json:"words"`
	// Patterns 是必须是正则才表达得准的片段（形状化，避免误伤）。
	Patterns []string `json:"patterns"`
}

// WriteItems 是封闭的敏感写操作词表。
//
// 刻意【不含裸 POST】：POST 是大量只读接口（GraphQL、搜索、登录）的传输方式，
// 收进来会大面积误伤；POST 只在路径命中 http_mutation_endpoint 时才拦。
var WriteItems = []WriteItem{
	{
		ID: "http_delete", Label: "HTTP 删除请求",
		Hint: "改为只读验证删除接口的鉴权/CSRF（不带实际删除动作）：构造请求但不发送，或只验证未授权时是否被拒",
		Patterns: []string{
			`-X\s*DELETE`, `--request\s+DELETE`, `requests\.delete\s*\(`, `httpx\.delete\s*\(`, `axios\.delete\s*\(`, `method\s*[:=]\s*['"]DELETE['"]`,
		},
		Words: []string{"删除接口", "删除请求"},
	},
	{
		ID: "http_write_method", Label: "HTTP 写方法（PUT/PATCH）",
		Hint: "改为只读面验证：越权/逻辑缺陷用 GET 或预检请求证明，确认「会被拒绝」即可，不真正提交变更",
		Patterns: []string{
			`-X\s*(?:PUT|PATCH)\b`, `--request\s+(?:PUT|PATCH)\b`, `requests\.(?:put|patch)\s*\(`, `axios\.(?:put|patch)\s*\(`, `method\s*[:=]\s*['"](?:PUT|PATCH)['"]`,
		},
	},
	{
		ID: "mutation_endpoint", Label: "变更类接口路径",
		Hint: "改为只读判断：只确认该接口存在与鉴权要求（401/403/200 的行为差异），不触发实际变更",
		Patterns: []string{
			`/(?:create|add|new|update|edit|modify|save|submit|import|upload|delete|remove|destroy|reset|disable|enable|activate|bind|unbind|assign|approve|reject|refund|withdraw|pay|transfer|cancel)(?:[/?#"'\s]|$)`,
		},
	},
	{
		// 只认【真的在执行写】的两种形状，而不是"命令里出现了 SQL 关键字"：
		//   ① 通过数据库客户端跑写语句（mysql/psql/... + 写动词）；
		//   ② 把写语句当 HTTP 请求体提交（-d/--data 后紧跟带 SQL 写动词的引号串，即注入写）。
		// 这样 `grep -n "DELETE FROM" readme.md` 这类"读代码/搜关键字"不会被误拦——读源码
		// 本身是正当侦察。代价：拼装得极隐晦的写语句可能漏过命令层，由意图层与人工复核兜。
		ID: "db_write", Label: "数据库写操作",
		Hint: "改为只读验证注入：用 SELECT/时间差/报错证明可控，不执行 INSERT/UPDATE/DELETE/DROP/TRUNCATE",
		Patterns: []string{
			`(?i)\b(?:mysql|mariadb|psql|sqlite3|mongosh|redis-cli)\b[^\n;&|]{0,120}\b(?:insert|replace|update|delete|drop|truncate|alter|grant|revoke)\b`,
			`(?i)(?:--data(?:-raw|-binary|-urlencode)?|(?:^|\s)-d)\s*['"][^\n]{0,300}\b(?:insert\s+into|update\s+\w+\s+set|delete\s+from|drop\s+(?:table|database)|truncate\s+table)\b`,
		},
		Words: []string{"写库", "改数据", "删数据", "更新数据"},
	},
	{
		ID: "db_dump", Label: "脱库/全量取数",
		Hint: "改为最小证据：只取一行/一个字段证明可控（如 version()、当前用户），不做全库导出",
		Patterns: []string{
			`--dump-all`, `\bsqlmap\b[^\n]*--dump\b`, `\bmysqldump\b`, `\bpg_dump\b`, `\bmongodump\b`, `(?i)\bINTO\s+OUTFILE\b`, `(?i)\bLOAD_FILE\s*\(`,
		},
		Words: []string{"脱库", "拖库", "全量导出", "导出整库"},
	},
}

var (
	techniqueByID = indexTechniques()
	writeItemByID = indexWriteItems()
)

func indexTechniques() map[string]Technique {
	m := make(map[string]Technique, len(Techniques))
	for _, t := range Techniques {
		m[t.ID] = t
	}
	return m
}

func indexWriteItems() map[string]WriteItem {
	m := make(map[string]WriteItem, len(WriteItems))
	for _, w := range WriteItems {
		m[w.ID] = w
	}
	return m
}

// Technique returns the vocabulary entry for id (ok=false when unknown).
func TechniqueByID(id string) (Technique, bool) {
	t, ok := techniqueByID[id]
	return t, ok
}

// WriteItemByID returns the write-vocabulary entry for id.
func WriteItemByID(id string) (WriteItem, bool) {
	w, ok := writeItemByID[id]
	return w, ok
}

// asciiWord builds the word-boundary matcher for a pure-ASCII keyword.
//
// 边界字符集刻意**包含 `.`**（比 P18 的 cliToolLexicon 更严）：`hydra.example.com`
// 是合法目标域名，不该因为含 "hydra" 就被判成爆破；而 `hydra -l admin` 照常命中。
// 同理 `nc`/`since`、`update`/`readme` 这类子串误伤也被挡住。
func asciiWordPattern(word string) string {
	return `(?i)(?:^|[^a-z0-9_.-])` + regexp.QuoteMeta(word) + `(?:[^a-z0-9_.-]|$)`
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// Hit 是一次词表命中。
type Hit struct {
	// Matched 是命中的词（原文片段），Where 是它在文本里的字节偏移。
	Matched string
	Where   int
}

// MatchWords 在 text 里找 words 的第一个命中（按出现位置最早；同位置取词表序）。
// ASCII 词走词边界正则，含中文的词走子串。返回 ok=false 表示没命中。
func MatchWords(text string, words []string) (Hit, bool) {
	best := Hit{Where: -1}
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if isASCII(w) {
			re, err := regexp.Compile(asciiWordPattern(w))
			if err != nil {
				continue
			}
			loc := re.FindStringIndex(text)
			if loc == nil {
				continue
			}
			// 命中下标可能落在边界字符上，向后找词本身的位置。
			at := strings.Index(strings.ToLower(text[loc[0]:loc[1]]), strings.ToLower(w))
			if at >= 0 {
				at += loc[0]
			} else {
				at = loc[0]
			}
			if best.Where < 0 || at < best.Where {
				best = Hit{Matched: w, Where: at}
			}
			continue
		}
		if at := strings.Index(strings.ToLower(text), strings.ToLower(w)); at >= 0 {
			if best.Where < 0 || at < best.Where {
				best = Hit{Matched: w, Where: at}
			}
		}
	}
	return best, best.Where >= 0
}

// negationPrefixes 是"这句话在否定这个手段"的前缀词（只用于语义层，命令层不启用）。
var negationPrefixes = []string{
	"禁止", "严禁", "不得", "不要", "不会", "避免", "不做", "不进行", "不发起", "不尝试",
	// "是否有/有没有 X 防护" 是典型的合规方向（评估防护是否存在），不是要发起 X。
	"是否有", "有没有", "是否存在", "能否防",
}

// Negated 判断命中点是否处于否定语境：命中词之前 negLookback 个字符内出现否定词。
// 只用来削语义层的误伤（"检查登录是否有爆破防护"这类合规方向），
// 真正的"不许发生"由命令层规则保证——所以这里可以宽松。
func Negated(text string, at int) bool {
	const negLookback = 12
	if at < 0 || at > len(text) {
		return false
	}
	start := at - negLookback*3 // 中文一个字 3 字节，向前多看一点
	if start < 0 {
		start = 0
	}
	head := text[start:at]
	for _, p := range negationPrefixes {
		if strings.Contains(head, p) {
			return true
		}
	}
	return false
}
