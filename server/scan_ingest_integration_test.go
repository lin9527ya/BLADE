package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
)

func TestIngestScanOutputWritesAssetsAndFacts(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("scan ingest", "ingest scanner output", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "httpx.jsonl"),
		[]byte(`{"url":"https://app.example.com","status_code":200,"title":"App","tech":["nginx"],"content_length":42,"host":"app.example.com"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskNum, _ := strconv.ParseInt(task.ID, 10, 64)
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: taskNum, ExplorationID: task.ExpID})
	s := &Server{m: m}
	res, err := s.ingestScanOutput(ctx, scanIngestInput{Format: "httpx-jsonl", Path: "httpx.jsonl"}, &actool.ToolContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.AssetsWritten != 1 || res.FactsWritten != 1 {
		t.Fatalf("result=%+v", res)
	}
	var assets, facts int
	if err := m.pg.QueryRow(`SELECT count(*) FROM assets WHERE 'https://app.example.com' = url`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.QueryRow(`SELECT count(*) FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND n.kind='fact' AND n.payload->>'summary' LIKE 'httpx：%'`, taskNum).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if assets != 1 || facts != 1 {
		t.Fatalf("assets=%d facts=%d", assets, facts)
	}
}
