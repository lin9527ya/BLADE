package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intel"
	actool "github.com/Autumn-27/norma/tool"
)

// P21 情报/测绘工具面：让 agent 自己去外部测绘引擎（先接 FOFA）查目标面资产，
// 而不是靠人搜完再喂给平台。
//
// 三条边界（写在工具描述里，也写在 docs/情报与计划任务.md）：
//   - **只查不扩**：本工具只返回候选，不写资产库、不动任务范围；要用哪条由 agent 显式
//     调 insert_assets / add_task_scope（与人手工喂资产走同一条路）。
//   - **不过 RoE 但也不绕过它**：查 FOFA 是访问第三方 API，不是访问目标，因此不落在
//     授权范围门（intercept/scope.go 只对 Bash/shell_* 抽目标）——但 agent 拿结果去
//     curl/nmap 真目标时照样被 RoE 拦，边界不缺。
//   - **配额与审计**：每次真实外呼都落 intel_queries 台账，并有平台侧每日上限
//     （intel_daily_quota，默认 200）——FOFA 自己的额度之外再加一道闸。

// intelToolKeys 是情报工具默认绑定的 key（planner 决策方向、worker 执行侦察、
// mainagent 人工直查；不绑 goals/auto）。
var intelToolKeys = []string{"intel_search"}

func (s *Server) intelTools() []actool.CoreTool {
	return []actool.CoreTool{s.toolIntelSearch()}
}

// intelConfig 从设置**现读** provider 配置：改 key/端点立即生效，不需要重建 agent。
func (s *Server) intelConfig() intel.Config {
	get := func(key string) string {
		v, _, _ := s.m.pg.GetSetting(key)
		return strings.TrimSpace(v)
	}
	proxy := get(settingIntelProxy)
	if proxy == "" {
		// 没配独立出口就跟随全局出口代理（与 LLM 之外的出站调用同一条纪律：
		// 全局代理是"这台机器的出口"，每个子系统想覆盖才单独配）。
		proxy = s.m.GlobalProxy()
	}
	return intel.Config{
		Key:     get(settingIntelKey),
		Email:   get(settingIntelEmail),
		BaseURL: get(settingIntelBaseURL),
		Proxy:   proxy,
		Timeout: 30 * time.Second,
	}
}

// intelDailyQuota 是平台侧每日调用上限（0/未设 = 用默认值）。
func (s *Server) intelDailyQuota() int {
	v, ok, _ := s.m.pg.GetSetting(settingIntelDailyQuota)
	if !ok || strings.TrimSpace(v) == "" {
		return defaultIntelDailyQuota
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return defaultIntelDailyQuota
	}
	return n
}

// intelUsageToday 返回今日已用次数与配额（本地时区自然日）。
func (s *Server) intelUsageToday(ctx context.Context) (int, int) {
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	n, err := s.m.pg.CountIntelQueriesSince(ctx, midnight)
	if err != nil {
		n = 0
	}
	return n, s.intelDailyQuota()
}

