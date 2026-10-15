package intercept

import (
	"strings"
	"testing"
	"time"
)

// 作业时间窗是授权边界的时间维度：scope.go 判「能打哪些目标」，这里判「什么时候能打」。
// 这些用例全是纯函数（不连库、不碰时钟），判定逻辑错在哪一眼可见。

func TestParseScopeHoursRejects(t *testing.T) {
	cases := []struct{ in, want string }{
		{"09:00", "缺少「-」"},
		{"09:60-18:00", "起点"},
		{"09:00-25:00", "终点"},
		{"9-18", "HH:MM"},
		{"09:00-09:00", "永不放行"},
		{"09:00-18:00,", ""}, // 尾随逗号是空的第二段，跳过而不是报错
	}
	for _, c := range cases {
		_, err := parseScopeHours(c.in)
		if c.want == "" {
			if err != nil {
				t.Errorf("%q 该被接受，却报错：%v", c.in, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%q 该被拒（%s），却通过了", c.in, c.want)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q 的错误信息没点明 %q：%v", c.in, c.want, err)
		}
	}
}

func TestParseScopeHoursEmpty(t *testing.T) {
	for _, in := range []string{"", "   "} {
		w, err := parseScopeHours(in)
		if err != nil || w != nil {
			t.Fatalf("%q 该解析成「未配置」（nil, nil），得到 (%v, %v)", in, w, err)
		}
	}
	// nil 窗口恒放行——没配窗口就是「没有时间维度的约束」，不是「默认禁止」。
	var nilW *scopeWindow
	at(t, nilW, "03:00").mustBe(true)
}

// at 造一个当天 HH:MM 的时刻，供 allows 断言用。
func at(t *testing.T, w *scopeWindow, hhmm string) *windowAssert {
	t.Helper()
	clock, err := parseClock(hhmm)
	if err != nil {
		t.Fatalf("用例里的时刻写错了：%v", err)
	}
	ts := time.Date(2026, 10, 15, clock/60, clock%60, 0, 0, time.Local)
	return &windowAssert{t: t, got: w.allows(ts), when: hhmm}
}

type windowAssert struct {
	t    *testing.T
	got  bool
	when string
}

func (a *windowAssert) mustBe(want bool) {
	a.t.Helper()
	if a.got != want {
		a.t.Errorf("%s 的判定应为 %v，得到 %v", a.when, want, a.got)
	}
}

func TestScopeWindowAllowsSameDay(t *testing.T) {
	w, err := parseScopeHours("09:00-18:00")
	if err != nil {
		t.Fatal(err)
	}
	// 左闭右开：09:00 在内，18:00 已出窗（"到 18 点"不该包含 18:00 那一刻起的动作）。
	at(t, w, "08:59").mustBe(false)
	at(t, w, "09:00").mustBe(true)
	at(t, w, "12:00").mustBe(true)
	at(t, w, "17:59").mustBe(true)
	at(t, w, "18:00").mustBe(false)
	at(t, w, "00:00").mustBe(false)
}

func TestScopeWindowAllowsOvernight(t *testing.T) {
	w, err := parseScopeHours("22:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	// 起点晚于终点 = 跨夜：当晚 22:00 到次日 06:00。
	at(t, w, "21:59").mustBe(false)
	at(t, w, "22:00").mustBe(true)
	at(t, w, "23:59").mustBe(true)
	at(t, w, "00:00").mustBe(true)
	at(t, w, "05:59").mustBe(true)
	at(t, w, "06:00").mustBe(false)
	at(t, w, "12:00").mustBe(false)
}

func TestScopeWindowAllowsMultipleRanges(t *testing.T) {
	w, err := parseScopeHours("09:00-12:00, 14:00-18:00")
	if err != nil {
		t.Fatal(err)
	}
	at(t, w, "10:00").mustBe(true)  // 上午段
	at(t, w, "12:30").mustBe(false) // 午休不在任何一段里
	at(t, w, "15:00").mustBe(true)  // 下午段
	at(t, w, "19:00").mustBe(false)
}
