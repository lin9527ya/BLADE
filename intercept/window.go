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

// SettingScopeHoursTZ 是作业时间窗的**时区**（IANA 名，如 Asia/Shanghai）。
//
// 为什么需要它：窗口是按"客户的办公时间"排的，而部署机的时区未必与写窗口的人一致
// （deploy/vps-hardening.sh 会把 VPS 设成 UTC，于是页面上写 09:00-18:00 实际在北京
// 时间 17:00–次日 02:00 生效）。空 = 跟随本机时区（旧行为，保持向后兼容）。
const SettingScopeHoursTZ = "intercept_scope_hours_tz"

// LoadScopeLocation 把时区设置解析成 Location。空 / "local" = 本机时区。
func LoadScopeLocation(name string) (*time.Location, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "local":
		return time.Local, nil
	case "utc":
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return nil, fmt.Errorf("时区 %q 无法识别（要 IANA 名，如 Asia/Shanghai、UTC；留空 = 跟随平台所在机器）：%w", name, err)
	}
	return loc, nil
}

// ValidateScopeHoursTZ 校验时区设置串，供设置接口在落库前挡住写错的值。
func ValidateScopeHoursTZ(name string) error {
	_, err := LoadScopeLocation(name)
	return err
}

// ScopeClock 描述"某个时区现在几点"，用于回执与页面（人要能一眼看出偏差）。
func ScopeClock(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	return t.In(loc).Format("2006-01-02 15:04") + " " + loc.String()
}

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
	w, _, err := i.scopeHoursAndTZ()
	return w, err
}

// scopeHoursAndTZ 同时取窗口与时区（两者都是"每次调用现读"，改完即时生效）。
func (i *Interceptor) scopeHoursAndTZ() (*scopeWindow, string, error) {
	raw, ok, err := i.db.GetSetting(SettingScopeHours)
	if err != nil || !ok {
		return nil, "", nil // 读不到 = 没配 = 不限时，与 ScopeMode 的降级口径一致
	}
	w, err := parseScopeHours(raw)
	if err != nil {
		return nil, "", err
	}
	tz, _, _ := i.db.GetSetting(SettingScopeHoursTZ)
	return w, strings.TrimSpace(tz), nil
}

// WindowAllows 是给**执行侧**用的窗口判定（server/scope_enforce.go 的到点暂停）：
// 传原始设置串与一个时刻，回答"这一刻允不允许动手"。空串 = 不限时 = 允许。
// 按**本机时区**判定（旧口径）；带时区的判定用 WindowAllowsIn。
//
// 与 scopeHours() 的区别只是取设置的方式（那边从库读、这边由调用方给串）：判定逻辑共用
// parseScopeHours/allows，不另写一份——"某一刻在不在窗口内"这件事有两个实现，迟早会不一致。
func WindowAllows(raw string, t time.Time) (bool, error) {
	return WindowAllowsIn(raw, "", t)
}

// WindowAllowsIn 按**指定时区**判定窗口。tzName 为空 = 本机时区。
//
// 时刻本身是绝对时间（time.Time 带位置信息），换算到窗口时区后再取时分——
// 这样"客户办公时间 09:00"在哪个时区部署都是同一个物理时刻。
func WindowAllowsIn(raw, tzName string, t time.Time) (bool, error) {
	w, err := parseScopeHours(raw)
	if err != nil {
		return false, err
	}
	loc, err := LoadScopeLocation(tzName)
	if err != nil {
		return false, err
	}
	return w.allows(t.In(loc)), nil
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
