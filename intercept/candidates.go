package intercept

// P12 遮挡面（对齐 CyberStrikeAI toolguard.candidateTexts 的思路）：
// 规则此前只对「整段 tool_input JSON 原文」做一次匹配——`%65%76%69%6c` 这类
// 百分号编码的目标、嵌在参数值里的目标、甚至编码进 JSON key 的目标，都能静默
// 绕过 deny 规则。candidateTexts 把一次工具调用展开成规则可逐条判定的候选集：
//
//   - 每个嵌套 string 值（表面 nested_string）与每个 map key（nested_key）；
//   - 每条候选最多再做 3 轮百分号解码（表面 percent_decoded:N）——自写解码器，
//     混杂串里的孤立 % 不破坏其余部分，且不做递归其它编码（避免解码炸弹）；
//   - 最后补整段原文（表面 raw），保证既有「匹配整段 JSON」的规则行为不回归。
//
// 去重按文本（首次出现的表面胜出）；解码仅对 ≤32KB 的候选做（防放大）。
// 方向是「宁误报勿漏报」：key 遍历会让规则更容易命中，dry-run 端点供校准。

import (
	"encoding/json"
	"strconv"
	"strings"
)

type candidate struct {
	Text    string
	Surface string // raw | nested_string | nested_key | percent_decoded:N
}

const (
	candidateDecodeRounds = 3
	candidateDecodeMaxLen = 32 * 1024
)

func candidateTexts(input []byte) []candidate {
	seen := make(map[string]struct{}, 8)
	out := make([]candidate, 0, 8)
	add := func(text, surface string) {
		if text == "" {
			return
		}
		if _, ok := seen[text]; ok {
			return
		}
		seen[text] = struct{}{}
		out = append(out, candidate{Text: text, Surface: surface})
		if len(text) > candidateDecodeMaxLen {
			return
		}
		cur := text
		for round := 1; round <= candidateDecodeRounds; round++ {
			next := decodePercentEscapes(cur)
			if next == cur {
				break
			}
			surface := "percent_decoded:" + strconv.Itoa(round)
			if _, ok := seen[next]; !ok {
				seen[next] = struct{}{}
				out = append(out, candidate{Text: next, Surface: surface})
			}
			cur = next
		}
	}

	var v any
	if err := json.Unmarshal(input, &v); err == nil {
		walkCandidates(v, add)
	}
	add(string(input), "raw") // 旧行为兜底：整段原文仍然参与匹配
	return out
}

func walkCandidates(v any, add func(text, surface string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			add(k, "nested_key")
			walkCandidates(vv, add)
		}
	case []any:
		for _, vv := range t {
			walkCandidates(vv, add)
		}
	case string:
		add(t, "nested_string")
	}
}

// decodePercentEscapes 逐 %XX 三元组解码；非法或不完整的三元组原样保留，
// 混杂串里孤立的 % 不破坏其余部分的解码。
func decodePercentEscapes(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := hexVal(s[i+1])
			lo, ok2 := hexVal(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
