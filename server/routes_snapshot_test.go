package server

import (
	"flag"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// P12 路由契约的**运行时**版本。
//
// 为什么不扫源码字面量：registerSideRoutes 用循环生成 9 条路由、registerFindingTraffic
// 用字符串拼接生成 7 条——AST 只认 BasicLit，这 16 条它一条也扫不到。此前契约测试正是
// 因此漏掉了整个 traffic 改动面（PATCH/DELETE/PUT 都在里面）。所以改成运行时清点：
// 用记录型 registrar 把 registerRoutes 真实挂上去的路由逐条抄下来。
//
// 它的用途是给**结构改动**当回归网：拆分 server.go、把路由搬到别的文件、改注册顺序，
// 都必须让这份快照逐条相等。顺序无关（按字符串排序后比对）。

var updateRoutes = flag.Bool("update-routes", false, "重新生成 testdata/routes.golden")

const routesGoldenPath = "testdata/routes.golden"

// recordingRegistrar 记录挂上来的路由，同时**转发给真 mux**——这样重复 pattern
// 仍会像生产一样 panic（Go 1.22+ 的 ServeMux 对重复 pattern 是 panic，不是覆盖）。
type recordingRegistrar struct {
	mux  *http.ServeMux
	rows []string
}

func (r *recordingRegistrar) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.rows = append(r.rows, pattern+"\t"+handlerSymbol(h))
	r.mux.HandleFunc(pattern, h)
}

// handlerSymbol 取处理函数的符号名（方法值 → 方法名；闭包 → 含 .funcN 的符号）。
// 记它是为了抓住「路由还在、但被接到了别的处理函数上」这种改错——只比对 pattern
// 是看不见的。模块前缀去掉，免得 golden 里全是 github.com/lin9527ya/blade/。
func handlerSymbol(h func(http.ResponseWriter, *http.Request)) string {
	f := runtime.FuncForPC(reflect.ValueOf(h).Pointer())
	if f == nil {
		return "?"
	}
	return strings.TrimPrefix(f.Name(), "github.com/lin9527ya/blade/")
}

// inventoryRoutes 清点真实注册的全部路由（已排序，格式 "METHOD /path\thandler"）。
//
// 用零值 &Server{} 是安全的：注册区没有任何分支（纯线性），绑定的都是方法值，
// 不会解引用字段；这也是本仓库测试的既有写法（见 goals_test.go / skill_meta_test.go）。
// 若将来有人在注册期引入条件分支，下面的条数下限会先把这件事拦下来。
func inventoryRoutes(t *testing.T) []string {
	t.Helper()
	rec := &recordingRegistrar{mux: http.NewServeMux()}
	(&Server{}).registerRoutes(rec)
	out := slices.Clone(rec.rows)
	sort.Strings(out)
	return out
}

// TestRouteSnapshot 把路由面钉成快照：任何增删改都必须显式更新 golden。
// 更新方式：go test ./server/ -run TestRouteSnapshot -update-routes
func TestRouteSnapshot(t *testing.T) {
	got := inventoryRoutes(t)
	if len(got) < 300 {
		t.Fatalf("只清点到 %d 条路由（<300）：registerRoutes 可能没被完整调用，先修测试再谈快照", len(got))
	}
	body := strings.Join(got, "\n") + "\n"

	if *updateRoutes {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(routesGoldenPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已重写 %s（%d 条路由）", routesGoldenPath, len(got))
		return
	}

	wantRaw, err := os.ReadFile(routesGoldenPath)
	if err != nil {
		t.Fatalf("读不到 %s（首次请用 -update-routes 生成）：%v", routesGoldenPath, err)
	}
	want := strings.Split(strings.TrimRight(string(wantRaw), "\n"), "\n")

	if slices.Equal(got, want) {
		return
	}

	// 差异分类报出来，比甩两个大列表好定位。
	inWant := make(map[string]bool, len(want))
	for _, w := range want {
		inWant[w] = true
	}
	inGot := make(map[string]bool, len(got))
	for _, g := range got {
		inGot[g] = true
	}
	var added, removed []string
	for _, g := range got {
		if !inWant[g] {
			added = append(added, g)
		}
	}
	for _, w := range want {
		if !inGot[w] {
			removed = append(removed, w)
		}
	}
	if len(added) > 0 {
		t.Errorf("路由快照多了 %d 条（若是有意新增，跑 -update-routes 更新 golden）：\n  %s",
			len(added), strings.Join(added, "\n  "))
	}
	if len(removed) > 0 {
		t.Errorf("路由快照少了 %d 条（若是有意删除，跑 -update-routes 更新 golden）：\n  %s",
			len(removed), strings.Join(removed, "\n  "))
	}
}

// inventoryPatterns 只要 pattern 列（"METHOD /path"），给契约测试用。
func inventoryPatterns(t *testing.T) []string {
	t.Helper()
	rows := inventoryRoutes(t)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		pattern, _, _ := strings.Cut(r, "\t")
		out = append(out, pattern)
	}
	return out
}
