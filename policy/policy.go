package policy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Effective 是一份**解析完成**的策略：执行点只读它，绝不回头读全局设置。
// 这样"全局默认被改了"与"运行中的任务"之间不存在竞态——任务落库时就把
// effective 快照写好了。
type Effective struct {
	// Version 是策略文档版本（每次变更 +1，进被拒记录与审计）。
	Version int `json:"version"`
	// Source 标明这份 effective 从哪来：global_default | task。
	Source string `json:"source"`

	// Techniques 是禁用的手段 id（policy.Techniques 的 ID）。
	Techniques []string `json:"techniques"`
	// ForbiddenWrites 是禁止的写操作 id（policy.WriteItems 的 ID）。
	ForbiddenWrites []string `json:"forbidden_writes"`
	// Custom 是操作者手写的额外禁止条目（词表的逃生舱；数量与形状由 Validate 把关）。
	Custom []CustomRule `json:"custom,omitempty"`

	// 节奏面：0 = 不限。
	MaxIntentsPerRound int `json:"max_intents_per_round"`
	MaxOpenIntents     int `json:"max_open_intents"`
	MaxIntentsTotal    int `json:"max_intents_total"`
}

// IsZero 报告这份策略是否什么都没限制（执行点据此完全跳过策略逻辑）。
func (e Effective) IsZero() bool {
	return len(e.Techniques) == 0 && len(e.ForbiddenWrites) == 0 && len(e.Custom) == 0 &&
		e.MaxIntentsPerRound <= 0 && e.MaxOpenIntents <= 0 && e.MaxIntentsTotal <= 0
}

// 自定义条目的生效层。
const (
	// CustomLayerBoth：意图层与命令层都判（默认）——"这个方向别提，也别做"。
	CustomLayerBoth = "both"
	// CustomLayerCommand：只编译进命令门——用于"说起来没关系、做出来才有事"的形状
	// （例如某个只读接口的写路径），避免按措辞误伤意图。
	CustomLayerCommand = "command"
)

// CustomRule 是操作者手写的一条边界条目（词表覆盖不到的"这个别碰"由它兜）。
// 它是**禁止**语义（本策略只做限制，不做白名单），判定规则：
//   - Regex=false：关键词。ASCII 走词边界（`.` 视为词内字符），中文走子串；与词表同款
//     否定语境豁免（"检查是否有 X"这类合规方向放行但留痕）。
//   - Regex=true：真正则（大小写不敏感），原样进命令门；意图层按原文匹配、不套否定豁免
//     （操作者写的是精确形状，不该被语义豁免掉）。
type CustomRule struct {
	// Label 是显示名（拒绝理由与规则名里用它）；空则显示 Pattern。
	Label string `json:"label,omitempty"`
	// Pattern 是匹配内容：关键词或正则。
	Pattern string `json:"pattern"`
	// Regex=true 时 Pattern 按正则解释。
	Regex bool `json:"regex,omitempty"`
	// Layer：both（默认）| command。
	Layer string `json:"layer,omitempty"`
}

// Display 返回这条自定义条目的显示名。
func (r CustomRule) Display() string {
	if l := strings.TrimSpace(r.Label); l != "" {
		return l
	}
	return r.Pattern
}

// IsCommandOnly 报告这条是否只作用于命令层。
func (r CustomRule) IsCommandOnly() bool { return r.Layer == CustomLayerCommand }

// Override 是任务自己的覆盖层：**指针字段**让"没填"（继承）与"显式清空"可区分——
// 数组 nil = 继承全局、[] = 显式不禁；数值 nil = 继承、0 = 不限。
type Override struct {
	Techniques         *[]string     `json:"techniques,omitempty"`
	ForbiddenWrites    *[]string     `json:"forbidden_writes,omitempty"`
	Custom             *[]CustomRule `json:"custom,omitempty"`
	MaxIntentsPerRound *int          `json:"max_intents_per_round,omitempty"`
	MaxOpenIntents     *int          `json:"max_open_intents,omitempty"`
	MaxIntentsTotal    *int          `json:"max_intents_total,omitempty"`
}

// IsZero 报告覆盖层是否什么都没改（全 nil）——用于区分"这个任务是显式配过策略"还是
// "创建时把全局默认抄了一份快照"。两者落库形状相同，语义不同（UI 要显示"继承自全局默认"）。
func (o Override) IsZero() bool {
	return o.Techniques == nil && o.ForbiddenWrites == nil && o.Custom == nil &&
		o.MaxIntentsPerRound == nil && o.MaxOpenIntents == nil && o.MaxIntentsTotal == nil
}

