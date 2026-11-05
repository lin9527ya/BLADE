package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 台账端点：未知任务 404；有任务时返回 ledger/chains 两个块。
func TestExplorationLedgerEndpoint(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	task, err := m.CreateTask("ledger api", "台账端点", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _ = m.pg.DeleteTask(tid) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	token, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}

	w := request("/api/exploration/ledger?task=" + task.ID)
	if w.Code != 200 {
		t.Fatalf("ledger: %d %s", w.Code, w.Body)
	}
	var out struct {
		Ledger map[string]any  `json:"ledger"`
		Chains *db.ChainReport `json:"chains"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v %s", err, w.Body)
	}
	if out.Ledger == nil {
		t.Fatalf("缺少 ledger 块：%s", w.Body)
	}
	if _, ok := out.Ledger["gaps_total"]; !ok {
		t.Fatalf("ledger 缺少 gaps_total：%s", w.Body)
	}
	if out.Chains == nil {
		t.Fatalf("缺少 chains 块：%s", w.Body)
	}

	if w := request("/api/exploration/ledger?task=does-not-exist"); w.Code != 404 {
		t.Fatalf("未知任务应 404，实际 %d", w.Code)
	}
}
