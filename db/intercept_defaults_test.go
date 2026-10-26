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
