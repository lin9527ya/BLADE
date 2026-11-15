package intercept

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// Bash argv 前缀规则：只允许「程序 + 子命令」这一层，不约束其后的 flags。
// 与 string/regex 规则相比，它不会被 `nmap x; rm -rf /` 这种复合命令骗过。

var argvArity = map[string]int{
	"cat": 1, "cd": 1, "ls": 1, "rm": 1, "grep": 1, "head": 1, "tail": 1,
	"echo": 1, "pwd": 1, "find": 1, "curl": 1, "wget": 1, "nmap": 1,
	"git": 2, "npm": 2, "docker": 2, "kubectl": 2, "terraform": 2,
	"npm run": 3, "docker compose": 3, "kubectl rollout": 3, "git config": 3,
	"gcloud": 3, "gh": 3, "aws": 3,
}

func argvPrefixLen(tokens []string) int {
	for n := len(tokens); n > 0; n-- {
		if a, ok := argvArity[strings.Join(tokens[:n], " ")]; ok {
			return min(a, len(tokens))
		}
	}
	return min(1, len(tokens))
}

type argvWord struct {
	Value   string
	Literal bool
}

var (
	errNotSimple  = errors.New("不是单条简单命令")
	errBackground = errors.New("命令在后台运行")
	errRedirect   = errors.New("命令包含重定向")
	errAssign     = errors.New("命令包含前置赋值")
	errWrapper    = errors.New("程序是包装器，会把参数当命令执行")
)

var argvWrappers = map[string]bool{"command": true, "builtin": true, "env": true, "exec": true, "eval": true}

// ParseSimpleCommand 只接受一条简单命令：无赋值/重定向/管道/列表/子 shell/后台/
// 复合命令，无 command/env/exec/eval 等包装器，且整行没有命令替换、进程替换、
// 参数/算术展开、扩展 glob 或 brace 展开。Bash 工具用 bash -c 执行，所以语法按 Bash。
func ParseSimpleCommand(command string) ([]argvWord, error) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, fmt.Errorf("命令无法解析：%w", err)
	}
	if len(f.Stmts) != 1 {
		return nil, errNotSimple
	}
	st := f.Stmts[0]
	switch {
	case st.Background || st.Coprocess:
		return nil, errBackground
	case len(st.Redirs) > 0:
		return nil, errRedirect
	}
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || st.Negated {
		return nil, errNotSimple
	}
	if len(call.Assigns) > 0 {
		return nil, errAssign
	}
	if len(call.Args) == 0 {
		return nil, errNotSimple
	}
	var bad error
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ParamExp, *syntax.ArithmExp, *syntax.ExtGlob, *syntax.BraceExp:
			bad = errors.New("命令包含展开或替换")
		}
		return bad == nil
	})
	if bad != nil {
		return nil, bad
	}
	words := make([]argvWord, 0, len(call.Args))
	for _, w := range call.Args {
		word, ok := argvWordOf(w)
		if !ok {
			return nil, errors.New("命令包含展开或替换")
		}
		words = append(words, word)
	}
	if argvWrappers[path.Base(words[0].Value)] {
		return nil, errWrapper
	}
	return words, nil
}

func argvWordOf(w *syntax.Word) (argvWord, bool) {
	var sb strings.Builder
	literal := true
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, "*?[]{}~\\") {
				literal = false
			}
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				literal = false
			}
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				literal = false
			}
			for _, q := range p.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok {
					return argvWord{}, false
				}
				if strings.Contains(lit.Value, "\\") {
					literal = false
				}
				sb.WriteString(lit.Value)
			}
		default:
			return argvWord{}, false
		}
	}
	return argvWord{Value: sb.String(), Literal: literal}, true
}

// ArgvPrefix 解析命令并返回程序+子命令前缀。前缀 token 必须是字面量，且不能是 flag：
// `git -C /repo push` 不会编出一个假前缀。
func ArgvPrefix(command string) ([]string, error) {
	words, err := ParseSimpleCommand(command)
	if err != nil {
		return nil, err
	}
	tokens := make([]string, len(words))
	for i, w := range words {
		tokens[i] = w.Value
		if !w.Literal {
			tokens[i] = "\x00"
		}
	}
	n := argvPrefixLen(tokens)
	for _, w := range words[:n] {
		if !w.Literal {
			return nil, errors.New("前缀 token 不是字面量")
		}
		if err := checkArgvToken(w.Value); err != nil {
			return nil, err
		}
	}
	return tokens[:n], nil
}

const argvMaxTokens = 8

func checkArgvToken(tok string) error {
	if tok == "" {
		return errors.New("token 为空")
	}
	if strings.HasPrefix(tok, "-") {
		return fmt.Errorf("token %q 是 flag；前缀只放程序和子命令", tok)
	}
	if strings.ContainsAny(tok, ";&|><`$()") || strings.IndexFunc(tok, unicode.IsSpace) >= 0 {
		return fmt.Errorf("token %q 含 shell 元字符或空格", tok)
	}
	return nil
}

// ParseArgvPattern 校验 argv 规则的 pattern：1–8 个 token 的 JSON 字符串数组。
func ParseArgvPattern(pattern string) ([]string, error) {
	var tokens []string
	if err := json.Unmarshal([]byte(pattern), &tokens); err != nil {
		return nil, fmt.Errorf(`argv pattern 是 JSON 字符串数组，例如 ["git","push"]：%w`, err)
	}
	if len(tokens) < 1 || len(tokens) > argvMaxTokens {
		return nil, fmt.Errorf("argv pattern 需要 1–%d 个 token，当前 %d 个", argvMaxTokens, len(tokens))
	}
	for _, tok := range tokens {
		if err := checkArgvToken(tok); err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

func argvRuleMatches(tokens []string, toolName string, input []byte) bool {
	if toolName != "Bash" || len(tokens) == 0 {
		return false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	prefix, err := ArgvPrefix(in.Command)
	if err != nil || len(prefix) != len(tokens) {
		return false
	}
	for i := range prefix {
		if prefix[i] != tokens[i] {
			return false
		}
	}
	return true
}

const AlwaysAllowPriority = -1000

// AlwaysAllowPrefix derives a stable prefix for a pending Bash request. It refuses
// programs whose prefix would amount to arbitrary code execution; that denylist is
// code-owned and is also enforced when the rule is edited.
func AlwaysAllowPrefix(command string) (string, error) {
	prefix, err := ArgvPrefix(command)
	if err != nil {
		return "", err
	}
	if len(prefix) == 0 {
		return "", errors.New("无法从命令推导出安全前缀")
	}
	prog := normalizeAlwaysAllowProgram(prefix[0])
	if alwaysAllowDenied[prog] {
		return "", fmt.Errorf("程序 %q 不能用 always-allow：它的参数可以执行任意代码或造成不可逆破坏", prog)
	}
	return strings.Join(prefix, " "), nil
}

func normalizeAlwaysAllowProgram(raw string) string {
	p := strings.ToLower(path.Base(strings.TrimSpace(raw)))
	// Strip common version suffixes: python3.11 -> python, php8.2 -> php, pip3 -> pip.
	if i := strings.IndexFunc(p, func(r rune) bool { return r >= '0' && r <= '9' }); i > 0 {
		p = p[:i]
	}
	if strings.HasPrefix(p, "mkfs") {
		return "mkfs"
	}
	return p
}
