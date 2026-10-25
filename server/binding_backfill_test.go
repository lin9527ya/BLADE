package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 存量库回填：代码把工具加进默认集后，tools.agents 不会自动更新；ToolResolve 又会按
// DB 行过滤。这个用例把两条绑定先删掉，再跑一次迁移，确认补回来且幂等。
func TestBuiltinBindingBackfillAddsMissingBindings(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM settings WHERE key='builtin_binding_backfill_20261025'`)
	})
	s := &Server{m: &Manager{pg: d}}

	type backup struct {
		key    string
		agents string
	}
	for _, key := range []string{"coverage_matrix", "list_companies"} {
		var raw string
		if err := d.QueryRow(`SELECT agents::text FROM tools WHERE key=$1`, key).Scan(&raw); err != nil {
			t.Skipf("tool %s not seeded in this DB: %v", key, err)
		}
		b := backup{key: key, agents: raw}
		t.Cleanup(func() {
			_, _ = d.Exec(`UPDATE tools SET agents=$2::jsonb WHERE key=$1`, b.key, b.agents)
		})
	}
	// 模拟旧库：两条绑定都不在。
	if _, err := d.Exec(`UPDATE tools SET agents = agents - 'planner' WHERE key='coverage_matrix'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE tools SET agents = agents - 'mainagent' WHERE key='list_companies'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM settings WHERE key='builtin_binding_backfill_20261025'`); err != nil {
		t.Fatal(err)
	}

	s.seedBuiltinBindingBackfill()

	has := func(key, agent string) bool {
		var raw string
		if err := d.QueryRow(`SELECT agents::text FROM tools WHERE key=$1`, key).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var agents []string
		_ = json.Unmarshal([]byte(raw), &agents)
		for _, a := range agents {
			if a == agent {
				return true
			}
		}
		return false
	}
	if !has("coverage_matrix", "planner") {
		t.Fatal("planner 仍未绑定 coverage_matrix")
	}
	if !has("list_companies", "mainagent") {
		t.Fatal("mainagent 仍未绑定 list_companies")
	}
	// 幂等：第二次直接因 flag 返回，不报错也不重复。
	s.seedBuiltinBindingBackfill()
	var v string
	_ = d.QueryRow(`SELECT value FROM settings WHERE key='builtin_binding_backfill_20261025'`).Scan(&v)
	if strings.TrimSpace(v) != "true" {
		t.Fatalf("迁移标记未写入: %q", v)
	}
}
