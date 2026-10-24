package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// OAST（带外应用安全测试）：盲打类漏洞（SSRF、命令注入、盲 XSS、反序列化）在目标侧
// 没有任何直接回显，唯一可靠的确认方式是**让它回连一个我们控制的地址**。
//
// 这里接的是 requestrepo（外部托管，BLADE 不需要开入站端口——部署姿态不变）：
// 平台只做三件事：按任务生成带标记的回调地址、去服务端取记录、把命中记成事实。
//
// 配置由 server 从设置注入：token 是 requestrepo 的会话 token（含 subdomain），
// 只在 HTTP 头里出现，绝不进工具输出/台账/提示词。

// OASTConfig 返回 (token, domain, enabled)。nil 或 enabled=false 时两个工具不可用。
var OASTConfig func() (token, domain string, enabled bool)

// OASTBaseURL 是 requestrepo 的 API 入口。变量而非常量：测试用 httptest 覆盖它。
var OASTBaseURL = "https://requestrepo.com"

const oastHTTPTimeout = 12 * time.Second

func oastCfg() (string, string, bool) {
	if OASTConfig == nil {
		return "", "", false
	}
	token, domain, ok := OASTConfig()
	if !ok || strings.TrimSpace(token) == "" {
		return "", "", false
	}
	if strings.TrimSpace(domain) == "" {
		domain = "requestrepo.com"
	}
	return strings.TrimSpace(token), strings.TrimSpace(domain), true
}

// oastMarker 是本任务的回调标记。带任务 id：同一账号下的多个任务共用一个子域，
// 只有靠标记才分得清哪条回调是哪个任务的。
func (t *ToolSet) oastMarker() string {
	if t.taskID <= 0 {
		return "bladetask"
	}
	return fmt.Sprintf("blade%d", t.taskID)
}

