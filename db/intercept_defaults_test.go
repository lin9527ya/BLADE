package db

import (
	"regexp"
	"testing"
)

// DoS 内置规则要覆盖真实施压工具，又不能被域名/文章里的 "ddos" 误伤。
func TestDoSCommandPattern(t *testing.T) {
	re := regexp.MustCompile(dosCommandPattern)
	hit := []string{
		"hping3 -S -p 80 --flood target",
		"slowloris target 80",
		"ab -n 100000 -c 500 http://target/",
		"wrk -t12 -c400 -d30s http://target/",
		"jmeter -n -t plan.jmx",
		"对登录接口做压力测试",
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("应命中 DoS 规则: %q", s)
		}
	}
	miss := []string{
		"curl https://ddos.example.com/",
		"cat /var/log/flood.log",
		"grep -r 'slowloris' ./notes",
		"curl https://target/delivery",
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("不应命中 DoS 规则: %q", s)
		}
	}
}

// 痕迹清理允许做，但每次都要人工审批；正则必须只命中“改日志”的动作，
// 不能把读日志、正常路径或其它 SQL 语句拖进审批队列。
func TestLogTamperPattern(t *testing.T) {
	re := regexp.MustCompile(logTamperPattern)
	hit := []string{
		"history -c",
		"wevtutil cl Security",
		"Clear-EventLog -LogName Security",
		"journalctl --vacuum-time=1s",
		"truncate -s 0 /var/log/auth.log",
		"rm -f /var/log/nginx/access.log",
		"echo x > /var/log/auth.log",
		"unset HISTFILE",
		"auditctl -D",
		"systemctl stop auditd",
		"touch -t 202001010000 /var/log/auth.log",
		"shred -u /var/log/auth.log",
		`del C:\Windows\System32\winevt\Logs\Security.evtx`,
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("应命中日志修改审批规则: %q", s)
		}
	}
	// 真·系统日志写入仍要命中（含引号路径、相对路径、find -delete、dd of=日志）。
	hitMore := []string{
		`echo x > "/var/log/auth.log"`,
		`echo x >> /var/log/auth.log`,
		`:> /var/log/auth.log`,
		`rm -f ~/.bash_history`,
		`rm -f ~/.zsh_history`,
		`dd if=/dev/zero of=/var/log/auth.log`,
		`find /var/log -name '*.log' -delete`,
		`find /var/adm -type f -delete`,
	}
	for _, s := range hitMore {
		if !re.MatchString(s) {
			t.Errorf("应命中日志修改审批规则: %q", s)
		}
	}
	miss := []string{
		"cat /var/log/auth.log",
		"grep -i failed /var/log/auth.log",
		"curl http://target/delivery",
		"SELECT * FROM audit_log",
		"ls /var/log/nginx/",
		// 误报回归：2>&1 之后的 console.log 不是“重定向到 .log 文件”。
		`cd /tmp && python3 -c "import playwright; print('ok')" 2>&1 | head -3; node -e "console.log('node playwright ok')"`,
		`python3 -c "print(1)" 2>&1 | node -e "console.log('ok')"`,
		`curl -s http://target/x 2>&1 | grep app.log`,
		`echo hi > /dev/null 2>&1; node -e "console.log(1)"`,
		// v8 回归：自己的普通日志文件/构建输出不是“目标系统日志”。
		`echo x >> app.log`,
		`nohup ./scan > scan.log 2>&1 &`,
		`truncate -s 0 /tmp/scan.log`,
		`rm -f /tmp/scan.log`,
		`touch -t 202001010000 /tmp/scan.log`,
		`shred -u ./out.log`,
		`dd if=/dev/zero of=/dev/null bs=1M count=1`,
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("不应命中日志修改审批规则: %q", s)
		}
	}
}

// v6 迁移只改仍是出厂 v5 正则的规则；同时把内置 ask 规则的默认超时打开。
func TestLogTamperRuleV6Migration(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	const name = "[内置] 日志/审计记录修改（痕迹清理）"
	restore := func() {
		_, _ = d.Exec(`UPDATE intercept_rules SET pattern=$2, timeout_enabled=true, timeout_seconds=300, timeout_action='deny' WHERE name=$1`, name, logTamperPattern)
		_ = d.SetSetting("intercept_default_rules_v6", "true")
	}
	defer restore()
	if _, err := d.Exec(`UPDATE intercept_rules SET pattern=$2, timeout_enabled=false, timeout_seconds=60, timeout_action='deny' WHERE name=$1`, name, logTamperPatternV5); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("intercept_default_rules_v6", "false"); err != nil {
		t.Fatal(err)
	}
	if err := d.seedDefaultInterceptRulesV6(); err != nil {
		t.Fatal(err)
	}
	var pattern, action string
	var enabled bool
	var secs int
	if err := d.QueryRow(`SELECT pattern, timeout_enabled, timeout_seconds, timeout_action FROM intercept_rules WHERE name=$1`, name).Scan(&pattern, &enabled, &secs, &action); err != nil {
		t.Fatal(err)
	}
	if pattern != logTamperPattern || !enabled || secs != 300 || action != "deny" {
		t.Fatalf("v6 迁移未生效: enabled=%v secs=%d action=%s pattern_changed=%v", enabled, secs, action, pattern != logTamperPatternV5)
	}
}

