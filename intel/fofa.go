package intel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FOFA 官方端点。企业版/自建可在设置里改 BaseURL（验收也靠它指向 httptest）。
const fofaDefaultBaseURL = "https://fofa.info"

// FOFA 单次查询的返回条数上限（官方 API 约束：size ≤ 10000，但一次给模型看几百条没意义）。
const fofaMaxSize = 100

// fofaDefaultFields 是默认请求字段（顺序即结果列序）。
var fofaDefaultFields = []string{"host", "ip", "port", "protocol", "domain", "title", "server"}

func init() { register("fofa", func(cfg Config) Provider { return &fofa{cfg: cfg} }) }

type fofa struct {
	cfg Config

	// client 每次查询只建一次：一次查询会连发多个请求（账号信息/翻页），
	// 每个请求都 clone 一份 DefaultTransport 会反复丢连接池与 TLS 会话，
	// 也把空闲连接泄漏到 transport 的 GC 之前。cfg 在 Provider 实例生命周期内不变。
	mu     sync.Mutex
	cached *http.Client
}

func (f *fofa) Name() string { return "fofa" }

func (f *fofa) client() (*http.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cached != nil {
		return f.cached, nil
	}
	cl, err := f.newClient()
	if err != nil {
		return nil, err
	}
	f.cached = cl
	return cl, nil
}

func (f *fofa) newClient() (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // 默认直连：FOFA 是国内站点，回退环境变量只会带来意外
	if p := strings.TrimSpace(f.cfg.Proxy); p != "" {
		pu, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("代理地址不合法：%v", err)
		}
		switch pu.Scheme {
		case "http", "https", "socks5":
		default:
			return nil, fmt.Errorf("代理只支持 http/https/socks5，收到 %q", p)
		}
		transport.Proxy = http.ProxyURL(pu)
	}
	timeout := f.cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func (f *fofa) base() string {
	if b := strings.TrimSpace(f.cfg.BaseURL); b != "" {
		return strings.TrimRight(b, "/")
	}
	return fofaDefaultBaseURL
}

// fofaNorm 是归一化后的应答：官方与常见中转站/自建（字段名与嵌套层不同）都收敛到这一份。
type fofaNorm struct {
	ErrMsg string
	Size   int      // 服务端报告的命中总数（不是本次返回条数）
	Fields []string // 列名，顺序即行内单元格顺序
	Rows   [][]string
}

// normalizeFofaResp 容忍三种常见结构差异，**明确列出支持什么**而不是"尽力猜"：
//  1. 结果容器位置：顶层 results ／ data.results ／ result.results；
//  2. 行的形状：数组（按 fields 对齐，官方形态）／对象（键序即列序，部分中转站）；
//  3. 计数键名：size ／ total（部分中转站只给 total）；
//  4. 错误表示：error=true ／ success=false ／ code != 0，错误文案取 errmsg/message/msg。
//
// 解析不出来就返回错误并附应答片段——**不返回"空结果"**：把结构不认识当成"没查到"
// 会让人以为目标不存在，而真相是这次解析失败。
func normalizeFofaResp(body []byte) (*fofaNorm, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("FOFA 应答不是合法 JSON（%v）：%s", err, clipBody(body))
	}
	if msg := fofaErrorOf(root); msg != "" {
		return nil, fofaError(msg)
	}
	container := root
	for _, key := range []string{"data", "result"} {
		if m, ok := root[key].(map[string]any); ok {
			if _, has := m["results"]; has {
				container = m
				break
			}
		}
	}
	// 区分两种"没行"：**键在但为 null** = 权威的"零命中"（合法应答，返回空结果）；
	// **键不在** = 结构不认识（报错）。把后者当空结果会让人以为目标不存在。
	rawAny, has := container["results"]
	if !has {
		return nil, fmt.Errorf("FOFA 应答里找不到 results 数组（支持顶层 / data / result 三种位置）：%s", clipBody(body))
	}
	rawRows, _ := rawAny.([]any)
	if rawAny != nil && rawRows == nil {
		return nil, fmt.Errorf("FOFA 应答的 results 不是数组（实际是 %T）：%s", rawAny, clipBody(body))
	}
	fields := stringList(container["fields"])
	if len(fields) == 0 {
		fields = stringList(root["fields"])
	}
	out := &fofaNorm{Fields: fields}
	for _, rr := range rawRows {
		switch row := rr.(type) {
		case []any:
			cells := make([]string, 0, len(row))
			for _, c := range row {
				cells = append(cells, cellString(c))
			}
			out.Rows = append(out.Rows, cells)
		case map[string]any:
			if len(out.Fields) == 0 {
				out.Fields = keyOrder(row)
			}
			cells := make([]string, 0, len(out.Fields))
			for _, f := range out.Fields {
				cells = append(cells, cellString(row[f]))
			}
			out.Rows = append(out.Rows, cells)
		default:
			out.Rows = append(out.Rows, []string{cellString(rr)})
		}
	}
	out.Size = intOf(container["size"], intOf(container["total"], intOf(root["size"], intOf(root["total"], len(out.Rows)))))
	return out, nil
}

