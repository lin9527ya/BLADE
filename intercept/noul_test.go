package intercept

import (
	"strings"
	"testing"
)

// TestStructuredThresholds pins the 0.54/0.55 and 0.85 boundaries — the whole point
// of P15 is that these live in code and are testable, not in prompt prose.
func TestStructuredThresholds(t *testing.T) {
	cases := []struct {
		name   string
		v      StructuredVerdict
		action string
	}{
		{
			name:   "破坏轴 0.54（低于阈值）+ choice=allow → 放行",
			v:      StructuredVerdict{Nouls: map[string]float64{AxisAvailability: 0.54}, Choice: "allow", ChoiceConf: 0.99},
			action: "allow",
		},
		{
			name:   "破坏轴 0.55（正好到阈值）→ 拒绝",
			v:      StructuredVerdict{Nouls: map[string]float64{AxisAvailability: 0.55}, Choice: "allow", ChoiceConf: 0.99},
			action: "deny",
		},
		{
			name:   "破坏性轴 0.9 但模型建议 approve → 仍拒绝（概率在模型，决策在代码）",
			v:      StructuredVerdict{Nouls: map[string]float64{AxisDataLoss: 0.9}, Choice: "allow", ChoiceConf: 0.99},
			action: "deny",
		},
		{
			name: "全轴 <0.35 + 渗透轴 0.8 + choice=reject conf 0.9 → 放行（明显的渗透动作不该被模型砍掉）",
			v: StructuredVerdict{
				Nouls:      map[string]float64{AxisPentest: 0.8, AxisAvailability: 0.1, AxisDataLoss: 0.2},
				Choice:     "deny",
				ChoiceConf: 0.90,
			},
			action: "allow",
		},
		{
			name: "choice=reject conf 0.85 且无破坏、无渗透动作 → 拒绝",
			v: StructuredVerdict{
				Nouls:      map[string]float64{AxisPentest: 0.1, AxisAvailability: 0.1},
				Choice:     "deny",
				ChoiceConf: 0.85,
			},
			action: "deny",
		},
		{
			name: "choice=reject conf 0.84（差一点）→ 放行",
			v: StructuredVerdict{
				Nouls:      map[string]float64{AxisPentest: 0.1, AxisAvailability: 0.1},
				Choice:     "deny",
				ChoiceConf: 0.84,
			},
			action: "allow",
		},
		{
			name:   "组织策略轴 0.7 → 拒绝（优先于渗透轴）",
			v:      StructuredVerdict{Nouls: map[string]float64{AxisOperatorPolicy: 0.7, AxisPentest: 0.9}, Choice: "allow", ChoiceConf: 0.9},
			action: "deny",
		},
		{
			name:   "choice=ask conf 0.55 → 转人工",
			v:      StructuredVerdict{Nouls: map[string]float64{AxisAvailability: 0.2}, Choice: "ask", ChoiceConf: 0.55},
			action: "ask",
		},
		{
			name:   "choice=ask conf 0.54 → 放行（低于阈值不打扰人）",
			v:      StructuredVerdict{Nouls: map[string]float64{AxisAvailability: 0.2}, Choice: "ask", ChoiceConf: 0.54},
			action: "allow",
		},
	}
	for _, c := range cases {
		action, reason := DecideStructured(c.v)
		if action != c.action {
			t.Fatalf("%s：action=%s want %s（reason=%s）", c.name, action, c.action, reason)
		}
		if reason == "" {
			t.Fatalf("%s：必须给出可复盘的判定理由", c.name)
		}
	}
}

// TestParseStructuredVerdictStrict pins the parse contract: anything malformed is an
// error (the caller then follows the fail action, default ask) — never a guessed
// action. Fences and approve/reject aliases are the only tolerated deviations.
func TestParseStructuredVerdictStrict(t *testing.T) {
	ok := `{"nouls":{"destroys_availability":0.1,"authorized_pentest_payload":0.9},"choice":"allow","choice_conf":0.8,"comment":"只读探测"}`
	v, err := ParseStructuredVerdict(ok)
	if err != nil || v.Choice != "allow" || v.Nouls[AxisPentest] != 0.9 {
		t.Fatalf("合法输入被拒: %+v err=%v", v, err)
	}
	// 代码块包裹 + approve/reject 别名：容忍。
	v, err = ParseStructuredVerdict("```json\n{\"nouls\":{},\"choice\":\"reject\",\"choice_conf\":0.9,\"comment\":\"x\"}\n```")
	if err != nil || v.Choice != "deny" {
		t.Fatalf("reject 别名应映射为 deny: %+v err=%v", v, err)
	}
	for name, bad := range map[string]string{
		"空回复":           "",
		"散文":            "我认为可以放行",
		"未知 choice":     `{"nouls":{},"choice":"maybe","choice_conf":0.5,"comment":"x"}`,
		"未知轴":           `{"nouls":{"whatever":0.9},"choice":"allow","choice_conf":0.9,"comment":"x"}`,
		"把握越界":          `{"nouls":{"destroys_availability":1.5},"choice":"allow","choice_conf":0.9,"comment":"x"}`,
		"置信度越界":         `{"nouls":{},"choice":"allow","choice_conf":-0.1,"comment":"x"}`,
		"缺 choice":      `{"nouls":{},"choice_conf":0.9,"comment":"x"}`,
		"缺 choice_conf": `{"nouls":{},"choice":"allow","comment":"x"}`,
		"缺 nouls":       `{"choice":"allow","choice_conf":0.9,"comment":"x"}`,
		"多出未知字段":        `{"nouls":{},"choice":"allow","choice_conf":0.9,"comment":"x","extra":1}`,
		"JSON 后有多余内容":   `{"nouls":{},"choice":"allow","choice_conf":0.9,"comment":"x"} 谢谢`,
	} {
		if _, err := ParseStructuredVerdict(bad); err == nil {
			t.Fatalf("%s：必须解析失败（随后走失败策略）", name)
		}
	}
}

// TestJudgeDefaultsAreFailSafeAndStructured pins the two P15 defaults against
// silent regression: structured verdicts ON, and a model failure lands on ask
// (not the old allow) — "配了裁判" must not degrade into a silent pass-through.
func TestJudgeDefaultsAreFailSafeAndStructured(t *testing.T) {
	if defaultJudgeFailAction != "ask" {
		t.Fatalf("裁判失败默认动作必须是 ask（失效安全），实际 %q", defaultJudgeFailAction)
	}
	if !defaultJudgeStructured {
		t.Fatal("结构化裁决必须默认开启")
	}
	// 结构化协议必须显式声明"替代前文任何输出格式要求"，否则用户保存过旧协议时
	// 模型会收到两份互相打架的格式要求。
	if !strings.Contains(JudgeStructuredContract, "替代前文任何输出格式要求") {
		t.Fatal("结构化协议必须声明替代旧格式")
	}
	p := EffectiveJudgePromptFor("自定义策略正文", true)
	if !strings.Contains(p, "自定义策略正文") || !strings.Contains(p, JudgeStructuredContract) || !strings.Contains(p, JudgeContextBoundary) {
		t.Fatal("结构化模式下必须同时保留用户策略、信任边界与结构化协议")
	}
	if strings.Contains(p, JudgeOutputContract) {
		t.Fatal("结构化模式下不得再追加自由文本协议")
	}
	// 自由文本模式保持原样（旧协议的 back-compat）。
	p2 := EffectiveJudgePromptFor("自定义策略正文", false)
	if !strings.Contains(p2, JudgeOutputContract) || strings.Contains(p2, JudgeStructuredContract) {
		t.Fatal("自由文本模式必须只用旧协议")
	}
}
