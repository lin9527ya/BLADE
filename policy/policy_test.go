package policy

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 词边界：ASCII 词的边界把 `.` 也算作词内字符——`hydra.example.com` 是合法目标域名，
// 不该因为含 "hydra" 被判成爆破；`hydra -l` 照常命中。中文词走子串。
func TestMatchWordsBoundaries(t *testing.T) {
	tech, _ := TechniqueByID("bruteforce")
	cases := []struct {
		text string
		want bool
		word string
	}{
		{"对 hydra.example.com 做指纹识别", false, ""},
		{"用 hydra 爆破登录口", true, "hydra"},
		{"跑一下 brute-force 字典", true, "brute-force"},
		{"因为 since 那次误判", false, ""},
		{"对登录口做弱口令测试", true, "弱口令"},
		{"检查账号是否有撞库风险", true, "撞库"},
	}
	for _, c := range cases {
		hit, ok := MatchWords(c.text, tech.Words)
		if ok != c.want {
			t.Fatalf("MatchWords(%q)=%v(%q), want %v", c.text, ok, hit.Matched, c.want)
		}
		if c.want && c.word != "" && hit.Matched != c.word {
			t.Fatalf("MatchWords(%q) matched %q, want %q", c.text, hit.Matched, c.word)
		}
	}
}

// 否定豁免：合规方向（"检查是否有爆破防护"）不该被拒——但会被标 Benign 留痕。
func TestCheckIntentNegationExemption(t *testing.T) {
	eff := Effective{Techniques: []string{"bruteforce"}}
	v := eff.CheckIntent(IntentInput{Summary: "检查登录接口是否有爆破防护措施"})
	if !v.Allow || !v.Benign {
		t.Fatalf("否定语境应放行并标 Benign: %+v", v)
	}
	v = eff.CheckIntent(IntentInput{Summary: "对登录接口做口令爆破，尝试常见弱口令"})
	if v.Allow || v.ItemID != "bruteforce" || v.Matched == "" || v.Hint == "" {
		t.Fatalf("正向提及应硬拒且带命中与建议: %+v", v)
	}
	// 拒绝理由必须让人/模型看懂"没入图 + 平台策略 + 怎么改"。
	text := RejectText(v, "对登录接口做口令爆破")
	for _, want := range []string{"未写入探索图", "口令爆破", "合规改写建议", "不要用近义措辞"} {
		if !strings.Contains(text, want) {
			t.Fatalf("拒绝理由缺少 %q：\n%s", want, text)
		}
	}
}

// 节奏面：每轮/在途/总量三档，各自独立可断言。
func TestCheckIntentPace(t *testing.T) {
	eff := Effective{MaxIntentsPerRound: 4, MaxOpenIntents: 12, MaxIntentsTotal: 60}
	cases := []struct {
		in   IntentInput
		want string
	}{
		{IntentInput{RoundUsed: 4}, "pace:round"},
		{IntentInput{OpenCount: 12}, "pace:open"},
		{IntentInput{TotalUsed: 60}, "pace:total"},
		{IntentInput{RoundUsed: 3, OpenCount: 5, TotalUsed: 10}, ""},
	}
	for _, c := range cases {
		v := eff.CheckIntent(c.in)
		if c.want == "" {
			if !v.Allow {
				t.Fatalf("%+v 应放行: %+v", c.in, v)
			}
			continue
		}
		if v.Allow || v.ItemID != c.want {
			t.Fatalf("%+v 应被 %s 拦: %+v", c.in, c.want, v)
		}
		if v.Hint == "" {
			t.Fatalf("%s 必须给出可操作提示", c.want)
		}
	}
	// 零策略 = 完全不管（行为与从前一致）。
	var zero Effective
	if !zero.IsZero() || !zero.CheckIntent(IntentInput{Summary: "对登录口爆破", TotalUsed: 999}).Allow {
		t.Fatal("零策略不该拦任何东西")
	}
}

// restrictivePolicy 是**旧的出厂默认**，只给测试用：默认放开后，规则编译/匹配/
// 提示词段仍需一组有内容的策略来验证机制本身。
func restrictivePolicy() Effective {
	return Effective{
		Version:            1,
		Source:             "global_default",
		Techniques:         []string{"bruteforce", "dos", "social_engineering", "webshell", "reverse_shell", "persistence"},
		ForbiddenWrites:    []string{"http_delete", "http_write_method", "mutation_endpoint", "db_write", "db_dump"},
		MaxIntentsPerRound: 4,
	}
}

