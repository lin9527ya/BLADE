// Package intel 是 ARTEX 的「情报/测绘数据源」抽象：把外部空间测绘引擎（FOFA、将来
// 的 Quake/ZoomEye/Shodan、企业信息收集）统一成一条查询接口，供 agent 工具调用。
//
// 设计纪律：
//   - **只做查询与呈现**：本包不写库、不扩范围。查到的东西要不要进资产库（insert_assets）
//     或纳入任务范围（add_task_scope），由 agent 或人另外决定——情报不等于授权。
//   - **纯逻辑 + HTTP**：不依赖 db/agent/server，可 httptest 表驱动单测。
//   - **错误要可读可自查**：key 无效 / 额度耗尽 / 语法错误 / 网络失败分别给不同的话术，
//     因为调用方是模型与人，两者都需要据此决定"换个写法"还是"去设置页补 key"。
package intel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Asset 是一条测绘结果（跨 provider 的公共形状；provider 特有的字段进 Extra 原样保留）。
type Asset struct {
	Host     string            `json:"host,omitempty"`
	IP       string            `json:"ip,omitempty"`
	Port     int               `json:"port,omitempty"`
	Protocol string            `json:"protocol,omitempty"`
	Domain   string            `json:"domain,omitempty"`
	Title    string            `json:"title,omitempty"`
	Server   string            `json:"server,omitempty"`
	ICP      string            `json:"icp,omitempty"`
	Extra    map[string]string `json:"extra,omitempty"`
}

// Display 给"一行式"用：优先 host，其次 ip:port。
func (a Asset) Display() string {
	if strings.TrimSpace(a.Host) != "" {
		return a.Host
	}
	if a.IP != "" {
		if a.Port > 0 {
			return fmt.Sprintf("%s:%d", a.IP, a.Port)
		}
		return a.IP
	}
	return "(无 host/ip)"
}

// Query 是一次查询请求。Expr 用的是 **provider 原生语法**（FOFA 就是 FOFA 语法），
// 本包不做自然语言翻译——语法是操作者与模型都该直接掌握的东西，翻译层只会引入歧义。
type Query struct {
	Expr   string
	Limit  int
	Fields []string
}

// Result 是一次查询的结果。
type Result struct {
	Provider string   `json:"provider"`
	Expr     string   `json:"expr"`
	Fields   []string `json:"fields"`
	// Total 是服务端报告的总命中数（可能远大于 Assets 长度——我们只取前 Limit 条）。
	Total     int       `json:"total"`
	Assets    []Asset   `json:"assets"`
	QueriedAt time.Time `json:"queried_at"`
}

// Config 是 provider 的运行配置。由服务端从设置**现读**（改 key 立即生效，不需要重建 agent）。
type Config struct {
	Key     string
	Email   string // FOFA 老账号需要；新账号留空
	BaseURL string // 空 = 官方端点（也用于把验收指向 httptest）
	Proxy   string // 独立出口代理（http/https/socks5）；空 = 直连，不回退环境变量
	Timeout time.Duration
}

// Provider 是一家情报源。
type Provider interface {
	Name() string
	// Search 执行一次查询。返回的 error 面向"人/模型可读可自查"：
	// key 无效、额度耗尽、语法错误、网络失败各自成句。
	Search(ctx context.Context, q Query) (Result, error)
	// Info 探活 + 读账号信息（剩余额度等），给设置页「测试连接」用。
	Info(ctx context.Context) (map[string]any, error)
}

// registry 是 provider 构造器注册表：加一家 = 写一个实现 + 在这里注册一行。
var registry = map[string]func(Config) Provider{}

func register(name string, ctor func(Config) Provider) { registry[name] = ctor }

// Names 返回已注册的 provider 名（排序稳定，供工具描述与设置页展示）。
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// New 按名字构造 provider。
func New(name string, cfg Config) (Provider, error) {
	ctor, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("未知情报源 %q（可用：%s）", name, strings.Join(Names(), "、"))
	}
	return ctor(cfg), nil
}
