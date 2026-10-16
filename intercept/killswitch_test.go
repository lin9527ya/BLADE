package intercept

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 停机开关与作业时间窗是**两层里靠上的那一层**（管"这一次调用放不放行"；
// 管"整个平台还该不该跑"的在 server/scope_enforce.go）。这里钉住三件事：
//   1. 停机开关**不是**授权范围的一档——scope_mode=off 时照样拦；
//   2. 作业时间窗（P35 起）同样与 scope_mode 解耦；
//   3. 停的是"动手"，不是"看"——非动作类工具不受影响。

func killSwitchDB(t *testing.T) *db.DB {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestKillSwitchDeniesActionsEvenWhenScopeModeOff(t *testing.T) {
	d := killSwitchDB(t)
	t.Setenv(EnvKillSwitch, "")
	defer setSetting(t, d, SettingScopeMode, "off")()
	defer setSetting(t, d, SettingScopeHours, "")()
	defer setSetting(t, d, SettingKillSwitch, "")()
	i := New(d)

	action := json.RawMessage(`{"command":"nmap evil.example"}`)

	// 关着：scope off → 不拦（这是既有的"不做空间判定"语义）。
	if _, hit := i.CheckScope(context.Background(), 1, "Bash", action); hit {
		t.Fatal("开关未启用时，scope off 不该拦任何东西")
	}

	// 启用：**立刻**拦，而且与 scope_mode 无关。
	st := KillSwitchReceipt{On: true, At: time.Now().Format(time.RFC3339), Reason: "客户来电叫停"}
	if err := WriteKillSwitch(d, st); err != nil {
		t.Fatal(err)
	}
	dec, hit := i.CheckScope(context.Background(), 1, "Bash", action)
	if !hit {
		t.Fatal("停机开关启用后，动作该被拦——哪怕 scope_mode=off")
	}
	if dec.Action != "deny" {
		t.Errorf("停机是硬拒（不是审批项），得到 %q", dec.Action)
	}
	if !strings.Contains(dec.RuleName, "停机开关") || !strings.Contains(dec.Message, "客户来电叫停") {
		t.Errorf("拒绝话术该点明是停机、并带上理由：%q / %q", dec.RuleName, dec.Message)
	}

	// 停的是"动手"，不是"看"：读图/记事实这类非动作工具照常放行，
	// 否则人连"现在到底是什么状态"都查不了。
	if _, hit := i.CheckScope(context.Background(), 1, "graph_overview", json.RawMessage(`{}`)); hit {
		t.Error("非动作类工具不该被停机开关拦住")
	}

	// 解除 → 恢复放行。
	st.On = false
	if err := WriteKillSwitch(d, st); err != nil {
		t.Fatal(err)
	}
	if _, hit := i.CheckScope(context.Background(), 1, "Bash", action); hit {
		t.Fatal("解除停机后不该再拦")
	}
}

func TestKillSwitchForcedByEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "YES", "on"} {
		t.Setenv(EnvKillSwitch, v)
		if !KillSwitchForcedByEnv() {
			t.Errorf("%s=%q 该判为强制停机", EnvKillSwitch, v)
		}
	}
	for _, v := range []string{"", "0", "false", "no"} {
		t.Setenv(EnvKillSwitch, v)
		if KillSwitchForcedByEnv() {
			t.Errorf("%s=%q 不该判为强制停机", EnvKillSwitch, v)
		}
	}

	// 环境变量压着时，库里没开也拦——这是"出事的原因可能就是平台自己"时的最外层刹车。
	d := killSwitchDB(t)
	t.Setenv(EnvKillSwitch, "1")
	defer setSetting(t, d, SettingScopeMode, "off")()
	defer setSetting(t, d, SettingKillSwitch, "")()
	i := New(d)
	dec, hit := i.CheckScope(context.Background(), 1, "Bash", json.RawMessage(`{"command":"id"}`))
	if !hit || dec.Action != "deny" {
		t.Fatalf("环境变量强制停机时该硬拒，得到 hit=%v action=%q", hit, dec.Action)
	}
	if !strings.Contains(dec.Message, EnvKillSwitch) {
		t.Errorf("话术该点明是哪个环境变量压着：%s", dec.Message)
	}
}

// 窗口与 scope_mode 解耦（P35）：off 只关"空间判定"，不该把"什么时候能打"一起关掉。
// 此前 off 档会连窗口一起跳过——设了窗口却什么都没拦，是最容易被忽略的一种失效。
func TestWindowAppliesWhenScopeModeOff(t *testing.T) {
	d := killSwitchDB(t)
	t.Setenv(EnvKillSwitch, "")
	defer setSetting(t, d, SettingScopeMode, "off")()
	defer setSetting(t, d, SettingScopeHours, "09:00-18:00")()
	defer setSetting(t, d, SettingKillSwitch, "")()
	i := New(d)

	oldNow := scopeNow
	defer func() { scopeNow = oldNow }()
	cmd := json.RawMessage(`{"command":"nmap evil.example"}`)

	scopeNow = func() time.Time { return time.Date(2026, 10, 16, 23, 30, 0, 0, time.Local) }
	dec, hit := i.CheckScope(context.Background(), 1, "Bash", cmd)
	if !hit || dec.Action != "deny" {
		t.Fatalf("窗口外该硬拒（与 scope_mode 无关），得到 hit=%v action=%q", hit, dec.Action)
	}
	if !strings.Contains(dec.Message, "改作业时间窗设置") {
		t.Errorf("拒绝话术该告诉人怎么放宽：%s", dec.Message)
	}

	scopeNow = func() time.Time { return time.Date(2026, 10, 16, 10, 0, 0, 0, time.Local) }
	if _, hit := i.CheckScope(context.Background(), 1, "Bash", cmd); hit {
		t.Fatal("窗口内、scope off 时不该拦")
	}
}