// fofaErrorOf 识别三种错误表示，返回面向用户的文案（空串 = 不是错误）。
func fofaErrorOf(m map[string]any) string {
	if b, ok := m["error"].(bool); ok && b {
		return firstString(m, "errmsg", "message", "msg")
	}
	if b, ok := m["success"].(bool); ok && !b {
		return firstString(m, "errmsg", "message", "msg")
	}
	if code, ok := m["code"]; ok {
		if n, ok := numOf(code); ok && n != 0 {
			return firstString(m, "errmsg", "message", "msg")
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return "FOFA 返回了错误（未给出 errmsg）"
}

func stringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// keyOrder 按固定顺序列出对象键：encoding/json 解成 map 后已丢原始顺序，这里按字典序
// 给出**稳定**的列序（测试与前端都依赖稳定），并在文档里写明这一点。
func keyOrder(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == math.Trunc(x) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(b)
	}
}

func numOf(v any) (int, bool) {
	switch x := v.(type) {
	case float64: // JSON 解出来的数字
		return int(x), true
	case int: // 内部嵌套调用传回来的原生 int（漏了它会让「内层算出的值」被当成解析不了）
		return x, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	default:
		return 0, false
	}
}

// intOf 返回第一个能解析成整数的候选值。
func intOf(vs ...any) int {
	for _, v := range vs {
		if n, ok := numOf(v); ok {
			return n
		}
	}
	return 0
}

func (f *fofa) Search(ctx context.Context, q Query) (Result, error) {
	if strings.TrimSpace(f.cfg.Key) == "" {
		return Result{}, fmt.Errorf("未配置 FOFA API Key：请在 系统设置 → 情报源 里填入（平台不会代你申请 key）")
	}
	expr := strings.TrimSpace(q.Expr)
	if expr == "" {
		return Result{}, fmt.Errorf("查询表达式不能为空（FOFA 语法，如 domain=\"example.com\" && protocol=\"http\"）")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > fofaMaxSize {
		limit = fofaMaxSize
	}
	fields := q.Fields
	if len(fields) == 0 {
		fields = fofaDefaultFields
	}

	vals := url.Values{}
	vals.Set("key", f.cfg.Key)
	if e := strings.TrimSpace(f.cfg.Email); e != "" {
		vals.Set("email", e) // 老账号需要；新账号留空即可
	}
	vals.Set("qbase64", base64.StdEncoding.EncodeToString([]byte(expr)))
	vals.Set("fields", strings.Join(fields, ","))
	vals.Set("size", strconv.Itoa(limit))
	vals.Set("page", "1")

	body, err := f.get(ctx, "/api/v1/search/all?"+vals.Encode())
	if err != nil {
		return Result{}, err
	}
	norm, err := normalizeFofaResp(body)
	if err != nil {
		return Result{}, err
	}
	// 列名以**应答给的为准**（拿 fields 回来对账）：中转站可能少给或多给列，
	// 用我们请求的顺序硬套会把值错位到别的列上。
	cols := fields
	if len(norm.Fields) > 0 {
		cols = norm.Fields
	}
	res := Result{Provider: "fofa", Expr: expr, Fields: cols, Total: norm.Size, QueriedAt: time.Now()}
	for _, row := range norm.Rows {
		res.Assets = append(res.Assets, assetFromRow(cols, row))
	}
	return res, nil
}

// Info 打 /api/v1/info/my：设置页「测试连接」用它显示账号与剩余额度。
func (f *fofa) Info(ctx context.Context) (map[string]any, error) {
	if strings.TrimSpace(f.cfg.Key) == "" {
		return nil, fmt.Errorf("未配置 FOFA API Key")
	}
	vals := url.Values{}
	vals.Set("key", f.cfg.Key)
	body, err := f.get(ctx, "/api/v1/info/my?"+vals.Encode())
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("FOFA 应答不是合法 JSON（%v）：%s", err, clipBody(body))
	}
	if e, _ := m["error"].(bool); e {
		msg, _ := m["errmsg"].(string)
		return nil, fofaError(msg)
	}
	return m, nil
}

func (f *fofa) get(ctx context.Context, pathAndQuery string) ([]byte, error) {
	cl, err := f.client()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.base()+pathAndQuery, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 FOFA（%v）：检查网络/代理设置", err)
	}
	defer resp.Body.Close()
	// 上限 8MB：测绘结果再大也不该把内存打爆（超出即当异常处理）。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 FOFA 应答失败：%v", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("FOFA 拒绝访问（HTTP %d）：API Key 可能无效或已过期", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// 有的部署用 4xx + JSON 正文给业务错误（额度/语法），正文比状态码更有用。
		var probe struct {
			Error  bool   `json:"error"`
			ErrMsg string `json:"errmsg"`
		}
		if json.Unmarshal(body, &probe) == nil && probe.Error && strings.TrimSpace(probe.ErrMsg) != "" {
			return nil, fofaError(probe.ErrMsg)
		}
		return nil, fmt.Errorf("FOFA 返回 HTTP %d：%s", resp.StatusCode, clipBody(body))
	}
	return body, nil
}

