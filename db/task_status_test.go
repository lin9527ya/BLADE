package db

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestDeriveTaskStatusMatrix 穷举推导的优先级：终态 > 排队 > 暂停 > 运行 > 未开始。
//
// 这是"任务状态四套表示"收敛的核心断言：推导现在是纯函数，可以逐组合验，不再靠
// 起一整个 Server 去猜。
func TestDeriveTaskStatusMatrix(t *testing.T) {
	cases := []struct {
		name string
		in   TaskStatusInput
		want string
	}{
		{"终态优先于一切", TaskStatusInput{Persisted: TaskStatusDone, Queued: true, Paused: true, Started: true, Ready: true}, TaskStatusDone},
		{"policy_blocked 也是终态", TaskStatusInput{Persisted: TaskStatusPolicyBlocked}, TaskStatusPolicyBlocked},
		{"timeout 也是终态", TaskStatusInput{Persisted: TaskStatusTimeout}, TaskStatusTimeout},
		{"队列优先于暂停", TaskStatusInput{Persisted: TaskStatusRunning, Queued: true, Paused: true, Started: true}, TaskStatusQueued},
		{"暂停优先于运行", TaskStatusInput{Persisted: TaskStatusRunning, Paused: true, Started: true, Ready: true}, TaskStatusPaused},
		{"已启动且就绪 = running", TaskStatusInput{Persisted: TaskStatusRunning, Started: true, Ready: true}, TaskStatusRunning},
		{"启动但未就绪 = created", TaskStatusInput{Persisted: TaskStatusRunning, Started: true}, TaskStatusCreated},
		{"未启动 = created", TaskStatusInput{Persisted: TaskStatusRunning}, TaskStatusCreated},
		{"LLM 链耗尽的已启动任务仍算 running", TaskStatusInput{Persisted: TaskStatusRunning, Started: true, LLMFailoverExhausted: true}, TaskStatusRunning},
		{"空持久化状态 = created", TaskStatusInput{}, TaskStatusCreated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveTaskStatus(c.in); got != c.want {
				t.Fatalf("DeriveTaskStatus(%+v) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestDerivedStatusesCoverEveryPersistedStatus 钉住两个集合的关系：
// 派生集合 = 持久化集合 + queued。少一个就意味着某个状态永远不可能出现在 UI 上。
func TestDerivedStatusesCoverEveryPersistedStatus(t *testing.T) {
	for _, s := range PersistedTaskStatuses {
		if !IsPersistedTaskStatus(s) {
			t.Fatalf("%q 在 PersistedTaskStatuses 里却过不了 IsPersistedTaskStatus", s)
		}
		if !IsDerivedTaskStatus(s) {
			t.Fatalf("持久化状态 %q 不在派生集合里：API 永远返回不出它", s)
		}
	}
	if IsPersistedTaskStatus(TaskStatusQueued) {
		t.Fatal("queued 是派生状态，库里没有这个值——把它算进持久化集合会让写入失败")
	}
	if !IsDerivedTaskStatus(TaskStatusQueued) {
		t.Fatal("queued 必须出现在派生集合里")
	}
}

// TestSchemaCheckMatchesPersistedStatuses 钉住 schema 的 CHECK 与 Go 侧集合一致。
//
// 两侧不一致的后果很隐蔽：Go 侧多一个值 → 写入时报 check violation；少一个值 →
// 一个合法状态永远写不进去。这个断言不需要数据库连接（读 schema.sql 即可）。
func TestSchemaCheckMatchesPersistedStatuses(t *testing.T) {
	body := readSourceForTest(t, "schema.sql")
	// 锚在 tasks_status_check 这个约束名上：库里还有别的 `CHECK (status IN (...))`
	// （例如 goals 表的 achieved/failed/open），不锚定就会抓错那一条。
	re := regexp.MustCompile(`tasks_status_check\s*\n?\s*CHECK \(status IN \(([^)]*)\)\)`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("schema.sql 里没找到 tasks_status_check 的 CHECK 子句")
	}
	var dbStatuses []string
	for _, part := range strings.Split(m[1], ",") {
		dbStatuses = append(dbStatuses, strings.Trim(strings.TrimSpace(part), "'"))
	}
	got := append([]string(nil), dbStatuses...)
	want := append([]string(nil), PersistedTaskStatuses...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("schema 的 CHECK 与 PersistedTaskStatuses 不一致：\n  db: %v\n  go: %v", got, want)
	}
}

// TestFrontendTaskStatusUnionMatchesGo 钉住前端联合类型与派生集合一致。
//
// 前端 `TaskStatus` 是第五份抄写（此前）。它多了/少了一个值，表现都是"某个筛选或
// 徽章永远不出现"，而且不会报错。这里直接读 TS 源做文本断言。
func TestFrontendTaskStatusUnionMatchesGo(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	tsx := filepath.Join(filepath.Dir(file), "..", "web", "src", "lib", "types.ts")
	body, err := os.ReadFile(tsx)
	if err != nil {
		t.Skipf("读不到前端类型定义（跳过）：%v", err)
	}
	re := regexp.MustCompile(`export type TaskStatus =([^;]*);`)
	m := re.FindSubmatch(body)
	if m == nil {
		t.Fatal("types.ts 里没找到 `export type TaskStatus = ...;`")
	}
	quoted := regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(m[1], -1)
	var fe []string
	for _, q := range quoted {
		fe = append(fe, string(q[1]))
	}
	got := append([]string(nil), fe...)
	want := append([]string(nil), DerivedTaskStatuses...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("前端 TaskStatus 与 DerivedTaskStatuses 不一致：\n  前端: %v\n  后端: %v", got, want)
	}
}
