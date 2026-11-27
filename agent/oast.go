package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// NucleiOASTServer 是 nuclei 使用的 interactsh 公共服务器（默认 oast.fun）。
// 它只用于 nuclei 自身的 OAST 判定：BLADE 不轮询该服务的回调，命中以 nuclei 输出为准。
var NucleiOASTServer = func() string { return "oast.fun" }

// OASTInteractshServers 是 requestrepo 不可用时的回退服务器列表（按顺序尝试）。
var OASTInteractshServers = func() []string {
	return []string{"oast.fun", "oast.pro", "oast.live", "oast.site", "oast.online"}
}

// OASTProxyFunc optionally returns the non-MITM egress proxy for OAST control
// requests (register / poll) so control calls do not reveal the host IP when a
// global proxy is active and never pass the OAST JWT through the traffic recorder.
var OASTProxyFunc func() string

var (
	oastSessionMu   sync.Mutex
	oastSessions    = map[int64][]*interactshSession{}
	oastRRDownUntil time.Time

	oastTokenMu    sync.Mutex
	oastTaskTokens = map[string]map[string]string{} // scopeKey -> token -> local label
	oastFileSaveMu sync.Mutex
)

func oastEnabled() bool {
	if OASTConfig == nil {
		return false
	}
	_, _, ok := OASTConfig()
	return ok
}

// interactshSessionFor 返回本任务的 interactsh 会话；没有就按服务器列表注册一个。
// 注册失败逐个换服务器；全部失败才算不可用。
func (t *ToolSet) interactshSessionFor(ctx context.Context) (*interactshSession, error) {
	oastSessionMu.Lock()
	if list := oastSessions[t.taskID]; len(list) > 0 {
		last := list[len(list)-1]
		oastSessionMu.Unlock()
		return last, nil
	}
	oastSessionMu.Unlock()
	var lastErr error
	for _, server := range OASTInteractshServers() {
		s, err := newInteractshSession(ctx, server)
		if err != nil {
			lastErr = err
			continue
		}
		oastSessionMu.Lock()
		oastSessions[t.taskID] = append(oastSessions[t.taskID], s)
		oastSessionMu.Unlock()
		return s, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有配置 interactsh 服务器")
	}
	return nil, lastErr
}

func (t *ToolSet) pollInteractsh(ctx context.Context) ([]oastRequest, error) {
	oastSessionMu.Lock()
	list := append([]*interactshSession(nil), oastSessions[t.taskID]...)
	oastSessionMu.Unlock()
	var out []oastRequest
	var lastErr error
	for _, s := range list {
		items, err := s.Poll(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, items...)
	}
	return out, lastErr
}

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

// oastMarker 是旧版任务的回调兼容前缀（仅用于存量任务迁移期匹配；新签发不再放入 URL）。
func (t *ToolSet) oastMarker() string {
	if t.taskID <= 0 {
		return "cbtask"
	}
	return fmt.Sprintf("cb%d", t.taskID)
}

func (t *ToolSet) oastScopeKey() string {
	expID := int64(0)
	if t != nil && t.ts != nil {
		expID = t.ts.ID()
	}
	return fmt.Sprintf("%d:%d", t.taskID, expID)
}

func (t *ToolSet) oastTokensFile() string {
	if t == nil {
		return ""
	}
	if root := strings.TrimSpace(t.taskRoot); root != "" {
		return filepath.Join(root, "shared", "oast_tokens.json")
	}
	if rd := strings.TrimSpace(t.runDir); rd != "" {
		return filepath.Join(rd, "oast_tokens.json")
	}
	return ""
}

func randomOASTToken() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	// First char always a letter so tokens work uniformly in URL paths & DNS labels.
	b := randomToken(1)
	if b != "" {
		// Ensure alpha start
		idx := int(b[0]) % len(letters)
		return string(letters[idx]) + randomToken(9)
	}
	return "a" + randomToken(9)
}

func (t *ToolSet) mintOASTToken(label string) string {
	key := t.oastScopeKey()
	tok := randomOASTToken()

	oastTokenMu.Lock()
	if len(oastTaskTokens) > 512 {
		clear(oastTaskTokens)
	}
	if oastTaskTokens[key] == nil {
		oastTaskTokens[key] = map[string]string{}
	}
	for oastTaskTokens[key][tok] != "" {
		tok = randomOASTToken()
	}
	oastTaskTokens[key][tok] = label
	oastTokenMu.Unlock()

	if path := t.oastTokensFile(); path != "" {
		oastFileSaveMu.Lock()
		merged := readOASTTokensFile(path)
		merged[tok] = label
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if raw, err := json.Marshal(merged); err == nil {
				tmp := path + ".tmp"
				if os.WriteFile(tmp, raw, 0o600) == nil {
					_ = os.Rename(tmp, path)
				}
			}
		}
		oastFileSaveMu.Unlock()
	}
	return tok
}