// v8 收紧后的三条破坏性规则：只拦真正的系统目录/块设备，不再误伤 /tmp 与 /dev/null。
func TestDestructiveRulePrecision(t *testing.T) {
	cases := []struct {
		name string
		re   *regexp.Regexp
		hit  []string
		miss []string
	}{
		{
			name: "deleteSystemPathPattern",
			re:   regexp.MustCompile(deleteSystemPathPattern),
			hit:  []string{"rm -f /etc/passwd", "rm -f /var/www/html/x", "rm -rf /", "rm -f /boot/vmlinuz"},
			miss: []string{"rm -f /tmp/x", "rm -f /home/user/file", "rm -f ./local"},
		},
		{
			name: "ddDevicePattern",
			re:   regexp.MustCompile(ddDevicePattern),
			hit:  []string{"dd if=/dev/zero of=/dev/sda bs=1M", "dd if=x of=/dev/nvme0n1", "dd if=x of=/dev/mapper/vg-root"},
			miss: []string{"dd if=/dev/zero of=/dev/null bs=1M", "dd if=/dev/zero of=/dev/zero", "dd if=/dev/zero of=/dev/stdout"},
		},
		{
			name: "destructiveSystemPattern",
			re:   regexp.MustCompile(destructiveSystemPattern),
			hit:  []string{"rm -rf /", "mkfs.ext4 /dev/sdb1", ":(){ :|:& };:", "shutdown -h now", "echo x > /dev/sda"},
			miss: []string{"rm -rf /tmp/x", "dd if=/dev/zero of=/dev/null"},
		},
	}
	for _, c := range cases {
		for _, s := range c.hit {
			if !c.re.MatchString(s) {
				t.Errorf("%s 应命中: %q", c.name, s)
			}
		}
		for _, s := range c.miss {
			if c.re.MatchString(s) {
				t.Errorf("%s 不应命中: %q", c.name, s)
			}
		}
	}
}

// v8 迁移只改仍是出厂旧正则的规则；用户编辑过的规则不动。
func TestDefaultInterceptRuleV8Migration(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	type ruleCase struct{ name, old, want string }
	cases := []ruleCase{
		{"[内置] 日志/审计记录修改（痕迹清理）", logTamperPatternV6, logTamperPattern},
		{"[内置] 删除系统关键目录", deleteSystemPathPatternV1, deleteSystemPathPattern},
		{"[内置] 覆写磁盘设备 dd", ddDevicePatternV1, ddDevicePattern},
		{"[内置] 破坏性系统命令", destructiveSystemPatternV1, destructiveSystemPattern},
	}
	restore := func() {
		for _, c := range cases {
			_, _ = d.Exec(`UPDATE intercept_rules SET pattern=$2, updated_at=NOW() WHERE name=$1`, c.name, c.want)
		}
		_ = d.SetSetting("intercept_default_rules_v8", "true")
	}
	defer restore()
	// 用户改过的那条：不迁移。
	if _, err := d.Exec(`UPDATE intercept_rules SET pattern='user-edited-pattern' WHERE name=$1`, cases[1].name); err != nil {
		t.Fatal(err)
	}
	// 其余三条恢复为出厂旧值。
	for _, c := range cases {
		if c.old == deleteSystemPathPatternV1 {
			continue
		}
		if _, err := d.Exec(`UPDATE intercept_rules SET pattern=$2 WHERE name=$1`, c.name, c.old); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetSetting("intercept_default_rules_v8", "false"); err != nil {
		t.Fatal(err)
	}
	if err := d.seedDefaultInterceptRulesV8(); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := d.QueryRow(`SELECT pattern FROM intercept_rules WHERE name=$1`, cases[1].name).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "user-edited-pattern" {
		t.Fatalf("用户编辑过的规则被迁移: %q", got)
	}
	for _, c := range []ruleCase{cases[0], cases[2], cases[3]} {
		if err := d.QueryRow(`SELECT pattern FROM intercept_rules WHERE name=$1`, c.name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Fatalf("%s 未迁移到 v8 正则", c.name)
		}
	}
}
