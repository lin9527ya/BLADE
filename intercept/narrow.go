package intercept

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// P15 改参放行（只许收窄）。
//
// 审批时人可以对参数做修改后再放行——但"放行一份被改过的参数"是全新的一次执行，
// 必须比原来更保守，否则就绕过了整套裁决。Narrow 用纯函数把这条不变量钉死：
//
//	收窄后的参数 ⊆ 原参数（键只减不增；字符串只许保序删字符；数值只许变小；
//	布尔只许 true→false；数组只许变短且不得换项；嵌套对象同规则）
//	且 目标集合 ⊆ 原目标集合（复用 ExtractTargetsDetailed，防换 host）
//
// 任何一项越界 → 拒绝改参（调用方应退回"原样放行 / 拒绝"两条路）。这是安全属性，
// 不是最佳努力：宁可让人只用原始参数，也不放行一个没被审过的调用。

// NarrowOutcome 是一次改参校验的结果。Diff 逐项说明改了什么（进 audit 的
// narrow_diff），越界时第一项是"禁止：…"。
type NarrowOutcome struct {
	Input json.RawMessage
	Diff  []string
}

// Narrow 校验 edited 是否是 original 的合法收窄。返回 ok=false 时 Input 为空，
// Diff 说明第一处越界原因。
func Narrow(toolName string, original, edited json.RawMessage) (NarrowOutcome, bool) {
	var orig, edit any
	if err := decodeJSONObject(original, &orig); err != nil {
		return NarrowOutcome{Diff: []string{"禁止：原参数不是 JSON 对象（" + err.Error() + "）"}}, false
	}
	if err := decodeJSONObject(edited, &edit); err != nil {
		return NarrowOutcome{Diff: []string{"禁止：修改后的参数不是 JSON 对象（" + err.Error() + "）"}}, false
	}
	var diff []string
	if ok, reason := narrowValue("", orig, edit, &diff); !ok {
		return NarrowOutcome{Diff: append([]string{"禁止：" + reason}, diff...)}, false
	}
	// 目标集合必须是子集：字符串收窄挡不住"换一个同长度的 host"，这里显式比目标。
	origTargets := narrowTargets(toolName, original)
	editTargets := narrowTargets(toolName, edited)
	for _, t := range editTargets {
		if !containsString(origTargets, t) {
			return NarrowOutcome{Diff: []string{"禁止：修改后的目标 " + t + " 不在原参数的目标集合内"}}, false
		}
	}
	if len(editTargets) < len(origTargets) {
		diff = append(diff, fmt.Sprintf("目标由 %d 个收窄为 %d 个", len(origTargets), len(editTargets)))
	}
	if len(diff) == 0 {
		diff = append(diff, "参数与原值一致（未改动）")
	}
	canonical, err := json.Marshal(edit)
	if err != nil {
		return NarrowOutcome{Diff: []string{"禁止：修改后的参数无法序列化"}}, false
	}
	return NarrowOutcome{Input: canonical, Diff: diff}, true
}

func decodeJSONObject(raw json.RawMessage, out *any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("空参数")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if _, ok := (*out).(map[string]any); !ok {
		return fmt.Errorf("不是对象")
	}
	return nil
}

