package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestModelPreviewSeparatesObservedAndConfigured(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err = pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	pid, err := pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("preview-%d", time.Now().UnixNano()), Format: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "preview-secret", Model: "configured-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteProfile(pid)
	tk := &Task{ID: fmt.Sprintf("preview-%d", pid)}
	tk.setLLMState(&pid, &pid, []int64{pid}, 1, "ready", "")
	s := &Server{m: &Manager{pg: pg, tasks: map[string]*Task{tk.ID: tk}}, skillDir: t.TempDir()}
	get := func() (taskLLMResolution, *observedModel) {
		t.Helper()
		r := httptest.NewRequest("GET", "/?role=mainagent", nil)
		r.SetPathValue("id", tk.ID)
		w := httptest.NewRecorder()
		s.taskModelPreview(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Expected taskLLMResolution
			Last     *observedModel `json:"last_used"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Expected, out.Last
	}
	exp, last := get()
	if !exp.Available || exp.Model != "configured-model" || last != nil {
		t.Fatalf("%+v %+v", exp, last)
	}
	if _, err = pg.Exec(`INSERT INTO llm_usage(task_id,worker,model,status) VALUES ($1,'main','observed-model','ok')`, tk.ID); err != nil {
		t.Fatal(err)
	}
	defer pg.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, tk.ID)
	exp, last = get()
	if exp.Model != "configured-model" || last == nil || last.Model != "observed-model" || len(s.providers.byProfile) > 0 {
		t.Fatalf("preview changed runtime or confused model: %+v %+v", exp, last)
	}
}