// Merge：nil=继承、[]=显式清空、数值=覆盖（0=不限）。
func TestMergeSemantics(t *testing.T) {
	base := Default()
	empty := []string{}
	zero := 0
	twenty := 20

	inherit := Merge(base, Override{})
	if len(inherit.Techniques) != len(base.Techniques) || inherit.MaxIntentsPerRound != base.MaxIntentsPerRound {
		t.Fatalf("空覆盖层应完全继承: %+v", inherit)
	}
	cleared := Merge(base, Override{Techniques: &empty, ForbiddenWrites: &empty})
	if len(cleared.Techniques) != 0 || len(cleared.ForbiddenWrites) != 0 {
		t.Fatalf("空数组应显式清空: %+v", cleared)
	}
	replaced := Merge(base, Override{Techniques: &[]string{"dos"}, MaxIntentsPerRound: &zero})
	if len(replaced.Techniques) != 1 || replaced.Techniques[0] != "dos" {
		t.Fatalf("数组应整体替换而非并集: %+v", replaced.Techniques)
	}
	if replaced.MaxIntentsPerRound != 0 {
		t.Fatalf("数值 0 应表示不限: %d", replaced.MaxIntentsPerRound)
	}
	capped := Merge(base, Override{MaxIntentsTotal: &twenty})
	if capped.MaxIntentsTotal != 20 || capped.MaxIntentsPerRound != base.MaxIntentsPerRound {
		t.Fatalf("数值覆盖应只动那一项: %+v", capped)
	}
	// 未知 id：写入侧拒绝，读取侧忽略。
	if got := UnknownIDs(Effective{Techniques: []string{"bruteforce", "nope"}}); len(got) != 1 || got[0] != "nope" {
		t.Fatalf("未知 id 应被报出: %v", got)
	}
	if _, err := MarshalDoc(1, Override{}, Effective{Techniques: []string{"nope"}}); err == nil {
		t.Fatal("含未知 id 的策略不该能落库")
	}
	if eff := Normalize(Effective{Techniques: []string{"dos", "dos", "nope"}}); len(eff.Techniques) != 1 || eff.Techniques[0] != "dos" {
		t.Fatalf("Normalize 应去重并丢弃未知 id: %+v", eff.Techniques)
	}
}

// 文档往返：落库的 effective 就是执行点读到的（配置点与执行点不回头读全局设置）。
func TestDocRoundTrip(t *testing.T) {
	raw, err := MarshalDoc(3, Override{Techniques: &[]string{"dos"}}, Merge(Default(), Override{Techniques: &[]string{"dos"}}))
	if err != nil {
		t.Fatal(err)
	}
	doc := ParseDoc(raw)
	if doc == nil || doc.GlobalVersion != 3 || doc.Effective.Source != "task" {
		t.Fatalf("文档往返丢了字段: %+v", doc)
	}
	if doc.Override.Techniques == nil || len(*doc.Override.Techniques) != 1 {
		t.Fatalf("override 必须原样保留（UI 靠它区分继承/显式）: %+v", doc.Override)
	}
	if ParseDoc(nil) != nil || ParseDoc([]byte("  ")) != nil || ParseDoc([]byte("{bad")) != nil {
		t.Fatal("空/坏文档应视为无策略")
	}
	// 全局默认：空 → 出厂默认；坏 JSON → 出厂默认。
	eff, ver := ParseGlobalDefault("")
	if eff.Source != "global_default" || ver != 1 || eff.IsZero() {
		t.Fatalf("空设置应给出厂默认: %+v v=%d", eff, ver)
	}
	if eff, _ := ParseGlobalDefault("{bad"); eff.IsZero() {
		t.Fatal("坏设置应回退出厂默认而不是空策略")
	}
}

// CompileRules：每条 pattern 必须能编译、长度合规、条数有上限。
func TestCompileRulesValid(t *testing.T) {
	rules := restrictivePolicy().CompileRules()
	if len(rules) == 0 || len(rules) > MaxPolicyRules {
		t.Fatalf("规则条数异常: %d", len(rules))
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			t.Fatalf("%s 的 pattern 编译失败: %v", r.ItemID, err)
		}
		if len(r.Pattern) > maxPolicyPatternLen {
			t.Fatalf("%s 的 pattern 过长: %d", r.ItemID, len(r.Pattern))
		}
		if r.Action != "deny" || r.MatchTarget != "tool_input" || r.MatchType != "regex" {
			t.Fatalf("%s 规则形态不对: %+v", r.ItemID, r)
		}
		if !strings.Contains(r.Message, "任务探索策略") {
			t.Fatalf("%s 的文案必须点名是平台的任务策略（否则模型会当成目标侧防御去绕过）", r.ItemID)
		}
		if seen[r.ItemID] {
			t.Fatalf("%s 生成了多条规则", r.ItemID)
		}
		seen[r.ItemID] = true
	}
	for _, want := range []string{"bruteforce", "dos", "db_write", "db_dump", "http_delete"} {
		if !seen[want] {
			t.Fatalf("默认策略少了 %s 的规则", want)
		}
	}
}