func (t *ToolSet) lookupOASTTokens() map[string]string {
	out := map[string]string{}
	if path := t.oastTokensFile(); path != "" {
		oastFileSaveMu.Lock()
		for k, v := range readOASTTokensFile(path) {
			out[k] = v
		}
		oastFileSaveMu.Unlock()
	}
	oastTokenMu.Lock()
	for k, v := range oastTaskTokens[t.oastScopeKey()] {
		if _, exists := out[k]; !exists || v != "" {
			out[k] = v
		}
	}
	oastTokenMu.Unlock()
	return out
}

func readOASTTokensFile(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// oastMint 生成一个带不透明随机令牌的回调地址（不暴露任务 ID 或测试意图关键词）。
func (t *ToolSet) oastMint() actool.CoreTool {
	return readTool("oast_mint",
		"生成一个**唯一不透明回调地址**（requestrepo / 自定义 OAST 服务，平台不开入站端口），用于确认盲打类漏洞："+
			"SSRF 打过去、命令注入里 curl/wget 它、盲 XSS 的 src 指向它、反序列化里发起请求……目标真的执行了，"+
			"回调就会被记录。\n"+
			"★ 外发 URL 仅包含随机令牌，不泄露任务编号或漏洞标签；命中后用 oast_poll 取记录并自动对照原标签。\n"+
			"★ 提示：公共 oast.fun 等域易被企业 WAF/DNS 防火墙阻断；高敏目标建议在设置中配置私有 OAST 域名。\n"+
			"★ 回调只能证明「目标访问了这个地址」，不能证明是哪段代码执行的——结论里如实写。",
		obj(map[string]any{
			"label": str("可选：本地语义标签（如 ssrf-admin、rce-ping），仅存于本地对照命中来源，不会拼入外发 URL"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !oastEnabled() {
				return actool.Errorf("oast_mint: 未配置 OAST——请在设置里打开 OAST 开关"), nil
			}
			var a struct {
				Label string `json:"label"`
			}
			_ = json.Unmarshal(in, &a)
			label := sanitizeOASTLabel(a.Label)
			tok := t.mintOASTToken(label)
			localNote := ""
			if label != "" {
				localNote = fmt.Sprintf("（本地标签: %s，不出现在外发 URL 中）", label)
			}
			token, domain, rrOK := oastCfg()
			// requestrepo 可用且近 5 分钟没失败过 → 优先它；否则回退 interactsh。
			if rrOK && time.Now().After(oastRRDownUntil) {
				if sub := oastSubdomain(token); sub != "" {
					callback := fmt.Sprintf("http://%s.%s/%s", sub, domain, tok)
					return actool.Text(fmt.Sprintf("callback url: %s（requestrepo）%s\n把它放进 payload；目标执行后调 oast_poll 取命中记录。", callback, localNote)), nil
				}
			}
			session, err := t.interactshSessionFor(ctx)
			if err != nil {
				return actool.Errorf("oast_mint: requestrepo 不可用，interactsh 回退也失败：" + err.Error()), nil
			}
			callback := fmt.Sprintf("http://%s/%s", session.URL(), tok)
			return actool.Text(fmt.Sprintf("callback url: %s（interactsh %s）%s\n把它放进 payload；目标执行后调 oast_poll 取命中记录。", callback, session.Server, localNote)), nil
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
			if !oastEnabled() {
				return actool.Errorf("oast_poll: 未配置 OAST——请在设置里打开 OAST 开关"), nil
			}
			var items []oastRequest
			var rrErr error
			if token, _, ok := oastCfg(); ok && time.Now().After(oastRRDownUntil) {
				got, ferr := fetchOASTRequests(ctx, token)
				if ferr != nil {
					oastRRDownUntil = time.Now().Add(5 * time.Minute)
					rrErr = ferr
				} else {
					items = append(items, got...)
				}
			}
			ish, ierr := t.pollInteractsh(ctx)
			items = append(items, ish...)
			if len(items) == 0 && rrErr != nil && ierr != nil {
				return actool.Errorf("oast_poll: requestrepo 与 interactsh 都不可用：requestrepo=" + rrErr.Error() + "; interactsh=" + ierr.Error()), nil
			}
			marker := t.oastMarker()
			tokens := t.lookupOASTTokens()
			known := t.knownOASTRequestIDs()
			fresh := make([]map[string]any, 0, 4)
			for _, it := range items {
				if it.ID == "" || known[it.ID] {
					continue
				}
				matched, label := oastMatchesRequest(it, marker, tokens)
				if !matched {
					continue
				}
				known[it.ID] = true
				summary := oastHitSummary(it, label)
				fact := map[string]any{
					"summary":         summary,
					"oast_request_id": it.ID,
					"confidence":      "observed",
				}
				if label != "" {
					fact["label"] = label
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
				row := map[string]any{
					"fact_id": id, "summary": fact["summary"], "ip": it.IP, "country": it.Country,
					"path": it.Path, "url": it.URL, "method": it.Method,
				}
				if label != "" {
					row["label"] = label
				}
				fresh = append(fresh, row)
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

func newOASTControlClient(timeout time.Duration) *http.Client {
	proxyAddr := ""
	if OASTProxyFunc != nil {
		proxyAddr = strings.TrimSpace(OASTProxyFunc())
	}
	if c, err := newProxyAwareHTTPClient(proxyAddr, timeout); err == nil && c != nil {
		return c
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: timeout}
}

func applyOASTControlHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", DefaultOutboundUserAgent())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
}

// fetchOASTRequests 取最近的记录（一次 50 条足够：更早的命中早已被轮询记下）。
func fetchOASTRequests(ctx context.Context, token string) ([]oastRequest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OASTBaseURL+"/api/v2/requests?limit=50", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	applyOASTControlHeaders(req)
	client := newOASTControlClient(oastHTTPTimeout)
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

func isOASTAlnumByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func oastTokenMatches(it oastRequest, token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return false
	}
	for _, v := range []string{it.Path, it.Query, it.URL, it.Domain} {
		lower := strings.ToLower(v)
		if lower == "" {
			continue
		}
		for offset := 0; offset < len(lower); {
			i := strings.Index(lower[offset:], token)
			if i < 0 {
				break
			}
			start := offset + i
			end := start + len(token)
			leftOK := start == 0 || !isOASTAlnumByte(lower[start-1])
			rightOK := end >= len(lower) || !isOASTAlnumByte(lower[end])
			if leftOK && rightOK {
				return true
			}
			offset = end
		}
	}
	return false
}

func oastMatchesRequest(it oastRequest, legacyMarker string, tokens map[string]string) (bool, string) {
	for tok, lbl := range tokens {
		if oastTokenMatches(it, tok) {
			return true, lbl
		}
	}
	if oastMatches(it, legacyMarker) {
		return true, ""
	}
	return false, ""
}

// oastMatches 判断一条记录是不是本任务的回调（兼容旧版 cb<taskID> 标记）。
func oastMatches(it oastRequest, marker string) bool {
	marker = strings.ToLower(strings.TrimSpace(marker))
	if marker == "" {
		return false
	}
	for _, v := range []string{it.Path, it.Query, it.URL, it.Domain} {
		lower := strings.ToLower(v)
		if lower == "" {
			continue
		}
		// 任务标记以数字结尾（cb9 / cb12）：必须要求标记后面不是数字，
		// 否则任务 9 会把任务 99 的回调（/cb999-other）也算成自己的。
		for offset := 0; offset < len(lower); {
			i := strings.Index(lower[offset:], marker)
			if i < 0 {
				break
			}
			end := offset + i + len(marker)
			if end >= len(lower) || lower[end] < '0' || lower[end] > '9' {
				return true
			}
			offset = end
		}
	}
	return false
}

func oastHitSummary(it oastRequest, label string) string {
	tagNote := ""
	if strings.TrimSpace(label) != "" {
		tagNote = fmt.Sprintf(" [tag: %s]", label)
	}
	if it.Type == "dns" {
		return fmt.Sprintf("OAST 回连（DNS）%s：%s 解析请求，来自 %s（%s）", tagNote, it.Domain, it.IP, it.Country)
	}
	target := it.Path
	if it.Query != "" {
		target += "?" + it.Query
	}
	if target == "" {
		target = it.URL
	}
	return fmt.Sprintf("OAST 回连（%s %s）%s：来自 %s（%s）", strings.ToUpper(nzStr(it.Method, "GET")), target, tagNote, it.IP, it.Country)
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
