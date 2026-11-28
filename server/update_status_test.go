package server

import (
	"encoding/json"
	"github.com/lin9527ya/blade/selfupdate"
	"net/http/httptest"
	"testing"
)

func TestUpdateStatusReattachesAndRejectsDuplicate(t *testing.T) {
	old := updHub
	updHub = &updateHub{subs: map[chan updateProgress]struct{}{}}
	defer func() { updHub = old }()
	s := &Server{}
	if !updHub.begin("v1.2.0") {
		t.Fatal("begin")
	}
	updHub.publish(selfupdate.PhaseDownload, 44, "download")
	ch, stop := updHub.subscribe()
	defer stop()
	if current := <-ch; current.Percent != 44 {
		t.Fatal(current)
	}
	w := httptest.NewRecorder()
	s.updateStatus(w, httptest.NewRequest("GET", "/api/update/status", nil))
	var result struct {
		Running  bool
		Progress updateProgress
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Running || result.Progress.Version != "v1.2.0" || result.Progress.Percent != 44 {
		t.Fatalf("%+v %v", result, err)
	}
	w = httptest.NewRecorder()
	s.updateApply(w, httptest.NewRequest("POST", "/api/update/apply", nil))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	updHub.finish(nil)
	if p := <-ch; p.Phase != selfupdate.PhaseStaged {
		t.Fatal(p)
	}
	if updHub.begin("v1.2.0") {
		t.Fatal("staged release was launched again")
	}
}
