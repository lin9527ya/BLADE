package db

import "testing"

// 通路台账要能区分三种状态：没动用 / 动过但没产出且失败 / 动过且有下游。
func TestChainLedgerStatuses(t *testing.T) {
	d, st := auditFixture(t)

	mkNode := func(kind, state, payload string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,$2,$3,$4::jsonb) RETURNING id`, st.expID, kind, state, payload).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", kind, err)
		}
		return id
	}
	unused := mkNode("foothold", "active", `{"summary":"web01 www-data","access":"shell","privilege":"user"}`)
	advanced := mkNode("foothold", "active", `{"summary":"web02 root","access":"shell","privilege":"root"}`)
	stale := mkNode("credential", "active", `{"summary":"db cred","kind":"password","username":"app","secret":"x","targets":[{"host":"db01","service":"ssh"}]}`)
	fact := mkNode("fact", "confirmed", `{"summary":"读到了 /etc/passwd"}`)

	mkIntent := func(state string) int64 {
		t.Helper()
		id, err := st.AddIntent(map[string]any{"summary": "用战果推进"}, 5, nil, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if state != "open" {
			if ok, err := st.CompareAndSetIntentState(id, "open", state); err != nil || !ok {
				t.Fatalf("settle %s: ok=%v err=%v", state, ok, err)
			}
		}
		return id
	}
	// advanced：动过 advanced 据点，产出了一条事实。
	okIntent := mkIntent("done")
	if err := st.Link(okIntent, RelUses, advanced); err != nil {
		t.Fatal(err)
	}
	if err := st.Link(okIntent, RelYields, fact); err != nil {
		t.Fatal(err)
	}
	// open：动过 stale 凭据，没产出，最后一次 exhausted。
	staleIntent := mkIntent("exhausted")
	if err := st.Link(staleIntent, RelUses, stale); err != nil {
		t.Fatal(err)
	}

	rep, err := st.ChainLedger()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]ChainRow{}
	for _, r := range rep.Rows {
		byID[r.LootID] = r
	}
	if r := byID[unused]; r.Status != ChainStatusUnused || r.Uses != 0 {
		t.Fatalf("unused 行不对：%+v", r)
	}
	if r := byID[advanced]; r.Status != ChainStatusAdvanced || r.Uses != 1 || len(r.Downstream) != 1 {
		t.Fatalf("advanced 行不对：%+v", r)
	}
	if r := byID[advanced]; r.Stage != "privilege" {
		t.Fatalf("root 据点应推断为 privilege，得到 %q", r.Stage)
	}
	if r := byID[stale]; r.Status != ChainStatusOpen || r.LastOutcome != "exhausted" {
		t.Fatalf("open 行不对：%+v", r)
	}
	// open 排在 unused 前面（更该被处理）。
	if rep.Rows[0].LootID != stale {
		t.Fatalf("open 应排在最前：%+v", rep.Rows)
	}
}

// 凭据 targets 已实测登入 → stage=lateral。
func TestChainLedgerCredentialVerifiedIsLateral(t *testing.T) {
	d, st := auditFixture(t)
	var id int64
	if err := d.QueryRow(`INSERT INTO exploration_nodes(exploration_id, kind, state, payload)
VALUES ($1,'credential','active',$2::jsonb) RETURNING id`, st.expID,
		`{"summary":"db root","kind":"password","username":"root","secret":"x","targets":[{"host":"db01","service":"ssh","verified":true}]}`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rep, err := st.ChainLedger()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Stage != "lateral" {
		t.Fatalf("已验证凭据应为 lateral：%+v", rep.Rows)
	}
}