// fofaError 把 FOFA 的 errmsg 翻译成"知道下一步干什么"的话术。
func fofaError(msg string) error {
	m := strings.TrimSpace(msg)
	low := strings.ToLower(m)
	switch {
	case strings.Contains(low, "key") || strings.Contains(low, "invalid") || strings.Contains(m, "无效"):
		return fmt.Errorf("FOFA 拒绝了这次查询：%s（检查 系统设置 → 情报源 里的 API Key）", m)
	case strings.Contains(low, "point") || strings.Contains(low, "quota") || strings.Contains(m, "额度") || strings.Contains(m, "不足"):
		return fmt.Errorf("FOFA 额度不足：%s（充值或换 key；平台侧还有每日调用上限）", m)
	case strings.Contains(low, "syntax") || strings.Contains(m, "语法"):
		return fmt.Errorf("FOFA 查询语法有误：%s（表达式用的是 FOFA 原生语法，如 domain=\"example.com\"）", m)
	}
	return fmt.Errorf("FOFA 查询失败：%s", m)
}

// assetFromRow 把一行结果按字段顺序映射成 Asset（顺序即 fields 的声明顺序）。
func assetFromRow(fields, row []string) Asset {
	var a Asset
	for i, f := range fields {
		if i >= len(row) {
			break
		}
		v := strings.TrimSpace(row[i])
		if v == "" {
			continue
		}
		switch strings.ToLower(f) {
		case "host":
			a.Host = v
		case "ip":
			a.IP = v
		case "port":
			if n, err := strconv.Atoi(v); err == nil {
				a.Port = n
			} else {
				a.setExtra(f, v)
			}
		case "protocol":
			a.Protocol = v
		case "domain":
			a.Domain = v
		case "title":
			a.Title = v
		case "server":
			a.Server = v
		case "icp":
			a.ICP = v
		default:
			a.setExtra(f, v)
		}
	}
	return a
}

func (a *Asset) setExtra(k, v string) {
	if a.Extra == nil {
		a.Extra = map[string]string{}
	}
	a.Extra[k] = v
}

// clipBody 把应答正文裁到可读长度（错误信息里带上原文，但别把 body 灌进日志/上下文）。
func clipBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	r := []rune(s)
	if len(r) <= 300 {
		return s
	}
	return string(r[:300]) + "…"
}