// 规则行为表：真写要拦、读代码/搜关键字/域名不能误拦——这是整条命令门的成败所在。
func TestCompileRulesMatching(t *testing.T) {
	byItem := map[string]string{}
	for _, r := range restrictivePolicy().CompileRules() {
		byItem[r.ItemID] = r.Pattern
	}
	match := func(item, subject string) bool {
		t.Helper()
		re, err := regexp.Compile(byItem[item])
		if err != nil {
			t.Fatalf("%s: %v", item, err)
		}
		return re.MatchString(subject)
	}
	cases := []struct {
		item, subject string
		want          bool
	}{
		// 真写 → 拦
		{"db_write", `mysql -u root -p pw -e "UPDATE users SET role='admin' WHERE id=1"`, true},
		{"db_write", `curl -s http://t/api/q -d "q=1'; DELETE FROM users-- -"`, true},
		{"db_dump", `sqlmap -u http://t/a.php?id=1 --dump`, true},
		{"db_dump", `mysqldump -u root db > dump.sql`, true},
		{"http_delete", `curl -X DELETE http://t/api/user/1`, true},
		{"http_write_method", `curl -X PUT -d 'a=b' http://t/api/user/1`, true},
		{"http_write_method", `curl --request PATCH http://t/api/user/1`, true},
		{"mutation_endpoint", `curl http://t/api/user/update?id=1`, true},
		{"mutation_endpoint", `curl -X POST http://t/api/user/create -d name=x`, true},
		{"bruteforce", `hydra -l admin -P pw.txt http://t/login`, true},
		{"bruteforce", `对登录口做弱口令测试`, true},
		// 读代码 / 搜关键字 / 域名 / 只读接口 → 不拦
		{"db_write", `grep -rn "DELETE FROM" ./src/`, false},
		{"db_write", `sed -n 's/UPDATE/UPDATE_/g' schema.sql`, false},
		{"db_write", `mysql -e "SELECT id FROM users LIMIT 1"`, false},
		{"db_delete", `curl http://t/api/user/1`, false},
		{"http_write_method", `curl -X POST http://t/login -d 'u=a&p=b'`, false},
		{"bruteforce", `curl http://hydra.example.com/`, false},
		// 注：否定豁免只作用于**语义层**（意图门），命令层按形状硬拦——所以
		// "检查是否有爆破防护"这句若出现在命令里会被拦（一次工具调用 + 明确理由），
		// 这条取舍在 policy/vocab.go 的注释与 docs/探索策略.md 里写明。
		{"mutation_endpoint", `curl http://t/api/users/newest`, false},
		{"mutation_endpoint", `curl http://t/api/user/list`, false},
	}
	for _, c := range cases {
		pattern, ok := byItem[c.item]
		if !ok {
			continue // 策略里没有这一项时跳过（例如 db_delete 只是防笔误）
		}
		if got := match(c.item, c.subject); got != c.want {
			t.Fatalf("%s 对 %q = %v, want %v\npattern=%s", c.item, c.subject, got, c.want, pattern)
		}
	}
}

// 提示词段：边界、硬底线改写、被拒摘要三块都必须说人话且包含关键出口。
func TestRenderBlockAndHardFloor(t *testing.T) {
	eff := restrictivePolicy()
	block := RenderBlock(eff)
	for _, want := range []string{"任务探索策略", "禁用手段", "敏感写操作", "被策略排除的方向不是", "产出 0 条意图", "口令爆破", "脱库"} {
		if !strings.Contains(block, want) {
			t.Fatalf("策略段缺少 %q：\n%s", want, block)
		}
	}
	if RenderBlock(Effective{}) != "" {
		t.Fatal("零策略不该注入任何段")
	}
	if HardFloorText(Effective{}) == HardFloorText(eff) {
		t.Fatal("有策略时硬底线必须被改写（否则提示词自相矛盾）")
	}
	if !strings.Contains(HardFloorText(eff), "策略内") {
		t.Fatal("硬底线改写应说明'只在策略内成立'")
	}
	notes := []RejectNote{{Summary: "对登录口做口令爆破", ItemLabel: "口令爆破/认证猜测", ItemID: "bruteforce", Matched: "爆破"}}
	stall := StallNoticeText(notes, eff)
	for _, want := range []string{"上一轮被探索策略拒绝", "口令爆破", "产出 0 条意图"} {
		if !strings.Contains(stall, want) {
			t.Fatalf("被拒摘要缺少 %q：\n%s", want, stall)
		}
	}
	if StallNoticeText(nil, eff) != "" {
		t.Fatal("没有被拒记录时不该注入摘要")
	}
}

