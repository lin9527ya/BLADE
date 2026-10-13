package intercept

import (
	"strings"
	"testing"
)

// deny 约束里的目标（域名/IP/URL 主机）会被确定性抽出来，一条目标一条 deny 规则。
func TestCompileConstraintExtractsTargets(t *testing.T) {
	d := CompileConstraint(7, "deny", "禁止访问 admin.example.com 与 10.0.0.5 这两个管理入口")
	if len(d.Rules) != 2 {
		t.Fatalf("抽到的规则数 = %d，want 2（%+v；note=%s）", len(d.Rules), d.Rules, d.Note)
	}
	var patterns []string
	for _, r := range d.Rules {
		patterns = append(patterns, r.Pattern)
		if r.Action != "deny" || r.MatchTarget != "tool_input" || r.MatchType != "string" {
			t.Fatalf("规则形态不对: %+v", r)
		}
		if !strings.Contains(r.Message, "任务操作约束 #7") {
			t.Fatalf("规则理由必须能追回约束原文: %q", r.Message)
		}
	}
	joined := strings.Join(patterns, ",")
	if !strings.Contains(joined, "admin.example.com") || !strings.Contains(joined, "10.0.0.5") {
		t.Fatalf("目标没抽全: %v", patterns)
	}
	if !strings.Contains(d.Note, "只对本任务生效") {
		t.Fatalf("note 应说明作用域: %s", d.Note)
	}
}

// 点名的命令行工具会编译成 tool_input 字符串规则；词边界要挡住 "since" 里的 "nc"。
func TestCompileConstraintToolLexicon(t *testing.T) {
	d := CompileConstraint(3, "deny", "禁止使用 sqlmap 打这个站")
	if len(d.Rules) != 1 || d.Rules[0].Pattern != "sqlmap" {
		t.Fatalf("工具名未编译: %+v（note=%s）", d.Rules, d.Note)
	}
	if got := detectToolMentions("这条约束从 since 那时起就存在"); len(got) != 0 {
		t.Fatalf("词边界失效，抽出了 %v", got)
	}
	if got := detectToolMentions("用 nmap -sV 扫"); len(got) != 1 || got[0] != "nmap" {
		t.Fatalf("正常工具名应识别: %v", got)
	}
}

// 抽不出东西的约束：如实说明，绝不硬造规则。
func TestCompileConstraintVagueIsHonest(t *testing.T) {
	d := CompileConstraint(9, "deny", "禁止对生产数据库做任何写操作")
	if len(d.Rules) != 0 {
		t.Fatalf("不该硬造规则: %+v", d.Rules)
	}
	if !strings.Contains(d.Note, "无法确定性编译") {
		t.Fatalf("应如实说明抽不出可编译项: %s", d.Note)
	}
}

// allow 约束编译成授权范围条目（不进规则），kind 按目标形态定。
func TestCompileConstraintAllowScope(t *testing.T) {
	d := CompileConstraint(11, "allow", "允许范围包含 10.0.0.0/24、10.1.1.1 与 api.example.com")
	if len(d.Rules) != 0 {
		t.Fatalf("allow 约束不该生成拦截规则: %+v", d.Rules)
	}
	kinds := map[string]string{}
	for _, sc := range d.Scope {
		kinds[sc.Value] = sc.Kind
	}
	if kinds["10.0.0.0/24"] != "cidr" || kinds["10.1.1.1"] != "ip" || kinds["api.example.com"] != "root_domain" {
		t.Fatalf("范围 kind 不对: %+v", d.Scope)
	}
	if !strings.Contains(d.Note, "授权范围") {
		t.Fatalf("note 应说明进的是授权范围: %s", d.Note)
	}
	// 空约束不炸。
	if d := CompileConstraint(1, "deny", "   "); len(d.Rules) != 0 || d.Note == "" {
		t.Fatalf("空约束应给出说明: %+v", d)
	}
}