// Doc 是落库的完整策略文档：override（UI 编辑的那层）+ effective（解析结果）+
// 解析自哪一版全局默认。存两份是刻意的——UI 要能回答"这项我是继承来的还是显式
// 清空的"，而执行点只认 effective。
type Doc struct {
	DocVersion    int       `json:"doc_version"`
	GlobalVersion int       `json:"global_version"`
	Override      Override  `json:"override"`
	Effective     Effective `json:"effective"`
}

// DocVersion 是当前文档结构版本。
const DocVersion = 1

// Default 是随平台出厂的全局默认策略：SRC 常态是"只读验证 + 不碰高危手段"。
// 用户可在设置页改；**已存在的任务不回溯**（它们各自的 effective 已快照）。
func Default() Effective {
	return Effective{
		Version:            1,
		Source:             "global_default",
		Techniques:         []string{"bruteforce", "dos", "social_engineering", "webshell", "reverse_shell", "persistence"},
		ForbiddenWrites:    []string{"http_delete", "http_write_method", "mutation_endpoint", "db_write", "db_dump"},
		MaxIntentsPerRound: 4, // 与 planner 提示词"最多 4 个"同一口径
	}
}

// ParseGlobalDefault 解析设置里的全局默认（空/坏 → 出厂默认）。
func ParseGlobalDefault(raw string) (Effective, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Default(), 1
	}
	var stored struct {
		Version   int       `json:"version"`
		Effective Effective `json:"effective"`
		Override  Override  `json:"override"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return Default(), 1
	}
	eff := Default()
	if stored.Effective.Source != "" || !stored.Effective.IsZero() {
		eff = stored.Effective
	} else {
		eff = Merge(Default(), stored.Override)
	}
	if stored.Version > 0 {
		eff.Version = stored.Version
	}
	eff.Source = "global_default"
	return Normalize(eff), eff.Version
}

// Merge 用覆盖层解析出最终策略：nil 字段继承 base，显式空数组清空，数值 0 = 不限。
func Merge(base Effective, ov Override) Effective {
	out := base
	if ov.Techniques != nil {
		out.Techniques = append([]string(nil), (*ov.Techniques)...)
	}
	if ov.ForbiddenWrites != nil {
		out.ForbiddenWrites = append([]string(nil), (*ov.ForbiddenWrites)...)
	}
	if ov.Custom != nil {
		out.Custom = append([]CustomRule(nil), (*ov.Custom)...)
	}
	if ov.MaxIntentsPerRound != nil {
		out.MaxIntentsPerRound = *ov.MaxIntentsPerRound
	}
	if ov.MaxOpenIntents != nil {
		out.MaxOpenIntents = *ov.MaxOpenIntents
	}
	if ov.MaxIntentsTotal != nil {
		out.MaxIntentsTotal = *ov.MaxIntentsTotal
	}
	return Normalize(out)
}

// Normalize 去重、排序、丢弃未知 id、把负数夹到 0，并给出**未知 id 列表**供
// 写入侧拒绝（读取侧只忽略，向前兼容旧文档）。
func Normalize(e Effective) Effective {
	e.Techniques, _ = normalizeIDs(e.Techniques, func(id string) bool { _, ok := techniqueByID[id]; return ok })
	e.ForbiddenWrites, _ = normalizeIDs(e.ForbiddenWrites, func(id string) bool { _, ok := writeItemByID[id]; return ok })
	e.Custom = normalizeCustom(e.Custom)
	e.MaxIntentsPerRound = max0(e.MaxIntentsPerRound)
	e.MaxOpenIntents = max0(e.MaxOpenIntents)
	e.MaxIntentsTotal = max0(e.MaxIntentsTotal)
	return e
}

// Validate 是写入侧的唯一把关口：未知 id、自定义条目的合法性（正则可编译、长度）、
// 数量上限、以及"总规则数不超上限"都在这里拒绝——**写不进去要说清楚，不静默丢**
// （静默丢会让操作者以为边界生效了，实际没有）。
func Validate(e Effective) error {
	if unknown := UnknownIDs(e); len(unknown) > 0 {
		return fmt.Errorf("策略里有未知条目：%s", strings.Join(unknown, "、"))
	}
	n := Normalize(e)
	if len(n.Custom) > MaxCustomRules {
		return fmt.Errorf("自定义禁止条目最多 %d 条（当前 %d 条）", MaxCustomRules, len(n.Custom))
	}
	for _, r := range n.Custom {
		if len(r.Pattern) > MaxCustomPatternLen {
			return fmt.Errorf("自定义条目「%s」太长（上限 %d 字符）", r.Display(), MaxCustomPatternLen)
		}
		if r.Regex {
			if _, err := regexp.Compile(r.Pattern); err != nil {
				return fmt.Errorf("自定义条目「%s」不是合法正则：%v", r.Display(), err)
			}
		}
	}
	if total := len(n.Techniques) + len(n.ForbiddenWrites) + len(n.Custom); total > MaxPolicyRules {
		return fmt.Errorf("规则总条数超上限（%d > %d）：手段 %d + 敏感写 %d + 自定义 %d",
			total, MaxPolicyRules, len(n.Techniques), len(n.ForbiddenWrites), len(n.Custom))
	}
	return nil
}

// UnknownIDs 返回词表里不存在的 id（写入侧据此 400 拒绝，而不是静默丢弃）。
func UnknownIDs(e Effective) []string {
	var out []string
	for _, id := range e.Techniques {
		if _, ok := techniqueByID[id]; !ok {
			out = append(out, id)
		}
	}
	for _, id := range e.ForbiddenWrites {
		if _, ok := writeItemByID[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// normalizeCustom 归一自定义条目：去空白、丢空条目、层归一（未知层→both）、按
// (pattern,regex,layer) 去重并按 Pattern 排序（顺序稳定，便于 diff 与落库比较）。
// **不在这里做数量/合法性拒绝**——那是 Validate 的事：写入侧要 400 说明白，不能静默丢。
func normalizeCustom(in []CustomRule) []CustomRule {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]CustomRule, 0, len(in))
	for _, r := range in {
		r.Pattern = strings.TrimSpace(r.Pattern)
		r.Label = strings.TrimSpace(r.Label)
		if r.Pattern == "" {
			continue
		}
		if r.Layer != CustomLayerCommand {
			r.Layer = CustomLayerBoth
		}
		key := fmt.Sprintf("%s|%t|%s", r.Pattern, r.Regex, r.Layer)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].Layer < out[j].Layer
	})
	return out
}

func normalizeIDs(ids []string, known func(string) bool) ([]string, []string) {
	seen := map[string]bool{}
	var out, dropped []string
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if !known(id) {
			dropped = append(dropped, id)
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, dropped
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// Labels 把技术 id 列表渲染成中文标签（UI 与提示词共用）。
func TechniqueLabels(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if t, ok := techniqueByID[id]; ok {
			out = append(out, t.Label)
		}
	}
	return out
}

// WriteLabels 把写操作 id 列表渲染成中文标签。
func WriteLabels(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if w, ok := writeItemByID[id]; ok {
			out = append(out, w.Label)
		}
	}
	return out
}

// MarshalDoc 生成落库的文档。
func MarshalDoc(globalVersion int, ov Override, eff Effective) (json.RawMessage, error) {
	// 先查未知 id 再归一化：Normalize 会把未知项丢掉，丢掉之后再查就永远查不出来了
	// （写入侧必须拒绝而不是静默丢弃——否则用户以为勾上了、其实没生效）。
	if unknown := UnknownIDs(eff); len(unknown) > 0 {
		return nil, fmt.Errorf("策略里有未知条目：%s", strings.Join(unknown, "、"))
	}
	eff.Source = "task"
	eff = Normalize(eff)
	doc := Doc{DocVersion: DocVersion, GlobalVersion: globalVersion, Override: ov, Effective: eff}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// MarshalSnapshot 生成"完全继承全局默认"的任务快照：没有覆盖层，Effective.Source 保留
// global_default。任务仍持有这份快照（而不是每次回头读设置），因为**运行中的任务不能被
// 另一处改了全局默认而悄悄改变边界**。
func MarshalSnapshot(globalVersion int, eff Effective) (json.RawMessage, error) {
	eff.Source = "global_default"
	eff = Normalize(eff)
	doc := Doc{DocVersion: DocVersion, GlobalVersion: globalVersion, Effective: eff}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ParseDoc 解析任务落库的策略文档；空/坏 → nil（= 无策略，行为与从前完全一致）。
func ParseDoc(raw []byte) *Doc {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	eff := Normalize(doc.Effective)
	// 列上做了 COALESCE(...,'{}')，所以"没有策略"读出来是空文档而不是 NULL——这里把
	// "空文档"归一成 nil，让所有调用方只处理一种"无策略"形状（写入侧也从不为空策略落库）。
	if eff.IsZero() && doc.Override.IsZero() {
		return nil
	}
	if eff.Source == "" {
		eff.Source = "task"
	}
	doc.Effective = eff
	return &doc
}
