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
	miss := []string{
		"cat /var/log/auth.log",
		"grep -i failed /var/log/auth.log",
		"curl http://target/delivery",
		"SELECT * FROM audit_log",
		"ls /var/log/nginx/",
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("不应命中日志修改审批规则: %q", s)
		}
	}
}
