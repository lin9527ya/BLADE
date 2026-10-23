package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// parsePackage 解析本包所有非测试 .go 文件。
//
// 契约测试**刻意不绑文件名**：函数在文件之间搬来搬去是常规重构（server.go 已按域拆成
// 若干 *_api.go），绑死某一份文件会让测试在拆分后静默抽到 0 个键。下面那条 <40 的断言
// 就是为这种情况留的警报——它响过一次（拆 server.go 时），那一次是真的。
func parsePackage(t *testing.T) []*ast.File {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []*ast.File
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out = append(out, f)
	}
	return out
}

// 设置项契约测试：get 与 put 必须覆盖同一批键。
//
// 背景：设置项散在多处（manager.go 的键常量、settingsPayload 的 map、putSettings 的
// 匿名 struct、putSettings 的分支、前端 types.ts），新增一项要同时改**后端这三处**，
// 漏掉 putSettings 那一处就是**静默 no-op**——界面显示保存成功、实际没落库。
// 2026-10 的 cors_allowed_origins 正是这么漏的：键加了、settingsPayload 回显加了，
// 忘了加 putSettings 的分支，于是"白名单内的来源该收到 CORS 头"这条验收才发现。
//
// 做法与 routes_contract_test.go 同构：把两侧的键名从**源码**里抽出来对账，
// 只允许显式登记的例外；新增例外必须在这里写明理由（逼出一次"这真的该只写/只读吗"的审阅）。
//
// 为什么抽源码而不是跑起来比：跑起来只能看到"当前值"，看不到"这个键根本没被处理"
// ——而后者恰恰是要防的。

// putSettingsFields 抽出 putSettings 里匿名 struct 的 json 标签。
func putSettingsFields(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range parsePackage(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "putSettings" {
				return true
			}
			ast.Inspect(fn, func(m ast.Node) bool {
				st, ok := m.(*ast.StructType)
				if !ok {
					return true
				}
				for _, fld := range st.Fields.List {
					if fld.Tag == nil {
						continue
					}
					if name := jsonTagName(fld.Tag.Value); name != "" {
						out = append(out, name)
					}
				}
				return true
			})
			return false
		})
	}
	sort.Strings(out)
	return out
}

// settingsPayloadKeys 抽出 settingsPayload 返回的 map 字面量的键。
func settingsPayloadKeys(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range parsePackage(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "settingsPayload" {
				return true
			}
			ast.Inspect(fn, func(m ast.Node) bool {
				cl, ok := m.(*ast.CompositeLit)
				if !ok || cl.Type == nil {
					return true
				}
				mt, ok := cl.Type.(*ast.MapType)
				if !ok {
					return true
				}
				if id, ok := mt.Value.(*ast.Ident); !ok || id.Name != "any" {
					return true
				}
				for _, el := range cl.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if lit, ok := kv.Key.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						out = append(out, trimQuotes(lit.Value))
					}
				}
				return true
			})
			return false
		})
	}
	sort.Strings(out)
	return out
}

// jsonTagName 取 `json:"name"` 里的 name（忽略 ,omitempty 之类）。
func jsonTagName(tag string) string {
	tag = trimQuotes(tag)
	const p = `json:"`
	i := indexOf(tag, p)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(p):]
	j := indexOf(rest, `"`)
	if j < 0 {
		return ""
	}
	name := rest[:j]
	if k := indexOf(name, ","); k >= 0 {
		name = name[:k]
	}
	return name
}

func trimQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '`' || s[0] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSettingsGetPutContract(t *testing.T) {
	putFields := putSettingsFields(t)
	getKeys := settingsPayloadKeys(t)
	if len(putFields) < 40 || len(getKeys) < 40 {
		t.Fatalf("只抽到 put=%d / get=%d 个键（<40）：解析模式可能已失效，先修测试再谈契约",
			len(putFields), len(getKeys))
	}

	for _, k := range putFields {
		if slices.Contains(getKeys, k) {
			continue
		}
		if _, ok := settingsWriteOnly[k]; !ok {
			t.Errorf("设置项 %q 能写但读不回：要么在 settingsPayload 里回显，"+
				"要么在 settings_contract_test.go 的 settingsWriteOnly 里登记并写明理由", k)
		}
	}
	for _, k := range getKeys {
		if slices.Contains(putFields, k) {
			continue
		}
		if _, ok := settingsReadOnly[k]; !ok {
			t.Errorf("设置项 %q 读得到但写不了：要么在 putSettings 里处理，"+
				"要么在 settings_contract_test.go 的 settingsReadOnly 里登记并写明理由", k)
		}
	}
	// 反向：登记过的例外必须仍然存在，否则是陈旧条目。
	for k := range settingsWriteOnly {
		if !slices.Contains(putFields, k) {
			t.Errorf("settingsWriteOnly 登记的 %q 已不在 putSettings 里：清掉这条登记", k)
		}
		if slices.Contains(getKeys, k) {
			t.Errorf("%q 已被 settingsPayload 回显：从 settingsWriteOnly 里删掉", k)
		}
	}
	for k := range settingsReadOnly {
		if !slices.Contains(getKeys, k) {
			t.Errorf("settingsReadOnly 登记的 %q 已不在 settingsPayload 里：清掉这条登记", k)
		}
		if slices.Contains(putFields, k) {
			t.Errorf("%q 已能被写入：从 settingsReadOnly 里删掉", k)
		}
	}
}

// settingsWriteOnly：**只写不读**的键——写进去的值刻意不回显。密钥类都在这。
var settingsWriteOnly = map[string]string{
	"brave_search_api_key":  "搜索后端密钥：只存不回显，界面只看 *_key_set 布尔",
	"tavily_search_api_key": "同上",
	"fofa_key":              "情报源密钥：只存不回显",
	"update_github_token":   "自更新 token：只存不回显",
}

// settingsReadOnly：**只读不写**的键——由后端算出，不接受写入。
var settingsReadOnly = map[string]string{
	"brave_key_set":            "由 brave_search_api_key 是否为空算出",
	"tavily_key_set":           "同上",
	"fofa_key_set":             "由 fofa_key 是否为空算出",
	"update_token_set":         "由 update_github_token 是否为空算出",
	"intel_used_today":         "今日情报用量，运行时统计",
	"intercept_scope_mode_env": "环境变量覆盖值，只读",
	"proxy_active":             "运行时状态：当前生效出口，只读",
	"proxy_blocked":            "运行时状态：全部代理不可用（出口已阻断，不会直连），只读",
	"proxy_status":             "运行时状态：每个候选的最近探测结果，只读",
}