// 自定义条目（操作者手写）：关键词走词边界/子串并享受否定豁免，正则原样匹配不豁免，
// command 层只进命令门；写入侧 Validate 拒绝非法正则、超长、超量与总规则数超限。
func TestCustomRules(t *testing.T) {
	base := Effective{Techniques: []string{"bruteforce"}}

	// ① 关键词：意图层拦、留痕可读、编译成命令门规则。
	eff := Merge(base, Override{Custom: &[]CustomRule{{Label: "别碰这个后台", Pattern: "wp-admin"}}})
	v := eff.CheckIntent(IntentInput{Summary: "扫一下 http://t/wp-admin/ 下的插件"})
	if v.Allow || v.ItemLabel != "自定义·别碰这个后台" || v.Matched != "wp-admin" {
		t.Fatalf("自定义关键词没拦住: %+v", v)
	}
	if !strings.Contains(RejectText(v, "扫一下 wp-admin"), "wp-admin") {
		t.Fatal("拒绝理由必须带命中片段")
	}
	// 否定语境与词表同款豁免（"检查 wp-admin 是否有弱口令"是合规评估方向）。
	if bv := eff.CheckIntent(IntentInput{Summary: "检查是否有 wp-admin 目录暴露"}); !bv.Allow || !bv.Benign {
		t.Fatalf("否定语境应放行但留痕: %+v", bv)
	}
	// 词边界：域名里的 wp-admin 也该命中（它就是那个路径），但不该命中无关词。
	if av := eff.CheckIntent(IntentInput{Summary: "看一眼 oldwp-admins 这个资产"}); !av.Allow {
		t.Fatalf("不该命中词内嵌的近似串: %+v", av)
	}
	rules := eff.CompileRules()
	found := false
	for _, r := range rules {
		if r.ItemID == "custom" {
			found = true
			if !strings.Contains(r.Name, "别碰这个后台") {
				t.Fatalf("规则名应带自定义标签: %s", r.Name)
			}
			if !regexp.MustCompile(r.Pattern).MatchString("curl http://t/wp-admin/admin.php") {
				t.Fatalf("自定义规则匹配不上: %s", r.Pattern)
			}
		}
	}
	if !found {
		t.Fatal("自定义条目没编译出规则")
	}

	// ② 正则：按原文匹配（大小写不敏感），不套否定豁免。
	re := Merge(base, Override{Custom: &[]CustomRule{{Label: "内部接口", Pattern: `/internal/(?:admin|debug)`, Regex: true}}})
	if rv := re.CheckIntent(IntentInput{Summary: "访问 /internal/admin/users 看权限"}); rv.Allow {
		t.Fatalf("自定义正则没拦住: %+v", rv)
	}
	if bv := re.CheckIntent(IntentInput{Summary: "检查是否存在 /internal/admin 的鉴权"}); bv.Allow {
		t.Fatalf("正则条目不套否定豁免（操作者写的是精确形状）: %+v", bv)
	}
	if ok := re.CheckIntent(IntentInput{Summary: "访问 /internal/list 看权限"}); !ok.Allow {
		t.Fatalf("不该命中未匹配的正则: %+v", ok)
	}

	// ③ command 层：意图措辞不判，但命令门照样拦。
	cmd := Merge(base, Override{Custom: &[]CustomRule{{Label: "生产库", Pattern: "prod-db.internal", Layer: CustomLayerCommand}}})
	if cv := cmd.CheckIntent(IntentInput{Summary: "连一下 prod-db.internal 看版本"}); !cv.Allow {
		t.Fatalf("command 层条目不该判意图: %+v", cv)
	}
	if !strings.Contains(RenderBlock(cmd), "prod-db.internal") || !strings.Contains(RenderBlock(cmd), "只判命令") {
		t.Fatal("提示词段必须写明 command 层条目的作用面")
	}
	hit := false
	for _, r := range cmd.CompileRules() {
		if r.ItemID == "custom" && regexp.MustCompile(r.Pattern).MatchString("nmap prod-db.internal") {
			hit = true
		}
	}
	if !hit {
		t.Fatal("command 层条目必须进命令门")
	}

	// ④ Validate：非法正则 / 超长 / 超量 / 总规则数超限都要拒绝（写不进去要说清楚）。
	if err := Validate(Merge(base, Override{Custom: &[]CustomRule{{Pattern: "(unclosed", Regex: true}}})); err == nil || !strings.Contains(err.Error(), "不是合法正则") {
		t.Fatalf("非法正则必须被拒: %v", err)
	}
	if err := Validate(Merge(base, Override{Custom: &[]CustomRule{{Pattern: "(unclosed"}}})); err != nil {
		t.Fatalf("关键词类型下括号是普通字符，不该按正则校验: %v", err)
	}
	if err := Validate(Merge(base, Override{Custom: &[]CustomRule{{Pattern: strings.Repeat("a", MaxCustomPatternLen+1)}}})); err == nil {
		t.Fatal("超长条目必须被拒")
	}
	many := make([]CustomRule, 0, MaxCustomRules+1)
	for i := 0; i <= MaxCustomRules; i++ {
		many = append(many, CustomRule{Pattern: "rule-" + strconv.Itoa(i)})
	}
	if err := Validate(Merge(base, Override{Custom: &many})); err == nil || !strings.Contains(err.Error(), "最多") {
		t.Fatalf("超量必须被拒: %v", err)
	}
	// 总规则数是硬上限：词表项 + 自定义条目都必须落在里面（加词表项时这条会红，
	// 提醒同步抬高 MaxPolicyRules 或压低 MaxCustomRules）。
	if total := len(Techniques) + len(WriteItems) + MaxCustomRules; total > MaxPolicyRules {
		t.Fatalf("上限算术被破坏：词表 %d + 写 %d + 自定义 %d = %d > %d",
			len(Techniques), len(WriteItems), MaxCustomRules, total, MaxPolicyRules)
	}
	// 去重后合法：重复条目不该被算两次。
	dup := []CustomRule{{Pattern: "dup"}, {Pattern: "dup"}, {Pattern: " dup "}}
	if err := Validate(Merge(base, Override{Custom: &dup})); err != nil {
		t.Fatalf("重复条目去重后应合法: %v", err)
	}
	if n := len(Normalize(Effective{Custom: dup}).Custom); n != 1 {
		t.Fatalf("去重后应剩 1 条，实际 %d", n)
	}
	// 空条目不进策略（也不该让策略变"非零"）。
	if z := Merge(Effective{}, Override{Custom: &[]CustomRule{{Pattern: "  "}}}); !z.IsZero() {
		t.Fatalf("空白条目应被丢弃: %+v", z)
	}
}

