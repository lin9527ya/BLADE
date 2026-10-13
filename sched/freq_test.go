package sched

import (
	"strings"
	"testing"
	"time"
)

// 本地时区固定成 UTC 做断言：Next 用的是传入时刻的 Location，测试里显式传 UTC，
// 断言就与跑测试的机器时区无关（CI 与本机同结果）。
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestFrequencyValidate(t *testing.T) {
	cases := []struct {
		name string
		f    Frequency
		want string // 期望错误里包含的片段；空 = 应通过
	}{
		{"每 30 分钟", Frequency{Kind: KindEveryMinutes, EveryMinutes: 30}, ""},
		{"每 5 分钟（下限）", Frequency{Kind: KindEveryMinutes, EveryMinutes: 5}, ""},
		{"每 4 分钟（低于下限）", Frequency{Kind: KindEveryMinutes, EveryMinutes: 4}, "5–10080"},
		{"每 10081 分钟（超上限）", Frequency{Kind: KindEveryMinutes, EveryMinutes: 10081}, "5–10080"},
		{"每 6 小时", Frequency{Kind: KindEveryHours, EveryHours: 6}, ""},
		{"每 0 小时", Frequency{Kind: KindEveryHours, EveryHours: 0}, "1–168"},
		{"每天 09:30", Frequency{Kind: KindDailyAt, At: "09:30"}, ""},
		{"每天 9:30（容忍短写）", Frequency{Kind: KindDailyAt, At: "9:30"}, ""},
		{"每天 25:00", Frequency{Kind: KindDailyAt, At: "25:00"}, "不合法"},
		{"每天 09:60", Frequency{Kind: KindDailyAt, At: "09:60"}, "不合法"},
		{"每天 930（缺冒号）", Frequency{Kind: KindDailyAt, At: "930"}, "不合法"},
		{"每周一 09:30", Frequency{Kind: KindWeeklyAt, At: "09:30", Weekday: 1}, ""},
		{"每周日 00:00", Frequency{Kind: KindWeeklyAt, At: "00:00", Weekday: 0}, ""},
		{"星期 7", Frequency{Kind: KindWeeklyAt, At: "09:30", Weekday: 7}, "0=周日"},
		{"未知类型", Frequency{Kind: "cron", At: "09:30"}, "未知的频率类型"},
		{"零值", Frequency{}, "未知的频率类型"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.f.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("应通过，实际: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误应含 %q，实际: %v", c.want, err)
			}
		})
	}
}

func TestFrequencyDescribe(t *testing.T) {
	cases := []struct {
		f    Frequency
		want string
	}{
		{Frequency{Kind: KindEveryMinutes, EveryMinutes: 30}, "每 30 分钟"},
		{Frequency{Kind: KindEveryHours, EveryHours: 6}, "每 6 小时"},
		{Frequency{Kind: KindDailyAt, At: "09:30"}, "每天 09:30"},
		{Frequency{Kind: KindWeeklyAt, At: "09:30", Weekday: 1}, "每周一 09:30"},
		{Frequency{Kind: KindWeeklyAt, At: "08:00", Weekday: 0}, "每周日 08:00"},
	}
	for _, c := range cases {
		if got := c.f.Describe(); got != c.want {
			t.Fatalf("%+v Describe=%q want %q", c.f, got, c.want)
		}
	}
}

