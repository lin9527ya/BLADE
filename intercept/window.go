package intercept

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 作业时间窗：授权作业**不只是「能打哪些目标」，还包括「什么时候能打」**。
//
// scope.go 判的是空间（目标在不在 task_scope 内），这里判的是时间——两者同属授权边界，
// 所以共用同一个模式开关（off/ask/deny，见 ScopeMode），在同一条判定路径上先后生效。
//
// 真实授权作业里这是硬要求：客户的授权书会写明作业窗口（通常就是他们的办公时间），
// 而"你只在这个时段动过手"是收尾时要能自证的东西。本仓已有的执行台账（tool_executions +
// activity）能给出"什么时候做了什么"，这个设置给出"允不允许做"。

// SettingScopeHours 是作业时间窗设置。空/缺省 = **不限时**——与 scope_mode 的 off 不同：
// 没配窗口就是"没有时间维度的约束"，而不是"默认禁止"。
const SettingScopeHours = "intercept_scope_hours"

// scopeWindow 是一个已解析的作业时间窗。
type scopeWindow struct {
	ranges []scopeTimeRange
	raw    string
}

type scopeTimeRange struct{ startMin, endMin int } // 当日分钟数 0..1439

// parseScopeHours 解析作业时间窗。
//
// 格式：`09:00-18:00`；可用逗号给多段：`09:00-12:00,14:00-18:00`。
// **起点晚于终点表示跨夜**：`22:00-06:00` = 当天 22:00 到次日 06:00。
// 空串返回 (nil, nil) = 不限时；格式错误返回 error——调用方要如实拒绝并说明怎么改，
// 不能静默当成"不限时"：一条写错的窗口设置静默失效，恰恰是客户验收时会核对的那一项。
//
// 时区取**本机时区**：作业窗口是给人排的（"客户的办公时间"），而部署时区就是运维认知里的
// 那个时间。跨时区作业要另加时区配置，这里不假装支持。
func parseScopeHours(raw string) (*scopeWindow, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var ranges []scopeTimeRange
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		start, end, ok := strings.Cut(part, "-")
		if !ok {
			return nil, fmt.Errorf("时间段 %q 缺少「-」分隔（正确写法如 09:00-18:00）", part)
		}
		s, err := parseClock(start)
		if err != nil {
			return nil, fmt.Errorf("时间段 %q 的起点：%w", part, err)
		}
		e, err := parseClock(end)
		if err != nil {
			return nil, fmt.Errorf("时间段 %q 的终点：%w", part, err)
		}
		if s == e {
			return nil, fmt.Errorf("时间段 %q 起止相同，等于永不放行；要全天就别设这个窗口", part)
		}
		ranges = append(ranges, scopeTimeRange{startMin: s, endMin: e})
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	return &scopeWindow{ranges: ranges, raw: raw}, nil
}

// parseClock 把 "HH:MM" 解析成当日分钟数。
func parseClock(s string) (int, error) {
	s = strings.TrimSpace(s)
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("%q 不是 HH:MM", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("%q 不是合法的 HH:MM（00:00–23:59）", s)
	}
	return hh*60 + mm, nil
}

// allows 报告某一时刻是否落在窗口内。nil 窗口（未配置）恒为放行。
func (w *scopeWindow) allows(t time.Time) bool {
	if w == nil {
		return true
	}
	now := t.Hour()*60 + t.Minute()
	for _, r := range w.ranges {
		if r.startMin < r.endMin { // 同日区间，左闭右开
			if now >= r.startMin && now < r.endMin {
				return true
			}
			continue
		}
		// 跨夜：22:00-06:00 = [22:00,24:00) ∪ [00:00,06:00)
		if now >= r.startMin || now < r.endMin {
			return true
		}
	}
	return false
}

// Describe 渲染成人话（回执/日志用），原样回显用户写的窗口。
func (w *scopeWindow) Describe() string {
	if w == nil {
		return "不限时"
	}
	return w.raw
}

// scopeNow 是窗口判定的取时点。**做成变量是为了可测**：窗口判定的正确性全在"某一时刻在不在
// 区间内"，而"现在几点"不能由测试摆布——不注入时钟就只能写出靠真实时间碰运气的用例。
var scopeNow = time.Now

// scopeHours 现读窗口设置（每调用读一次：改设置立即生效，与 ScopeMode 同口径）。
func (i *Interceptor) scopeHours() (*scopeWindow, error) {
	raw, ok, err := i.db.GetSetting(SettingScopeHours)
	if err != nil || !ok {
		return nil, nil // 读不到 = 没配 = 不限时，与 ScopeMode 的降级口径一致
	}
	return parseScopeHours(raw)
}

// ValidateScopeHours 校验一个作业时间窗设置串，供设置接口在**落库前**挡住格式错误。
//
// 为什么要在这里挡：存进去一条解析不了的窗口，之后每一次工具调用都会落到人工确认上
// （CheckScope 对无法解析的窗口如实 ask），而用户多半要等到任务跑不动了才发现。
// 当场退回并说清怎么写，比事后从一堆待审批里反推原因便宜得多。
func ValidateScopeHours(raw string) error {
	_, err := parseScopeHours(raw)
	return err
}
