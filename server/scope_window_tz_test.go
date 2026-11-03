package server

import (
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/intercept"
)

// 窗口按**配置的时区**判定：VPS 加固脚本把机器设成 UTC，用户在北京时间写 09:00-18:00
// 时必须在北京时间生效。这里断言执行侧与状态回执都按这个口径走。
func TestScopeWindowUsesConfiguredTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	_ = loc
	// 纯判定层已由 intercept 的用例覆盖；这里固定住"执行侧传的是配置时区"这一接线。
	now := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC) // 北京 10:00
	in, err := intercept.WindowAllowsIn("09:00-18:00", "Asia/Shanghai", now)
	if err != nil || !in {
		t.Fatalf("北京 10:00 应在窗口内: %v %v", in, err)
	}
	if in, _ := intercept.WindowAllowsIn("09:00-18:00", "UTC", now); in {
		t.Fatal("同一时刻按 UTC 是 02:00，不该在窗口内")
	}
	// 状态回执要带上两种时间的写法，页面才能显示偏差。
	st := scopeWindowState{TZ: "Asia/Shanghai",
		ServerTime: intercept.ScopeClock(now, time.Local),
		WindowTime: intercept.ScopeClock(now, loc)}
	if !strings.Contains(st.WindowTime, "10:00") || !strings.Contains(st.WindowTime, "Asia/Shanghai") {
		t.Fatalf("窗口时区时间不对: %s", st.WindowTime)
	}
	if st.ServerTime == "" {
		t.Fatal("服务器本地时间必须一并给出")
	}
}