func TestFrequencyNext(t *testing.T) {
	cases := []struct {
		name string
		f    Frequency
		now  time.Time
		want time.Time
	}{
		// 间隔型：锚点 + 间隔，秒截 0；不做时钟对齐（不做"每 30 分钟就是整点/半点"）。
		{"每 30 分钟：锚点 10:07:33", Frequency{Kind: KindEveryMinutes, EveryMinutes: 30},
			time.Date(2026, 10, 13, 10, 7, 33, 0, time.UTC), at(2026, 10, 13, 10, 37)},
		{"每 30 分钟：锚点 23:50", Frequency{Kind: KindEveryMinutes, EveryMinutes: 30},
			at(2026, 10, 13, 23, 50), at(2026, 10, 14, 0, 20)},
		{"每 6 小时", Frequency{Kind: KindEveryHours, EveryHours: 6},
			at(2026, 10, 13, 10, 7), at(2026, 10, 13, 16, 7)},
		{"每 24 小时跨天", Frequency{Kind: KindEveryHours, EveryHours: 24},
			at(2026, 10, 13, 10, 7), at(2026, 10, 14, 10, 7)},

		// 每天 HH:MM：今天还没到就是今天，到了/已过就是明天（"就是现在"算下一次）。
		{"每天 09:30：现在是 08:00", Frequency{Kind: KindDailyAt, At: "09:30"},
			at(2026, 10, 13, 8, 0), at(2026, 10, 13, 9, 30)},
		{"每天 09:30：现在是 10:00", Frequency{Kind: KindDailyAt, At: "09:30"},
			at(2026, 10, 13, 10, 0), at(2026, 10, 14, 9, 30)},
		{"每天 09:30：现在正好 09:30", Frequency{Kind: KindDailyAt, At: "09:30"},
			at(2026, 10, 13, 9, 30), at(2026, 10, 14, 9, 30)},
		{"每天 09:30：现在 09:30:00.5", Frequency{Kind: KindDailyAt, At: "09:30"},
			time.Date(2026, 10, 13, 9, 30, 0, 500000000, time.UTC), at(2026, 10, 14, 9, 30)},
		{"每天 09:30：跨月（10-31 → 11-01）", Frequency{Kind: KindDailyAt, At: "09:30"},
			at(2026, 10, 31, 10, 0), at(2026, 11, 1, 9, 30)},

		// 每周 W HH:MM：先落到本周该星期几，不满足严格晚于就加一周。
		{"每周一 09:30：现在是周日", Frequency{Kind: KindWeeklyAt, At: "09:30", Weekday: 1},
			at(2026, 10, 11, 12, 0), at(2026, 10, 12, 9, 30)}, // 2026-10-11 是周日
		{"每周一 09:30：现在是周一 08:00", Frequency{Kind: KindWeeklyAt, At: "09:30", Weekday: 1},
			at(2026, 10, 12, 8, 0), at(2026, 10, 12, 9, 30)},
		{"每周一 09:30：现在是周一 10:00", Frequency{Kind: KindWeeklyAt, At: "09:30", Weekday: 1},
			at(2026, 10, 12, 10, 0), at(2026, 10, 19, 9, 30)},
		{"每周一 09:30：现在正好周一 09:30", Frequency{Kind: KindWeeklyAt, At: "09:30", Weekday: 1},
			at(2026, 10, 12, 9, 30), at(2026, 10, 19, 9, 30)},
		{"每周日 08:00：现在是周日 07:00", Frequency{Kind: KindWeeklyAt, At: "08:00", Weekday: 0},
			at(2026, 10, 11, 7, 0), at(2026, 10, 11, 8, 0)},
		{"每周六 23:00：跨年（2027-01-02 是周六）", Frequency{Kind: KindWeeklyAt, At: "23:00", Weekday: 6},
			at(2026, 12, 31, 10, 0), at(2027, 1, 2, 23, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.f.Next(c.now)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(c.want) {
				t.Fatalf("Next=%s want %s", got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
			if !got.After(c.now) {
				t.Fatalf("Next 必须严格晚于 now（否则会同一分钟连触发）: got=%s now=%s", got, c.now)
			}
		})
	}
}

// 非法频率算不出下一次（写入侧该拒的，读取侧也不该悄悄退化成某个默认）。
func TestNextRejectsInvalid(t *testing.T) {
	if _, err := (Frequency{Kind: KindDailyAt, At: "25:00"}).Next(at(2026, 10, 13, 0, 0)); err == nil {
		t.Fatal("非法时间必须报错")
	}
	if _, err := (Frequency{Kind: "cron"}).Next(at(2026, 10, 13, 0, 0)); err == nil {
		t.Fatal("未知类型必须报错")
	}
}