func (s *Server) toolIntelSearch() actool.CoreTool {
	return roTool("intel_search",
		"向外部测绘引擎（情报源）查询互联网资产。当前可用源："+strings.Join(intel.Names(), "、")+"（FOFA）。\n"+
			"★表达式用【源的原生语法】——FOFA 就是 FOFA 语法，例如 domain=\"example.com\"、\n"+
			"  domain=\"example.com\" && protocol=\"http\"、ip=\"1.2.3.0/24\"、cert=\"example.com\"、\n"+
			"  title=\"后台\"。平台不做自然语言翻译，怎么写就怎么查。\n"+
			"返回：命中总数 + 前若干条的紧凑清单 + **完整结果的绝对路径**（大结果落盘，需要就 Read）。\n"+
			"⚠️ 本工具**只查询、不入库、不扩范围**：要把结果变成平台里的资产，请再用 insert_assets 登记；\n"+
			"要纳入本任务测试范围，请用 add_task_scope（以及注意资产拦截规则会拦掉不该碰的目标）。\n"+
			"⚠️ 每次调用都计入平台侧每日配额（设置页可见用量）。",
		objSchema(map[string]any{
			"provider": strParam("情报源名（当前只有 fofa；省略则默认 fofa）"),
			"expr":     strParam("源原生查询表达式（FOFA 语法）"),
			"limit":    map[string]any{"type": "integer", "description": "取回条数，默认 20，上限 100（命中总数可能远大于它）"},
			"fields": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "要请求的字段（默认 host/ip/port/protocol/domain/title/server；想要 icp/cert/os 等就显式给）",
			},
		}, "expr"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Provider string   `json:"provider"`
				Expr     string   `json:"expr"`
				Limit    int      `json:"limit"`
				Fields   []string `json:"fields"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Expr) == "" {
				return actool.Errorf("expr 不能为空（FOFA 语法，如 domain=\"example.com\"）"), nil
			}
			name := strings.TrimSpace(a.Provider)
			if name == "" {
				name = intel.Names()[0]
			}
			cfg := s.intelConfig()
			prov, err := intel.New(name, cfg)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 未配置 key 是"本地就没法发"的情况：给可读指引，不占配额、不落台账。
			if cfg.Key == "" {
				return actool.Errorf(fmt.Sprintf("情报源 %s 还没配 API Key：请在 系统设置 → 情报源 里填入（平台不代你申请 key）", name)), nil
			}
			used, quota := s.intelUsageToday(ctx)
			if quota > 0 && used >= quota {
				return actool.Errorf(fmt.Sprintf("情报查询已达平台侧每日上限（今日 %d/%d 次）：请明天再查，或在 系统设置 → 情报源 调整上限；"+
					"也可以用已有资产继续推进，不必依赖新查询", used, quota)), nil
			}

			ri := agent.RunInfoFrom(ctx)
			start := time.Now()
			res, err := prov.Search(ctx, intel.Query{Expr: a.Expr, Limit: a.Limit, Fields: a.Fields})
			taskID := ""
			if ri.TaskID > 0 {
				taskID = strconv.FormatInt(ri.TaskID, 10)
			}
			// 台账：成功与真实外呼失败都记（失败的调用也算用掉一次尝试）。
			row := db.IntelQuery{Provider: name, Expr: a.Expr, TaskID: taskID, IntentID: ri.IntentID}
			if err != nil {
				row.Error = err.Error()
				_ = s.m.pg.RecordIntelQuery(ctx, row)
				return actool.Errorf("查询失败：" + err.Error()), nil
			}
			row.TotalHits, row.Returned = res.Total, len(res.Assets)
			_ = s.m.pg.RecordIntelQuery(ctx, row)

			spoolPath := s.spoolIntelResult(taskID, name, res)
			s.auditIntelQuery(ri, name, a.Expr, res, spoolPath, time.Since(start))
			return actool.Text(renderIntelResult(res, spoolPath, used+1, quota)), nil
		})
}

// spoolIntelResult 把完整结果落盘并返回绝对路径（大结果不灌进模型上下文；
// 与 P13 执行输出、P17 产物同根：任务级产物都落在 <数据目录>/tasks/<task>/ 下）。
func (s *Server) spoolIntelResult(taskID, provider string, res intel.Result) string {
	dir := filepath.Join(s.m.dir, "intel")
	if taskID != "" {
		dir = filepath.Join(s.m.dir, "tasks", taskID, "intel")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", provider, time.Now().Unix()))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return ""
	}
	return path
}

// auditIntelQuery 把这次查询写进任务时间线（有任务上下文时）。
func (s *Server) auditIntelQuery(ri agent.RunInfo, provider, expr string, res intel.Result, spool string, took time.Duration) {
	if ri.TaskID <= 0 {
		return
	}
	t, ok := s.m.Task(strconv.FormatInt(ri.TaskID, 10))
	if !ok || t.Store == nil {
		return
	}
	detail := fmt.Sprintf("表达式：%s\n命中：%d 条，取回 %d 条（%.1fs）", expr, res.Total, len(res.Assets), took.Seconds())
	if spool != "" {
		detail += "\n完整结果：" + spool
	}
	_, _ = t.Store.AppendActivity(db.Activity{
		Worker: "system", Kind: "intel_query",
		Summary: fmt.Sprintf("情报查询（%s）：命中 %d 条", provider, res.Total),
		Detail:  detail,
	})
}

// renderIntelResult 渲染给模型看的紧凑回执：够它判断"这条线有没有货"，又不灌上下文。
func renderIntelResult(res intel.Result, spool string, used, quota int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "intel_search(%s) 命中 %d 条，本次取回 %d 条\n", res.Provider, res.Total, len(res.Assets))
	if quota > 0 {
		fmt.Fprintf(&b, "（平台侧今日用量 %d/%d）\n", used, quota)
	}
	if len(res.Assets) == 0 {
		b.WriteString("没有命中：换个表达式（放宽条件/换字段）或换关键词再试；空结果也是一条可写进 fact 的结论。\n")
	}
	const maxLines = 20
	for i, a := range res.Assets {
		if i >= maxLines {
			fmt.Fprintf(&b, "… 其余 %d 条见完整结果文件\n", len(res.Assets)-maxLines)
			break
		}
		addr := a.IP
		if a.Port > 0 {
			addr = fmt.Sprintf("%s:%d", a.IP, a.Port)
		}
		title := a.Title
		if r := []rune(title); len(r) > 60 {
			title = string(r[:60]) + "…"
		}
		fmt.Fprintf(&b, "%2d. %-42s %-21s %s\n", i+1, clipIntel(a.Display(), 42), addr, title)
	}
	if spool != "" {
		b.WriteString("完整结果（JSON，含全部字段）：" + spool + "\n")
	} else {
		b.WriteString("（完整结果落盘失败，仅上面这些可用）\n")
	}
	b.WriteString("下一步：要用哪条就显式调 insert_assets 登记（本工具不入库）；要纳入本任务范围用 add_task_scope。\n")
	return b.String()
}

func clipIntel(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
