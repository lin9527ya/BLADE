// Package sched 是「计划任务」的频率模型：一份结构化频率声明 + 下一次触发时刻的计算。
//
// 纯函数、无依赖——**刻意不引 cron 库**：结构化表单（每 N 分钟/小时、每天 HH:MM、
// 每周 W HH:MM）覆盖了渗透巡检的全部实际需要，而 cron 表达式的边界语义（夏令时、
// 月末、跳秒、六个字段的方言）只会给操作者带来歧义。要更复杂的编排就该用任务编排
// 而不是把 cron 抄进来。
package sched

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 频率种类（封闭词表；未知值在 Validate 处拒绝，不静默当成某个默认）。
const (
	KindEveryMinutes = "every_minutes"
	KindEveryHours   = "every_hours"
	KindDailyAt      = "daily_at"
	KindWeeklyAt     = "weekly_at"
)

// 间隔频率的上限：7 天。比这更长的时间尺度用"每周某天"表达更清楚。
const (
	MinEveryMinutes = 5
	MaxEveryMinutes = 7 * 24 * 60
	MinEveryHours   = 1
	MaxEveryHours   = 7 * 24
)

// Frequency 是一份频率声明。零值无效（Validate 会拒）。
type Frequency struct {
	Kind         string `json:"kind"`
	EveryMinutes int    `json:"every_minutes,omitempty"`
	EveryHours   int    `json:"every_hours,omitempty"`
	// At 是 "HH:MM"（本地时区）。
	At string `json:"at,omitempty"`
	// Weekday 是 0=周日 … 6=周六（与 time.Weekday 对齐）。
	Weekday int `json:"weekday,omitempty"`
}

// weekdayNames 按 time.Weekday 下标取名。
var weekdayNames = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// Validate 校验频率是否合法（写入侧据此 400；不合法绝不落库）。
func (f Frequency) Validate() error {
	switch f.Kind {
	case KindEveryMinutes:
		if f.EveryMinutes < MinEveryMinutes || f.EveryMinutes > MaxEveryMinutes {
			return fmt.Errorf("每 %d 分钟不合法：需在 %d–%d 分钟之间", f.EveryMinutes, MinEveryMinutes, MaxEveryMinutes)
		}
	case KindEveryHours:
		if f.EveryHours < MinEveryHours || f.EveryHours > MaxEveryHours {
			return fmt.Errorf("每 %d 小时不合法：需在 %d–%d 小时之间", f.EveryHours, MinEveryHours, MaxEveryHours)
		}
	case KindDailyAt:
		if _, _, err := parseAt(f.At); err != nil {
			return err
		}
	case KindWeeklyAt:
		if f.Weekday < 0 || f.Weekday > 6 {
			return fmt.Errorf("星期取值 %d 不合法：0=周日 … 6=周六", f.Weekday)
		}
		if _, _, err := parseAt(f.At); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知的频率类型 %q（可用：every_minutes / every_hours / daily_at / weekly_at）", f.Kind)
	}
	return nil
}

// Describe 渲染成人话（UI 列表与日志共用同一份说法）。
func (f Frequency) Describe() string {
	switch f.Kind {
	case KindEveryMinutes:
		return fmt.Sprintf("每 %d 分钟", f.EveryMinutes)
	case KindEveryHours:
		return fmt.Sprintf("每 %d 小时", f.EveryHours)
	case KindDailyAt:
		return fmt.Sprintf("每天 %s", f.At)
	case KindWeeklyAt:
		w := f.Weekday
		if w < 0 || w > 6 {
			w = 0
		}
		return fmt.Sprintf("每%s %s", weekdayNames[w], f.At)
	}
	return f.Kind
}

// Next 返回严格晚于 after 的下一次触发时刻（本地时区）。
//
// 语义（写进 UI 与文档，避免"为什么没跑"的歧义）：
//   - 间隔型：**从锚点起算**（创建/上次触发 + 间隔），秒截到 0；不追补错过的那几次
//     （进程停机三天后再开机，只跑一次，不补三次）。
//   - 定时刻型（每天/每周）：按本地墙钟算；"就是现在"算下一次（严格晚于 after），
//     避免同一分钟里连触发两次。
func (f Frequency) Next(after time.Time) (time.Time, error) {
	if err := f.Validate(); err != nil {
		return time.Time{}, err
	}
	switch f.Kind {
	case KindEveryMinutes:
		return after.Truncate(time.Minute).Add(time.Duration(f.EveryMinutes) * time.Minute), nil
	case KindEveryHours:
		return after.Truncate(time.Minute).Add(time.Duration(f.EveryHours) * time.Hour), nil
	case KindDailyAt:
		h, m, _ := parseAt(f.At)
		t := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, after.Location())
		if !t.After(after) {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	case KindWeeklyAt:
		h, m, _ := parseAt(f.At)
		// 先落到"本周该星期几的 HH:MM"，不满足严格晚于就加一周。
		delta := (f.Weekday - int(after.Weekday()) + 7) % 7
		t := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, after.Location()).AddDate(0, 0, delta)
		if !t.After(after) {
			t = t.AddDate(0, 0, 7)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("未知的频率类型 %q", f.Kind)
}

// parseAt 解析 "HH:MM"（也容忍 "H:MM"）。
func parseAt(s string) (hour, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("时间 %q 不合法：要写成 HH:MM（如 09:30）", s)
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("时间 %q 不合法：要写成 00:00–23:59 之间的 HH:MM", s)
	}
	return h, m, nil
}