// oastMint 生成一个带任务标记的回调地址。
func (t *ToolSet) oastMint() actool.CoreTool {
	return readTool("oast_mint",
		"生成一个**唯一回调地址**（requestrepo，外部托管，平台不开入站端口），用于确认盲打类漏洞："+
			"SSRF 打过去、命令注入里 curl/wget 它、盲 XSS 的 src 指向它、反序列化里发起请求……目标真的执行了，"+
			"回调就会被记录。\n"+
			"★ 地址里带本任务标记；命中后用 oast_poll 取记录（会自动记成一条事实）。\n"+
			"★ 同一地址可重复使用；想要分辨是哪条 payload 命中的，用 label 区分。\n"+
			"★ 回调只能证明「目标访问了这个地址」，不能证明是哪段代码执行的——结论里如实写。",
		obj(map[string]any{
			"label": str("可选：给这次回调起个短标签（如 ssrf-admin、rce-ping），便于分辨是哪条 payload 命中的"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			token, domain, ok := oastCfg()
			if !ok {
				return actool.Errorf("oast_mint: 未配置 OAST——请在设置里填入 requestrepo 会话 token 并打开 OAST 开关"), nil
			}
			sub := oastSubdomain(token)
			if sub == "" {
				return actool.Errorf("oast_mint: token 里解析不出子域——请确认填的是 requestrepo 的会话 token"), nil
			}
			var a struct {
				Label string `json:"label"`
			}
			_ = json.Unmarshal(in, &a)
			marker := t.oastMarker()
			if label := sanitizeOASTLabel(a.Label); label != "" {
				marker += "-" + label
			}
			callback := fmt.Sprintf("http://%s.%s/%s", sub, domain, marker)
			return actool.Text(fmt.Sprintf("callback url: %s\n把它放进 payload；目标执行后调 oast_poll 取命中记录（按请求 id 去重，可重复轮询）。", callback)), nil
		})
}

// oastPoll 拉取回调记录，把**新命中**的记成事实并返回。
func (t *ToolSet) oastPoll() actool.CoreTool {
	return t.writeExpTool("oast_poll",
		"取本任务的 OAST 回调记录：新命中的请求会记成一条事实（按请求 id 去重）并返回。\n"+
			"★ 记录里带 方法/路径/来源 IP/国家/时间——这就是「目标真的执行了」的证据。\n"+
			"★ 只证明访问，不证明执行路径；结论里如实写「目标回连了 X」，别写成「确认 RCE」。",
		obj(map[string]any{}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			token, _, ok := oastCfg()
			if !ok {
				return actool.Errorf("oast_poll: 未配置 OAST——请在设置里填入 requestrepo 会话 token 并打开 OAST 开关"), nil
			}
			items, err := fetchOASTRequests(ctx, token)
			if err != nil {
				return actool.Errorf("oast_poll: 拉取回调记录失败：" + err.Error()), nil
			}
			marker := t.oastMarker()
			known := t.knownOASTRequestIDs()
			fresh := make([]map[string]any, 0, 4)
			for _, it := range items {
				if it.ID == "" || known[it.ID] || !oastMatches(it, marker) {
					continue
				}
				known[it.ID] = true
				fact := map[string]any{
					"summary":         oastHitSummary(it),
					"oast_request_id": it.ID,
					"confidence":      "observed",
				}
				if it.Type != "" {
					fact["kind"] = it.Type
				}
				for k, v := range map[string]string{
					"method": it.Method, "path": it.Path, "url": it.URL, "domain": it.Domain,
					"ip": it.IP, "country": it.Country, "query": it.Query,
				} {
					if strings.TrimSpace(v) != "" {
						fact[k] = v
					}
				}
				if it.Date > 0 {
					fact["at"] = time.Unix(it.Date, 0).Format(time.RFC3339)
				}
				id, err := t.ts.AddNode(db.KindFact, fact, 0, "confirmed", t.worker, nil)
				if err != nil {
					continue
				}
				t.writes.Facts++
				fresh = append(fresh, map[string]any{
					"fact_id": id, "summary": fact["summary"], "ip": it.IP, "country": it.Country,
					"path": it.Path, "url": it.URL, "method": it.Method,
				})
			}
			if len(fresh) == 0 {
				return actool.Text(fmt.Sprintf("no new callbacks（已记录 %d 条，可稍后再轮询）", len(known))), nil
			}
			raw, _ := json.Marshal(fresh)
			return actool.Text(fmt.Sprintf("callbacks: %d\n%s\n以上已作为事实登记（confidence=observed）：只证明目标回连了该地址，结论里如实写。", len(fresh), raw)), nil
		})
}

type oastRequest struct {
	ID      string `json:"_id"`
	Type    string `json:"type"`
	Date    int64  `json:"date"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Query   string `json:"query"`
	URL     string `json:"url"`
	Domain  string `json:"domain"`
	IP      string `json:"ip"`
	Country string `json:"country"`
}

// fetchOASTRequests 取最近的记录（一次 50 条足够：更早的命中早已被轮询记下）。
func fetchOASTRequests(ctx context.Context, token string) ([]oastRequest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OASTBaseURL+"/api/v2/requests?limit=50", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: oastHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Requests []oastRequest `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Requests, nil
}

// oastMatches 判断一条记录是不是本任务的回调（HTTP 看 path/query/url，DNS 看域名）。
func oastMatches(it oastRequest, marker string) bool {
	marker = strings.ToLower(marker)
	for _, v := range []string{it.Path, it.Query, it.URL, it.Domain} {
		if v != "" && strings.Contains(strings.ToLower(v), marker) {
			return true
		}
	}
	return false
}

func oastHitSummary(it oastRequest) string {
	if it.Type == "dns" {
		return fmt.Sprintf("OAST 回连（DNS）：%s 解析请求，来自 %s（%s）", it.Domain, it.IP, it.Country)
	}
	target := it.Path
	if it.Query != "" {
		target += "?" + it.Query
	}
	if target == "" {
		target = it.URL
	}
	return fmt.Sprintf("OAST 回连（%s %s）：来自 %s（%s）", strings.ToUpper(nzStr(it.Method, "GET")), target, it.IP, it.Country)
}

// knownOASTRequestIDs 已登记的回调请求 id（去重：同一次回调不重复记事实）。
func (t *ToolSet) knownOASTRequestIDs() map[string]bool {
	out := map[string]bool{}
	facts, err := t.ts.ListByKind(db.KindFact, 500)
	if err != nil {
		return out
	}
	for _, n := range facts {
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		if id := payloadString(p, "oast_request_id"); id != "" {
			out[id] = true
		}
	}
	return out
}

// oastSubdomain 从 requestrepo 的会话 token（JWT）里取子域。**只解码不验签**：
// 验签由服务端在 API 调用时做，这里只是为了拼出回调地址。
func oastSubdomain(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Subdomain string `json:"subdomain"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	return strings.TrimSpace(claims.Subdomain)
}

// sanitizeOASTLabel 只保留回调用得到的字符：小写字母、数字、短横线；限长。
func sanitizeOASTLabel(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '_' || r == ' ':
			b.WriteRune('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func nzStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
