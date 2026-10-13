package intel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FOFA 官方端点。企业版/自建可在设置里改 BaseURL（验收也靠它指向 httptest）。
const fofaDefaultBaseURL = "https://fofa.info"

// FOFA 单次查询的返回条数上限（官方 API 约束：size ≤ 10000，但一次给模型看几百条没意义）。
const fofaMaxSize = 100

// fofaDefaultFields 是默认请求字段（顺序即结果列序）。
var fofaDefaultFields = []string{"host", "ip", "port", "protocol", "domain", "title", "server"}

func init() { register("fofa", func(cfg Config) Provider { return &fofa{cfg: cfg} }) }

type fofa struct{ cfg Config }

func (f *fofa) Name() string { return "fofa" }

func (f *fofa) client() (*http.Client, error) {
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

// fofaResp 是 /api/v1/search/all 的应答。
type fofaResp struct {
	Error    bool       `json:"error"`
	ErrMsg   string     `json:"errmsg"`
	Size     int        `json:"size"`
	Page     int        `json:"page"`
	Results  [][]string `json:"results"`
	Mode     string     `json:"mode"`
	Query    string     `json:"query"`
	Required int        `json:"required_fpoints"`
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
	var resp fofaResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return Result{}, fmt.Errorf("FOFA 应答不是合法 JSON（%v）：%s", err, clipBody(body))
	}
	if resp.Error {
		return Result{}, fofaError(resp.ErrMsg)
	}

	res := Result{Provider: "fofa", Expr: expr, Fields: fields, Total: resp.Size, QueriedAt: time.Now()}
	for _, row := range resp.Results {
		res.Assets = append(res.Assets, assetFromRow(fields, row))
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
