package intercept

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// P15 结构化裁决：概率在模型，决策在代码。
//
// 自由文本裁判的裁决完全由模型那句话决定——阈值、优先级、边界都是散文，改不动也
// 测不了。这里把裁判输出拆成两根：模型只给【各轴的把握】与一个【建议 choice】，
// 最终 allow/ask/deny 由下面这几个常量按固定规则算出，可被单测钉死在 0.54/0.55/0.85
// 这些边界上。
//
// 判定方向与本产品一致：只拦「难以恢复的真实破坏」；组织策略轴（若启用）优先于
// 渗透 payload 轴；拿不准就交人（ask 是本产品的保守方向，而不是默认放行）。

// 代码拥有的阈值（改这里就是改行为，且单测会跟着红）。
const (
	// NoulRejectThreshold 破坏性轴 / 组织策略轴任一把握 ≥ 此值 → deny。
	NoulRejectThreshold = 0.55
	// NoulChoiceRejectConf 模型 choice=deny 且置信度 ≥ 此值，且没有任何中高破坏分
	// （见 NoulDestructiveLow / NoulPentestLow）时 → deny。
	NoulChoiceRejectConf = 0.85
	// NoulDestructiveLow 破坏分的"低"界：低于它才算"没有破坏迹象"。
	NoulDestructiveLow = 0.35
	// NoulPentestLow 渗透 payload 分低于它，才允许模型的高置信 deny 生效
	// （即：一个明显的渗透动作不该被模型的 deny 砍掉）。
	NoulPentestLow = 0.5
	// NoulAskConf 模型 choice=ask 且置信度 ≥ 此值 → ask（交人）。
	NoulAskConf = 0.55
)

// 轴 id（模型必须用这些键；未知轴视为解析失败 → ask）。
const (
	AxisAvailability   = "destroys_availability"
	AxisDataLoss       = "irreversible_data_loss"
	AxisCredentials    = "credential_or_acl_change"
	AxisConfig         = "config_tamper"
	AxisPentest        = "authorized_pentest_payload"
	AxisOperatorPolicy = "violates_operator_policy"
)

// destructiveAxes 是"任一命中即拒绝"的轴，按严重度排序（只影响理由文案顺序）。
var destructiveAxes = []struct{ Axis, Label string }{
	{AxisAvailability, "破坏业务可用性"},
	{AxisDataLoss, "不可逆数据破坏"},
	{AxisCredentials, "账号权限篡改"},
	{AxisConfig, "改系统配置"},
}

// StructuredVerdict 是结构化裁判的一次输出。
type StructuredVerdict struct {
	Nouls      map[string]float64 `json:"nouls"`
	Choice     string             `json:"choice"` // allow | ask | deny
	ChoiceConf float64            `json:"choice_conf"`
	Comment    string             `json:"comment"`
}

