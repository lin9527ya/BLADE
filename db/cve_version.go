package db

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// CVE 版本约束：单组 AND 约束（与 cve_library 的"一组产品/一条约束"一致）。
// 支持 >= > <= < = == !=、逗号分隔的 AND、~ ^ 以及 1.2.x / 1.x 通配。
// 不支持 OR（`||`）——不同分支应拆成不同 CVE 条目，而不是在一条约束里猜。
type cveVersionClause struct {
	op      string
	version string
}

type VersionConstraint struct {
	Raw     string
	Any     bool
	Clauses []cveVersionClause
}

var (
	reVersionCore   = regexp.MustCompile(`\d+(?:\.\d+)*`)
	reSimpleClause  = regexp.MustCompile(`^(==|=|>=|<=|>|<|!=)?\s*v?([0-9][0-9A-Za-z.\-+_]*)\s*$`)
	reWildcardParts = regexp.MustCompile(`(?i)^v?([0-9]+(?:\.[0-9]+)*)\.(?:x|\*)$`)
)

func ParseVersionConstraint(raw string) (VersionConstraint, error) {
	c := VersionConstraint{Raw: strings.TrimSpace(raw)}
	if c.Raw == "" || c.Raw == "*" {
		c.Any = true
		return c, nil
	}
	if strings.Contains(c.Raw, "||") {
		return c, fmt.Errorf("版本约束不支持 OR（||）；请拆成多条 CVE")
	}
	for _, part := range strings.Split(c.Raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		clauses, err := parseVersionClause(part)
		if err != nil {
			return c, err
		}
		c.Clauses = append(c.Clauses, clauses...)
	}
	if len(c.Clauses) == 0 {
		c.Any = true
	}
	return c, nil
}

func parseVersionClause(part string) ([]cveVersionClause, error) {
	part = strings.TrimSpace(part)
	if part == "" {
		return nil, nil
	}
	switch part[0] {
	case '~':
		return tildeClauses(strings.TrimSpace(part[1:]))
	case '^':
		return caretClauses(strings.TrimSpace(part[1:]))
	}
	// 1.2.x / 1.x / 1.2.*
	if m := reWildcardParts.FindStringSubmatch(strings.TrimPrefix(part, "v")); m != nil {
		parts, err := versionInts(m[1])
		if err != nil {
			return nil, err
		}
		return wildcardClauses(parts)
	}
	m := reSimpleClause.FindStringSubmatch(part)
	if m == nil {
		return nil, fmt.Errorf("无效版本约束: %s", part)
	}
	core := versionCore(m[2])
	if core == "" {
		return nil, fmt.Errorf("无效版本: %s", part)
	}
	op := m[1]
	if op == "==" {
		op = "="
	}
	if op == "" {
		op = "="
	}
	return []cveVersionClause{{op: op, version: core}}, nil
}

func tildeClauses(raw string) ([]cveVersionClause, error) {
	parts, err := versionInts(raw)
	if err != nil {
		return nil, err
	}
	// ~1.2.3 → >=1.2.3, <1.3.0; ~1.2 → >=1.2.0, <1.3.0; ~1 → >=1.0.0, <2.0.0
	idx := 1
	if len(parts) == 1 {
		idx = 0
	}
	lower := formatVersion(parts)
	upper := bumpVersion(parts, idx)
	return []cveVersionClause{{op: ">=", version: lower}, {op: "<", version: upper}}, nil
}

func caretClauses(raw string) ([]cveVersionClause, error) {
	parts, err := versionInts(raw)
	if err != nil {
		return nil, err
	}
	// ^1.2.3 → <2.0.0；^0.2.3 → <0.3.0；^0.0.3 → <0.0.4；全零 → <0.0.1
	idx := -1
	for i, p := range parts {
		if p != 0 {
			idx = i
			break
		}
	}
	upper := "0.0.1"
	if idx >= 0 {
		upper = bumpVersion(parts, idx)
	}
	return []cveVersionClause{{op: ">=", version: formatVersion(parts)}, {op: "<", version: upper}}, nil
}

func wildcardClauses(parts []int) ([]cveVersionClause, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("通配版本需要至少一个数字段")
	}
	lower := formatVersion(parts)
	upper := bumpVersion(parts, len(parts)-1)
	return []cveVersionClause{{op: ">=", version: lower}, {op: "<", version: upper}}, nil
}

func versionCore(s string) string {
	return reVersionCore.FindString(strings.TrimSpace(s))
}

func versionInts(raw string) ([]int, error) {
	core := versionCore(raw)
	if core == "" {
		return nil, fmt.Errorf("版本没有数字段: %s", raw)
	}
	parts := strings.Split(core, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("无效版本: %s", raw)
		}
		out = append(out, n)
	}
	return out, nil
}

func formatVersion(parts []int) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strconv.Itoa(p)
	}
	return strings.Join(out, ".")
}

// bumpVersion 把第 idx 段加一，后面的段清零；用于 ~ / ^ / 通配的上界。
func bumpVersion(parts []int, idx int) string {
	if idx < 0 {
		return "0.0.1"
	}
	out := make([]int, len(parts))
	copy(out, parts)
	out[idx]++
	for i := idx + 1; i < len(out); i++ {
		out[i] = 0
	}
	return formatVersion(out)
}

// SatisfiedBy 判断版本是否满足约束。comparable=false 表示版本无法解析（调用方应
// 标记 version_unknown，而不是 mismatch）。
func (c VersionConstraint) SatisfiedBy(version string) (satisfied bool, comparable bool) {
	if c.Any {
		return true, true
	}
	core := versionCore(version)
	if core == "" {
		return false, false
	}
	for _, cl := range c.Clauses {
		cmp := compareVersionNumbers(core, cl.version)
		ok := false
		switch cl.op {
		case "=":
			ok = cmp == 0
		case "!=":
			ok = cmp != 0
		case ">":
			ok = cmp > 0
		case ">=":
			ok = cmp >= 0
		case "<":
			ok = cmp < 0
		case "<=":
			ok = cmp <= 0
		}
		if !ok {
			return false, true
		}
	}
	return true, true
}

func compareVersionNumbers(a, b string) int {
	pa := versionParts(a)
	pb := versionParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func versionParts(core string) []int {
	parts := strings.Split(versionCore(core), ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}
