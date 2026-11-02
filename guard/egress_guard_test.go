package guard

import "testing"

// 直连防护只认**显式绕过代理**的写法；对本机的调试命令放行，对普通命令完全不介入。
func TestDetectProxyBypass(t *testing.T) {
	hit := []string{
		`curl --noproxy '*' https://target.example/admin`,
		`curl --no-proxy '*' http://target.example/`,
		`curl --noproxy=target.example https://target.example/`,
		`NO_PROXY=* curl -s https://target.example/`,
		`no_proxy=target.example curl -s https://target.example/`,
		`env -u HTTPS_PROXY curl https://target.example/`,
		`unset https_proxy; curl https://target.example/`,
		`HTTPS_PROXY='' curl https://target.example/`,
		`curl -x '' https://target.example/`,
	}
	for _, cmd := range hit {
		if reason, ok := detectProxyBypass(cmd); !ok || reason == "" {
			t.Fatalf("应识别为绕过代理：%s", cmd)
		}
	}
	safe := []string{
		`curl -s https://target.example/`,
		`curl --noproxy localhost http://127.0.0.1:8080/`,
		`curl --noproxy '*' http://127.0.0.1:8080/`,
		`NO_PROXY=localhost,127.0.0.1 curl -s https://target.example/`,
		`nmap -sV -p 1-1000 target.example`,
		`python3 exp.py`,
	}
	for _, cmd := range safe {
		if reason, ok := detectProxyBypass(cmd); ok {
			t.Fatalf("不该判为绕过代理（%s）：%s", reason, cmd)
		}
	}
}
