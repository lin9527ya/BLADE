package intercept

import (
	"encoding/json"
	"testing"
)

func TestArgvPrefixAndMatching(t *testing.T) {
	prefix, err := ArgvPrefix("git push origin main")
	if err != nil || len(prefix) != 2 || prefix[0] != "git" || prefix[1] != "push" {
		t.Fatalf("prefix=%v err=%v", prefix, err)
	}
	in, _ := json.Marshal(map[string]any{"command": "git push origin main"})
	if !argvRuleMatches([]string{"git", "push"}, "Bash", in) {
		t.Fatal("git push should match")
	}
	if argvRuleMatches([]string{"git", "push"}, "Bash", []byte(`{"command":"git push; rm -rf /"}`)) {
		t.Fatal("compound command must not match argv")
	}
	if _, err := ArgvPrefix("git -C /repo push"); err == nil {
		t.Fatal("a flag before the subcommand must not yield a prefix")
	}
	if _, err := ArgvPrefix("nmap x; rm -rf /"); err == nil {
		t.Fatal("compound command must be refused")
	}
	if _, err := ParseArgvPattern(`["git","push"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseArgvPattern(`["git","-C"]`); err == nil {
		t.Fatal("flags are not valid prefix tokens")
	}
}

func TestAlwaysAllowPrefixRefusesDangerousPrograms(t *testing.T) {
	if p, err := AlwaysAllowPrefix("git push origin main"); err != nil || p != "git push" {
		t.Fatalf("git prefix=%q err=%v", p, err)
	}
	for _, cmd := range []string{"python3.11 -c 'print(1)'", "sudo rm -rf /", "tar --checkpoint-action=exec=sh x", "sh -c id"} {
		if p, err := AlwaysAllowPrefix(cmd); err == nil {
			t.Fatalf("%q should be refused, got %q", cmd, p)
		}
	}
}
