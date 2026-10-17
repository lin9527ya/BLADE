package opsec

import "testing"

func TestCredentialAttempt(t *testing.T) {
	cases := []struct {
		cmd     string
		ok      bool
		host    string
		account string
		spray   bool
	}{
		// 爆破器：一次调用 = 成批尝试
		{cmd: `hydra -l admin -P /usr/share/wordlists/rockyou.txt ssh://10.0.0.5`,
			ok: true, host: "10.0.0.5", account: "admin", spray: true},
		{cmd: `hydra -L users.txt -p Passw0rd 10.0.0.5 ssh`,
			ok: true, host: "10.0.0.5", account: "", spray: true},
		{cmd: `kerbrute passwordspray -d corp.local users.txt Passw0rd`,
			ok: true, host: "corp.local", spray: true},
		{cmd: `medusa -h 10.0.0.5 -u root -P rockyou.txt ssh`,
			ok: true, host: "10.0.0.5", account: "root", spray: true},

		// nxc：单口令 = 一次尝试；带表 = 成批
		{cmd: `nxc smb 10.0.0.5 -u svc_backup -p 'P@ssw0rd'`,
			ok: true, host: "10.0.0.5", account: "svc_backup", spray: false},
		{cmd: `netexec smb 10.0.0.5 -u users.txt -p passwords.txt`,
			ok: true, host: "10.0.0.5", account: "users.txt", spray: true},
		{cmd: `crackmapexec winrm 10.0.0.6 --passwords /tmp/list.txt`,
			ok: true, host: "10.0.0.6", spray: true},

		// 单次凭据使用
		{cmd: `sshpass -p 'toor' ssh deploy@10.0.0.7`,
			ok: true, host: "10.0.0.7", account: "deploy", spray: false},
		{cmd: `impacket-psexec corp.local/administrator:P@ss:word@10.0.0.5`,
			ok: true, host: "10.0.0.5", account: "administrator", spray: false},
		{cmd: `evil-winrm -i 10.0.0.5 -u Administrator -p pass`,
			ok: true, host: "10.0.0.5", account: "Administrator", spray: false},
		{cmd: `xfreerdp /v:10.0.0.5 /u:admin /p:pass`,
			ok: true, host: "10.0.0.5", account: "admin", spray: false},
		{cmd: `rdesktop -u admin -p pass 10.0.0.5`,
			ok: true, host: "10.0.0.5", account: "admin", spray: false},
		{cmd: `smbclient //10.0.0.5/C$ -U 'CORP\admin%pass'`,
			ok: true, host: "10.0.0.5", account: "admin", spray: false},
		{cmd: `mssqlclient.py corp/admin:pass@10.0.0.5`,
			ok: true, host: "10.0.0.5", account: "admin", spray: false},
		{cmd: `curl -u admin:hunter2 http://10.0.0.9/admin`,
			ok: true, host: "10.0.0.9", account: "admin", spray: false},
		{cmd: `wget --user=ops --password=x http://10.0.0.9/f`,
			ok: true, host: "10.0.0.9", account: "ops", spray: false},
		{cmd: `psql postgres://app:secret@db01:5432/prod`,
			ok: true, host: "db01", account: "app", spray: false},
		{cmd: `mysql -u root -p'toor' -h db01`,
			ok: true, host: "db01", account: "root", spray: false},
		{cmd: `redis-cli -h cache01 -a secret`,
			ok: true, host: "cache01", spray: false},

		// 认不出来就明说认不出来（不猜、不假装覆盖）
		{cmd: `ssh deploy@10.0.0.7`, ok: false},        // 裸 ssh 可能是密钥，不算试口令
		{cmd: `curl http://10.0.0.9/`, ok: false},      // 没有 -u
		{cmd: `mysql -u root -p`, ok: false},           // 交互式提示口令，无从判定
		{cmd: `nc -zv 10.0.0.5 22`, ok: false},         // 端口探测，不是试凭据
		{cmd: `nmap -sV 10.0.0.5`, ok: false},          //
		{cmd: `hydra -l admin -P list.txt`, ok: false}, // 认得出工具、认不出目标
		{cmd: `ls -la /tmp`, ok: false},                //
		{cmd: ``, ok: false},                           //
	}
	for _, c := range cases {
		at, ok := CredentialAttempt(c.cmd)
		if ok != c.ok {
			t.Errorf("%q: ok=%v，期望 %v", c.cmd, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if at.Host != c.host {
			t.Errorf("%q: host=%q，期望 %q", c.cmd, at.Host, c.host)
		}
		if at.Account != c.account {
			t.Errorf("%q: account=%q，期望 %q", c.cmd, at.Account, c.account)
		}
		if at.Spray != c.spray {
			t.Errorf("%q: spray=%v，期望 %v", c.cmd, at.Spray, c.spray)
		}
	}
}
