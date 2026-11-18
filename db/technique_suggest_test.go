package db

import "testing"

func TestSuggestTechniques(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"用 nmap 做端口扫描", "T1046"},
		{"hydra 弱口令爆破", "T1110"},
		{"sqlmap 验证 sql注入", "T1190"},
		{"kerberoast 拿 SPN 票据", "T1558.003"},
		{"没有关键词的探索", ""},
	}
	for _, c := range cases {
		got := SuggestTechniques(c.text)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%q: want none, got %v", c.text, got)
			}
			continue
		}
		found := false
		for _, tech := range got {
			if tech == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want %s in %v", c.text, c.want, got)
		}
	}
}
