package db

import "testing"

// StrandedDependents 只报"前置已全部收场、且至少一个不会有结论"的 open 意图。
// 这是深度链（利用→提权→横向）最常见的断裂点：上游失败、下游永远进不了 frontier。
func TestStrandedDependents(t *testing.T) {
	_, st := auditFixture(t)
	upstream, err := st.AddIntent(map[string]any{"summary": "上游：拿 shell"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	downstream, err := st.AddIntent(map[string]any{"summary": "下游：提权"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(downstream, RelDependsOn, upstream); err != nil {
		t.Fatal(err)
	}

	// 上游还在跑：不算 stranded（还在等）。
	if got, err := st.StrandedDependents(); err != nil || len(got) != 0 {
		t.Fatalf("上游未收场时不该报 stranded：%v err=%v", got, err)
	}

	// 上游以 exhausted 收场：下游永远等不到。
	if ok, err := st.CompareAndSetIntentState(upstream, "open", "exhausted"); err != nil || !ok {
		t.Fatalf("settle upstream: ok=%v err=%v", ok, err)
	}
	got, err := st.StrandedDependents()
	if err != nil {
		t.Fatal(err)
	}
	if deps, ok := got[downstream]; !ok || len(deps) != 1 || deps[0] != upstream {
		t.Fatalf("stale=true 的下游应报 stranded：%v", got)
	}

	// 下游仍是 open，unblock_intent 的 ClearIntentDependencies 依旧有效。
	if n, err := st.ClearIntentDependencies(downstream); err != nil || n != 1 {
		t.Fatalf("clear dependencies: n=%d err=%v", n, err)
	}
	if got, _ := st.StrandedDependents(); len(got) != 0 {
		t.Fatalf("拆链后不该再报 stranded：%v", got)
	}
}

// 上游以 done/negative 收场是"有结论"，下游会正常可领，不能报 stranded。
func TestStrandedDependentsIgnoresSatisfiedUpstream(t *testing.T) {
	_, st := auditFixture(t)
	upstream, err := st.AddIntent(map[string]any{"summary": "上游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	downstream, err := st.AddIntent(map[string]any{"summary": "下游"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Link(downstream, RelDependsOn, upstream); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CompareAndSetIntentState(upstream, "open", "done"); err != nil || !ok {
		t.Fatalf("settle upstream: ok=%v err=%v", ok, err)
	}
	if got, err := st.StrandedDependents(); err != nil || len(got) != 0 {
		t.Fatalf("done 的上游不该报 stranded：%v err=%v", got, err)
	}
}