// 自定义条目的落库往返：override 与 effective 两侧都要保住（UI 编辑的是 override）。
func TestCustomRulesDocRoundTrip(t *testing.T) {
	eff := Merge(Effective{}, Override{Custom: &[]CustomRule{{Label: "L", Pattern: "p", Layer: CustomLayerCommand}}})
	raw, err := MarshalDoc(3, Override{Custom: &[]CustomRule{{Label: "L", Pattern: "p", Layer: CustomLayerCommand}}}, eff)
	if err != nil {
		t.Fatal(err)
	}
	doc := ParseDoc(raw)
	if doc == nil || len(doc.Effective.Custom) != 1 || doc.Effective.Custom[0].Pattern != "p" {
		t.Fatalf("effective 侧丢了: %s", raw)
	}
	if doc.Override.Custom == nil || len(*doc.Override.Custom) != 1 || (*doc.Override.Custom)[0].Layer != CustomLayerCommand {
		t.Fatalf("override 侧丢了: %s", raw)
	}
	if doc.Override.IsZero() {
		t.Fatal("有自定义条目时 override 不该算'空'（否则 UI 会显示成继承）")
	}
}

// TestFactoryDefaultIsPermissive 钉住产品决策：出厂默认不再限制任何手段。
// 需要只读验证的场景（SRC 等）应显式收紧，而不是让所有人先去掉限制。
func TestFactoryDefaultIsPermissive(t *testing.T) {
	eff := Default()
	if len(eff.Techniques) != 0 || len(eff.ForbiddenWrites) != 0 || len(eff.Custom) != 0 {
		t.Fatalf("出厂默认不应禁用任何手段: %+v", eff)
	}
	if rules := eff.CompileRules(); len(rules) != 0 {
		t.Fatalf("出厂默认不应编译出任何 deny 规则，实际 %d 条", len(rules))
	}
	for _, summary := range []string{"上传 webshell 并反弹 shell", "用 sqlmap 跑注入并脱库", "对登录口做口令爆破"} {
		if v := eff.CheckIntent(IntentInput{Summary: summary}); !v.Allow {
			t.Fatalf("出厂默认下意图应放行（%q），实际被拒: %+v", summary, v)
		}
	}
}
