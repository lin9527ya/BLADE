package policy

import (
	"encoding/json"
	"fmt"
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

	// 节奏面：0 = 不限。
	MaxIntentsPerRound int `json:"max_intents_per_round"`
	MaxOpenIntents     int `json:"max_open_intents"`
	MaxIntentsTotal    int `json:"max_intents_total"`
}

// IsZero 报告这份策略是否什么都没限制（执行点据此完全跳过策略逻辑）。
func (e Effective) IsZero() bool {
	return len(e.Techniques) == 0 && len(e.ForbiddenWrites) == 0 &&
		e.MaxIntentsPerRound <= 0 && e.MaxOpenIntents <= 0 && e.MaxIntentsTotal <= 0
}

// Override 是任务自己的覆盖层：**指针字段**让"没填"（继承）与"显式清空"可区分——
// 数组 nil = 继承全局、[] = 显式不禁；数值 nil = 继承、0 = 不限。
type Override struct {
	Techniques         *[]string `json:"techniques,omitempty"`
	ForbiddenWrites    *[]string `json:"forbidden_writes,omitempty"`
	MaxIntentsPerRound *int      `json:"max_intents_per_round,omitempty"`
	MaxOpenIntents     *int      `json:"max_open_intents,omitempty"`
	MaxIntentsTotal    *int      `json:"max_intents_total,omitempty"`
}

// IsZero 报告覆盖层是否什么都没改（全 nil）——用于区分"这个任务是显式配过策略"还是
// "创建时把全局默认抄了一份快照"。两者落库形状相同，语义不同（UI 要显示"继承自全局默认"）。
func (o Override) IsZero() bool {
	return o.Techniques == nil && o.ForbiddenWrites == nil && o.MaxIntentsPerRound == nil &&
		o.MaxOpenIntents == nil && o.MaxIntentsTotal == nil
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
	e.MaxIntentsPerRound = max0(e.MaxIntentsPerRound)
	e.MaxOpenIntents = max0(e.MaxOpenIntents)
	e.MaxIntentsTotal = max0(e.MaxIntentsTotal)
	return e
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