// ParseStructuredVerdict 严格解析结构化裁决：字段固定、轴名固定、数值必须在 0–1。
// 任何不合规都返回错误——调用方按失败策略处理（默认 ask），绝不"猜一个动作"。
func ParseStructuredVerdict(text string) (StructuredVerdict, error) {
	// 用指针接这两个必填字段：Go 的 JSON 解码区分不了"缺字段"与"零值"，而
	// "缺 choice_conf" 恰恰是必须判失败的畸形输出（否则默认 0 分会被读成放行）。
	var raw struct {
		Nouls      map[string]float64 `json:"nouls"`
		Choice     string             `json:"choice"`
		ChoiceConf *float64           `json:"choice_conf"`
		Comment    string             `json:"comment"`
	}
	dec := json.NewDecoder(strings.NewReader(stripCodeFence(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return StructuredVerdict{}, fmt.Errorf("结构化裁决不是合法 JSON 对象: %w", err)
	}
	if dec.More() {
		return StructuredVerdict{}, fmt.Errorf("结构化裁决 JSON 之后还有多余内容")
	}
	if raw.Nouls == nil {
		return StructuredVerdict{}, fmt.Errorf("缺少 nouls 字段")
	}
	if raw.ChoiceConf == nil {
		return StructuredVerdict{}, fmt.Errorf("缺少 choice_conf 字段")
	}
	v := StructuredVerdict{Nouls: raw.Nouls, Choice: raw.Choice, ChoiceConf: *raw.ChoiceConf, Comment: raw.Comment}
	choice := strings.ToLower(strings.TrimSpace(v.Choice))
	switch choice {
	case "approve": // 兼容 approve/reject 措辞
		choice = "allow"
	case "reject":
		choice = "deny"
	}
	if choice != "allow" && choice != "ask" && choice != "deny" {
		return StructuredVerdict{}, fmt.Errorf("choice 必须是 allow/ask/deny，实际 %q", v.Choice)
	}
	v.Choice = choice
	if math.IsNaN(v.ChoiceConf) || v.ChoiceConf < 0 || v.ChoiceConf > 1 {
		return StructuredVerdict{}, fmt.Errorf("choice_conf 必须在 0–1，实际 %v", v.ChoiceConf)
	}
	for axis, conf := range v.Nouls {
		if !knownAxis(axis) {
			return StructuredVerdict{}, fmt.Errorf("未知裁决轴 %q", axis)
		}
		if math.IsNaN(conf) || conf < 0 || conf > 1 {
			return StructuredVerdict{}, fmt.Errorf("轴 %s 的把握必须在 0–1，实际 %v", axis, conf)
		}
	}
	return v, nil
}

func knownAxis(axis string) bool {
	switch axis {
	case AxisAvailability, AxisDataLoss, AxisCredentials, AxisConfig, AxisPentest, AxisOperatorPolicy:
		return true
	}
	return false
}

// DecideStructured 把结构化裁决映射成 allow/ask/deny —— 规则全部在代码里：
//
//  1. 任一破坏性轴 ≥ 0.55 → deny（组织策略轴同权，单独列出）
//  2. 模型 choice=deny 且 conf ≥ 0.85，且没有中高破坏分（破坏 < 0.35 且渗透 payload < 0.5，
//     或存在组织策略轴）→ deny
//  3. 模型 choice=ask 且 conf ≥ 0.55 → ask
//  4. 其余 → allow（没有破坏迹象时的产品默认方向）
//
// 返回的 reason 带全部数字，落 audit，供事后复盘"为什么这么判"。
func DecideStructured(v StructuredVerdict) (action, reason string) {
	conf := func(axis string) float64 { return v.Nouls[axis] }

	maxDestructive, topLabel := 0.0, ""
	var fired []string
	for _, a := range destructiveAxes {
		c := conf(a.Axis)
		if c > maxDestructive {
			maxDestructive, topLabel = c, a.Label
		}
		if c >= NoulRejectThreshold {
			fired = append(fired, fmt.Sprintf("%s=%.2f", a.Label, c))
		}
	}
	policy := conf(AxisOperatorPolicy)
	hasPolicy := false
	if _, ok := v.Nouls[AxisOperatorPolicy]; ok {
		hasPolicy = true
		if policy >= NoulRejectThreshold {
			fired = append(fired, fmt.Sprintf("违反组织审批策略=%.2f", policy))
		}
	}
	pentest := conf(AxisPentest)
	tail := fmt.Sprintf("choice=%s(%.2f)；最高破坏分=%.2f；渗透 payload=%.2f", v.Choice, v.ChoiceConf, maxDestructive, pentest)

	if len(fired) > 0 {
		sort.Strings(fired)
		return "deny", "命中破坏轴：" + strings.Join(fired, "；") + "；" + tail
	}
	if v.Choice == "deny" && v.ChoiceConf >= NoulChoiceRejectConf {
		switch {
		case hasPolicy:
			return "deny", fmt.Sprintf("模型高置信拒绝且存在组织策略轴（组织策略分=%.2f）；%s", policy, tail)
		case maxDestructive < NoulDestructiveLow && pentest < NoulPentestLow:
			return "deny", "模型高置信拒绝且无破坏性迹象；" + tail
		}
	}
	if v.Choice == "ask" && v.ChoiceConf >= NoulAskConf {
		return "ask", "模型建议转人工且置信度足够；" + tail
	}
	if topLabel != "" {
		return "allow", fmt.Sprintf("未命中破坏阈值（最高破坏轴=%s %.2f）；%s", topLabel, maxDestructive, tail)
	}
	return "allow", "未命中破坏阈值；" + tail
}

// StructuredVerdictJSON 把结构化裁决压缩成一行 JSON 存进 audit（复盘"模型当时怎么打的分"）。
func StructuredVerdictJSON(v StructuredVerdict) string {
	b, err := json.Marshal(map[string]any{
		"nouls": v.Nouls, "choice": v.Choice, "choice_conf": v.ChoiceConf, "comment": v.Comment,
	})
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err == nil {
		b = buf.Bytes()
	}
	if len(b) > 2048 {
		b = b[:2048]
	}
	return string(b)
}
