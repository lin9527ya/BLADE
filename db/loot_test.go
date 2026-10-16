package db

import (
	"encoding/json"
	"testing"
)

// 凭据的可利用面（P36）：指纹要稳、要不可反推、要区分得开；目标合并不能让"验证过"被抹掉。

func TestCredentialFingerprintStableAndOpaque(t *testing.T) {
	a := CredentialFingerprint("password", "wp_user", "s3cret")
	b := CredentialFingerprint("Password", " wp_user ", "s3cret") // 大小写/空白归一
	if a != b {
		t.Errorf("同一条凭据该得到同一个指纹：%q vs %q", a, b)
	}
	if len(a) != 8 {
		t.Errorf("指纹该是 8 位十六进制：%q", a)
	}
	if CredentialFingerprint("password", "wp_user", "s3cret2") == a {
		t.Error("本体不同该得到不同指纹（否则指认不成立）")
	}
	if CredentialFingerprint("password", "other", "s3cret") == a {
		t.Error("账号不同该得到不同指纹")
	}
	if CredentialFingerprint("password", "wp_user", "") != "" {
		t.Error("没有本体就不该有指纹：一个看起来像指纹的空值会让人以为它在指认某条凭据")
	}
	if a == "s3cret" || len(a) >= len("s3cret")*2 {
		t.Errorf("指纹不该是明文本身或其可逆形式：%q", a)
	}
}

func TestParseCredentialToleratesJunk(t *testing.T) {
	var n *Node
	if v := ParseCredential(n); v.Secret != "" || v.Targets != nil {
		t.Errorf("nil 节点该得到空视图：%+v", v)
	}
	bad := &Node{Kind: KindCredential, Payload: json.RawMessage(`{not json`)}
	if v := ParseCredential(bad); v.Kind != "" {
		t.Errorf("畸形 payload 该当作空字段，而不是让读路径整段失败：%+v", v)
	}
}

func TestMergeCredentialTargetNeverDowngrades(t *testing.T) {
	list := MergeCredentialTarget(nil, CredentialTarget{
		Host: "db01", Port: 3306, Service: "mysql", Verified: true, Evidence: "登录成功",
	})
	// 第二次只登记目标、没验证：不能把已验证抹掉（"我上次真的进去了"是既成事实）。
	list = MergeCredentialTarget(list, CredentialTarget{Host: "DB01", Port: 3306, Service: "MySQL"})
	if len(list) != 1 {
		t.Fatalf("同 (host,port,service) 该合并成一条：%+v", list)
	}
	if !list[0].Verified || list[0].Evidence != "登录成功" {
		t.Errorf("已验证不该被未验证的记录抹掉：%+v", list[0])
	}
	// 不同端口是不同目标（同一台机器上 3306 与 22 是两条可利用面）。
	list = MergeCredentialTarget(list, CredentialTarget{Host: "db01", Port: 22, Service: "ssh"})
	if len(list) != 2 {
		t.Errorf("端口不同该算两条目标：%+v", list)
	}
	// 空 host 直接丢弃，不产生一条无主目标。
	list = MergeCredentialTarget(list, CredentialTarget{Host: "   "})
	if len(list) != 2 {
		t.Errorf("空 host 该被丢弃：%+v", list)
	}
	SortCredentialTargets(list)
	if !list[0].Verified {
		t.Errorf("排序该把已验证的排前面：%+v", list)
	}
}

func TestCredentialTargetLabel(t *testing.T) {
	cases := []struct {
		t    CredentialTarget
		want string
	}{
		{CredentialTarget{Host: "db01"}, "db01"},
		{CredentialTarget{Host: "db01", Port: 3306}, "db01:3306"},
		{CredentialTarget{Host: "db01", Port: 3306, Service: "mysql"}, "db01:3306（mysql）"},
		{CredentialTarget{Host: "db01", Service: "smb"}, "db01（smb）"},
	}
	for _, c := range cases {
		if got := c.t.Label(); got != c.want {
			t.Errorf("Label() = %q，想要 %q", got, c.want)
		}
	}
}