// narrowValue 递归校验"编辑后 ⊆ 原值"，并把每处收窄写进 diff。
func narrowValue(path string, orig, edit any, diff *[]string) (bool, string) {
	switch o := orig.(type) {
	case map[string]any:
		e, ok := edit.(map[string]any)
		if !ok {
			return false, label(path) + " 原本是对象，改后不是"
		}
		for k := range e {
			if _, exists := o[k]; !exists {
				return false, label(path) + " 新增了键 " + k
			}
		}
		keys := make([]string, 0, len(o))
		for k := range o {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ev, kept := e[k]
			if !kept {
				*diff = append(*diff, "删除键 "+join(path, k))
				continue
			}
			if ok, reason := narrowValue(join(path, k), o[k], ev, diff); !ok {
				return false, reason
			}
		}
		return true, ""
	case string:
		e, ok := edit.(string)
		if !ok {
			return false, label(path) + " 原本是字符串，改后不是"
		}
		if e == o {
			return true, ""
		}
		if !isSubsequence(e, o) {
			return false, label(path) + " 的字符串只能删减（保序删除原字符），不能新增、改写或重排"
		}
		*diff = append(*diff, "收窄 "+label(path)+"："+clipRunes(o, 40)+" → "+clipRunes(e, 40))
		return true, ""
	case json.Number:
		e, ok := edit.(json.Number)
		if !ok {
			return false, label(path) + " 原本是数值，改后不是"
		}
		of, err1 := o.Float64()
		ef, err2 := e.Float64()
		if err1 != nil || err2 != nil {
			return false, label(path) + " 的数值无法比较"
		}
		if ef == of {
			return true, ""
		}
		// 只许变小：绝对值不得增大；且不得借改符号换语义（0 除外）。
		if math.Abs(ef) > math.Abs(of) || (of != 0 && ef != 0 && math.Signbit(ef) != math.Signbit(of)) {
			return false, label(path) + " 的数值只能变小"
		}
		*diff = append(*diff, fmt.Sprintf("下调 %s：%v → %v", label(path), o, e))
		return true, ""
	case bool:
		e, ok := edit.(bool)
		if !ok {
			return false, label(path) + " 原本是布尔，改后不是"
		}
		if e == o {
			return true, ""
		}
		if e {
			return false, label(path) + " 的布尔只能 true→false（不能打开被关掉的开关）"
		}
		*diff = append(*diff, "关闭 "+label(path))
		return true, ""
	case []any:
		e, ok := edit.([]any)
		if !ok {
			return false, label(path) + " 原本是数组，改后不是"
		}
		if len(e) > len(o) {
			return false, label(path) + " 的数组只能变短"
		}
		for _, item := range e {
			if !containsJSON(o, item) {
				return false, label(path) + " 的数组只能删除原元素，不能替换成新元素"
			}
		}
		if len(e) < len(o) {
			*diff = append(*diff, fmt.Sprintf("缩短 %s：%d 项 → %d 项", label(path), len(o), len(e)))
		}
		return true, ""
	case nil:
		if edit != nil {
			return false, label(path) + " 原本是空值，不能改成有值"
		}
		return true, ""
	default:
		return false, label(path) + " 的类型不支持改参"
	}
}

// isSubsequence 判断 edited 是否是 original 的保序子序列：允许删字符（含整段参数），
// 不允许新增、改写或重排。对命令字符串而言这就是"只能削、不能加"的可执行定义。
func isSubsequence(edited, original string) bool {
	if edited == "" {
		return true
	}
	e, o := []rune(edited), []rune(original)
	i := 0
	for _, r := range o {
		if i < len(e) && e[i] == r {
			i++
			if i == len(e) {
				return true
			}
		}
	}
	return i == len(e)
}

// narrowTargets 抽出本次调用涉及的网络目标（与 RoE 层同一套抽取器）。shell 类工具
// 取命令字段，其余工具退回整段参数文本——宁可多抽，也不漏一个被换掉的 host。
func narrowTargets(toolName string, raw json.RawMessage) []string {
	text := shellCommandOf(toolName, raw)
	if strings.TrimSpace(text) == "" {
		text = string(raw)
	}
	out := []string{}
	for _, h := range ExtractTargetsDetailed(text) {
		if !containsString(out, h.Value) {
			out = append(out, h.Value)
		}
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// containsJSON 按规范化 JSON 比较（对象键序无关）。
func containsJSON(list []any, v any) bool {
	want := canonicalJSON(v)
	for _, item := range list {
		if canonicalJSON(item) == want {
			return true
		}
	}
	return false
}

func canonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func label(path string) string {
	if path == "" {
		return "参数"
	}
	return "参数 " + path
}
