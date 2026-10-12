package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// P12 路由契约测试（做法对齐 CyberStrikeAI internal/security/route_inventory_test.go）：
// 用 go/parser 把 server.go 里注册的路由面变成可断言的清单——「新增一条公开路由」
// （JWT 豁免）或「把路由挂到 root mux」都必须先改这里的显式登记表，否则测试失败。
// 4000+ 行的 Handler() 里漏一层鉴权就是一个越权面，靠人眼审是审不过来的。

// scanRouteLiterals 扫描 src 里 `ident.HandleFunc("METHOD /path", ...)` 的第一参字面量。
func scanRouteLiterals(t *testing.T, src, ident string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		base, ok := sel.X.(*ast.Ident)
		if !ok || base.Name != ident {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		out = append(out, strings.Trim(lit.Value, "`\""))
		return true
	})
	sort.Strings(out)
	return out
}

// routePath 取 "METHOD /path" 里的路径部分。
func routePath(route string) string {
	if i := strings.Index(route, " "); i >= 0 {
		return route[i+1:]
	}
	return route
}

func TestRouteInventoryContract(t *testing.T) {
	muxRoutes := scanRouteLiterals(t, "server.go", "mux")
	if len(muxRoutes) < 200 {
		t.Fatalf("只扫到 %d 条 mux 路由（<200）：解析模式可能已失效，先修测试再谈契约", len(muxRoutes))
	}

	// ① 所有 mux 路由必须落在 /api/ 下——mux 只被挂到 root 的 "/api/"，混入别的
	// 前缀等于把它暴露在 JWT 之外。
	for _, r := range muxRoutes {
		if p := routePath(r); !strings.HasPrefix(p, "/api/") {
			t.Errorf("路由 %q 不在 /api/ 下：mux 只挂在 /api/ 之后，这样注册会绕过 JWT", r)
		}
	}

	// ② JWT 豁免面（requireAuth 只放行 /api/auth/* 与 /api/health）必须精确等于
	// 本清单：新增公开路由必须在这里显式登记，逼出一次「这真的该公开吗」的审阅。
	publicAllow := map[string]bool{
		"GET /api/auth/status":           true,
		"POST /api/auth/init":            true,
		"POST /api/auth/login":           true,
		"POST /api/auth/change-password": true,
		"GET /api/health":                true,
	}
	for _, r := range muxRoutes {
		p := routePath(r)
		if !strings.HasPrefix(p, "/api/auth/") && p != "/api/health" {
			continue
		}
		if !publicAllow[r] {
			t.Errorf("出现未登记的 JWT 豁免路由 %q：若确需公开，请在 routes_contract_test.go 的 publicAllow 里显式登记", r)
		}
	}
	for r := range publicAllow {
		if !slices.Contains(muxRoutes, r) {
			t.Errorf("登记过的公开路由 %q 已不存在：同步更新 publicAllow", r)
		}
	}

	// ③ root mux 只允许两个已知挂载；新增 root.Handle* 等于在 JWT 之外开新面。
	rootMounts := scanRouteLiterals(t, "server.go", "root")
	if len(rootMounts) > 0 {
		// root.Handle 不是 HandleFunc，这里只在误用 HandleFunc 时报警。
		t.Errorf("root mux 上出现 HandleFunc 注册 %v：所有 API 路由必须走受 requireAuth 保护的 mux", rootMounts)
	}
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(src), "root.Handle("); got != 2 {
		t.Errorf("root mux 的挂载数 = %d，want 2（/api/ + 前端兜底）：新增挂载必须先审阅", got)
	}
}

// TestRequireAuthExemptionContract 从另一侧钉住同一份契约：requireAuth 源码里出现
// 的路径字面量必须只有两个豁免项——公开面是「两侧同时改才算数」的。
func TestRequireAuthExemptionContract(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "auth.go", nil, 0)
	if err != nil {
		t.Fatalf("parse auth.go: %v", err)
	}
	var paths []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "requireAuth" {
			return true
		}
		ast.Inspect(fn, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				v := strings.Trim(lit.Value, "`\"")
				if strings.HasPrefix(v, "/api") {
					paths = append(paths, v)
				}
			}
			return true
		})
		return false
	})
	sort.Strings(paths)
	want := []string{"/api/auth/", "/api/health"}
	if !slices.Equal(paths, want) {
		t.Fatalf("requireAuth 的路径豁免 = %v，want %v：公开面变更必须同步更新契约测试", paths, want)
	}
}
