package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestLootLedgerAndCredentialAttempts(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	full, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || full.TaskID == nil {
		t.Fatalf("fixture finding: %+v %v", full, err)
	}
	taskID := *full.TaskID
	task, ok := s.m.Task(strconv.FormatInt(taskID, 10))
	if !ok || task == nil {
		t.Fatal("task not found")
	}
	if _, err := task.Store.AddNode(db.KindFoothold, map[string]any{
		"summary": "SSH 据点", "access": "valid", "privilege": "user", "protocol": "ssh", "host": "10.0.0.5", "port": 22,
	}, 5, db.StateLootActive, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindCredential, map[string]any{
		"summary": "svc 凭据", "username": "svc", "secret": "s3cr3t-value", "kind": "password",
		"targets": []map[string]any{{"host": "10.0.0.5", "port": 22, "service": "ssh", "verified": true}},
	}, 5, db.StateLootActive, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindArtifact, map[string]any{
		"summary": "上传 webshell", "kind": "webshell", "host": "10.0.0.5", "locator": "/var/www/x.php",
	}, 5, db.StateArtifactActive, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindFootprint, map[string]any{
		"summary": "SSH 登录日志", "category": "log", "host": "10.0.0.5", "where": []string{"auth.log"},
	}, 5, db.StateFootprintOpen, "test", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.RecordCredentialAttempt(taskID, "10.0.0.5", "svc", "hydra", "hydra -l svc ssh://10.0.0.5", false); err != nil {
		t.Fatal(err)
	}

	w := request(http.MethodGet, "/api/loot?task="+strconv.FormatInt(taskID, 10), "")
	if w.Code != http.StatusOK {
		t.Fatalf("loot = %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "s3cr3t-value") || strings.Contains(w.Body.String(), `"secret"`) {
		t.Fatalf("loot leaked credential secret: %s", w.Body)
	}
	var got struct {
		Items []struct {
			Kind        string `json:"kind"`
			Summary     string `json:"summary"`
			Fingerprint string `json:"fingerprint"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, it := range got.Items {
		kinds[it.Kind] = true
		if it.Kind == "credential" && it.Fingerprint == "" {
			t.Fatalf("credential item has no fingerprint: %+v", it)
		}
	}
	for _, k := range []string{"foothold", "credential", "artifact", "footprint"} {
		if !kinds[k] {
			t.Fatalf("loot missing kind %s: %+v", k, got)
		}
	}
	if got.Total < 4 {
		t.Fatalf("loot total = %d", got.Total)
	}

	w = request(http.MethodGet, "/api/loot/export?format=csv&task="+strconv.FormatInt(taskID, 10), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("csv export = %d cd=%q", w.Code, w.Header().Get("Content-Disposition"))
	}
	if strings.Contains(w.Body.String(), "s3cr3t-value") {
		t.Fatalf("csv leaked credential secret: %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), "kind,state,task_id") {
		t.Fatalf("csv header missing: %s", w.Body)
	}

	w = request(http.MethodGet, "/api/credential-attempts?task="+strconv.FormatInt(taskID, 10), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hydra") {
		t.Fatalf("credential attempts = %d %s", w.Code, w.Body)
	}
	w = request(http.MethodGet, "/api/credential-attempts?format=csv&task="+strconv.FormatInt(taskID, 10), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "10.0.0.5") {
		t.Fatalf("credential attempts csv = %d %s", w.Code, w.Body)
	}
}
