package server

import (
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/policy"
)

// 红队默认迁移只动"旧只读默认"这一种精确形态：移掉 http_delete，其它策略原样保留。
func TestRewriteLegacyReadOnlyPolicy(t *testing.T) {
	old := `{"version":6,"override":{"techniques":[],"forbidden_writes":["http_delete"],"custom":[]},"effective":{"version":6,"source":"global_default","techniques":null,"forbidden_writes":["http_delete"],"max_intents_per_round":4}}`
	out, changed := rewriteLegacyReadOnlyPolicy(old)
	if !changed {
		t.Fatal("旧只读默认应被迁移")
	}
	doc := policy.ParseDoc([]byte(out))
	if doc == nil {
		t.Fatalf("迁移结果解析失败: %s", out)
	}
	if len(doc.Effective.ForbiddenWrites) != 0 || len(doc.Effective.Techniques) != 0 {
		t.Fatalf("迁移后应全部放开: %+v", doc.Effective)
	}
	if doc.Effective.Source != "global_default" || doc.Effective.Version != 7 {
		t.Fatalf("来源/版本不对: %+v", doc)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	ov, _ := raw["override"].(map[string]any)
	if fw, ok := ov["forbidden_writes"].([]any); !ok || len(fw) != 0 {
		t.Fatalf("override 应显式写空数组（表示清空而不仅是继承）: %v", ov["forbidden_writes"])
	}

	untouched := []string{
		``,
		`{"version":1,"override":{},"effective":{"version":1,"source":"global_default","techniques":["bruteforce"],"forbidden_writes":["http_delete"],"max_intents_per_round":4}}`,
		`{"version":1,"override":{},"effective":{"version":1,"source":"global_default","techniques":[],"forbidden_writes":["http_delete","db_write"],"max_intents_per_round":4}}`,
		`{"version":1,"override":{},"effective":{"version":1,"source":"task","techniques":[],"forbidden_writes":["http_delete"]}}`,
	}
	for _, raw := range untouched {
		if _, changed := rewriteLegacyReadOnlyPolicy(raw); changed {
			t.Errorf("自定义/非默认策略不该被改: %s", raw)
		}
	}
}
