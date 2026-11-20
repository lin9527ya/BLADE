package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

func TestExplorationPromptMigrationKeepsCustomizations(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	for _, key := range []string{"planner", "worker"} {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil {
			t.Fatalf("agent %s: %v", key, err)
		}
		original, _ := m.pg.CurrentPrompt(a.ID)
		extra, _ := m.pg.CurrentPromptAppend(a.ID)
		t.Cleanup(func() { _, _ = m.pg.SavePromptWithAppend(a.ID, original, extra, "restore fixture", "test") })
		old, err := os.ReadFile("testdata/prompts/" + key + "-v0.9.19.body.md")
		if err != nil {
			t.Fatal(err)
		}
		for _, custom := range []bool{false, true} {
			body := string(old)
			if custom {
				body += "\n操作者的额外规则"
			}
			if _, err := m.pg.SavePromptWithAppend(a.ID, body, "保留我的追加要求", "test", "test"); err != nil {
				t.Fatal(err)
			}
			migrateExplorationPrompts(m.pg)
			got, _ := m.pg.CurrentPrompt(a.ID)
			want := agent.BuiltinPromptSeeds()[key]
			if custom {
				want = body
			}
			if got != want {
				t.Fatalf("%s custom=%v: wrong migrated body", key, custom)
			}
			appendText, _ := m.pg.CurrentPromptAppend(a.ID)
			if appendText != "保留我的追加要求" {
				t.Fatal("append lost")
			}
			versions, _ := m.pg.ListPromptVersions(a.ID)
			migrateExplorationPrompts(m.pg)
			after, _ := m.pg.ListPromptVersions(a.ID)
			if len(versions) != len(after) {
				t.Fatal("repeat startup created duplicate versions")
			}
		}
	}
}

func TestFactDescriptionMigrationPreservesToolPolicy(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	var seed agent.ToolSeed
	for _, s := range agent.BuiltinToolSeeds() {
		if s.Key == "record_fact" {
			seed = s
		}
	}
	old, err := os.ReadFile("testdata/prompts/record-fact-v0.9.22.description.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Ensure a row exists without resetting a row other tests deliberately edited.
	if err := m.pg.SeedTool(seed.Key, seed.Desc, nil, nil); err != nil {
		t.Fatal(err)
	}
	row, err := m.pg.GetTool(seed.Key)
	if err != nil {
		t.Fatal(err)
	}
	originalAgents, _ := json.Marshal(row.Agents)
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`UPDATE tools SET description=$2,schema=$3,enabled=$4,agents=$5,deferred=$6 WHERE key=$1`, row.Key, row.Description, row.Schema, row.Enabled, originalAgents, row.Deferred)
	})
	if _, err := m.pg.Exec(`UPDATE tools SET description=$1,enabled=false,agents='["worker"]',schema='{"type":"object","properties":{"summary":{"type":"string","default":"mine"}}}' WHERE key='record_fact'`, string(old)); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	got, err := m.pg.GetTool("record_fact")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != seed.Desc || got.Enabled || len(got.Agents) != 1 || got.Agents[0] != "worker" {
		t.Fatalf("policy reset: %+v", got)
	}
	if err := m.pg.UpdateToolDescriptionIfUnchanged(seed.Key, seed.Desc, "my custom description"); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	got, _ = m.pg.GetTool(seed.Key)
	if got.Description != "my custom description" {
		t.Fatal("custom tool description overwritten")
	}
}

func TestFactBatchRetainsIdentityAndPriority(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	for i := 0; i < 3; i++ {
		task.NotifyFact("fact", int64(10+i), int64(20+i), "独立观察", []int64{int64(i + 1)})
	}
	ev := task.drainTriggers()[0]
	if len(ev.Facts) != 3 || ev.Facts[0].FactID != 20 || ev.Facts[2].IntentID != 12 {
		t.Fatalf("batch lost branches: %+v", ev)
	}
	task.NotifyFinding(1, "must retain")
	for i := 0; i < 100; i++ {
		task.NotifyAsset("ordinary asset")
		task.flushLowPriority()
	}
	found := false
	for _, event := range task.drainTriggers() {
		if event.Kind == "finding" {
			found = true
		}
	}
	if !found {
		t.Fatal("ordinary batches displaced a finding")
	}
}

func TestHistoricalBuiltinPromptFingerprints(t *testing.T) {
	for _, version := range []string{"v0.9.19", "v0.9.22"} {
		for _, key := range []string{"planner", "worker"} {
			body, err := os.ReadFile("testdata/prompts/" + key + "-" + version + ".body.md")
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(strings.TrimSpace(string(body))))
			hashes := plannerBuiltinPromptSHA256
			if key == "worker" {
				hashes = workerBuiltinPromptSHA256
			}
			if !hashes[fmt.Sprintf("%x", sum[:])] {
				t.Fatalf("lost builtin fingerprint %s %s", key, version)
			}
		}
	}
	// v0.9.23 planner snapshot must stay recognized so upgrades migrate smoothly.
	v0923Body, err := os.ReadFile("testdata/prompts/planner-v0.9.23.body.md")
	if err != nil {
		t.Fatal(err)
	}
	sum0923 := sha256.Sum256([]byte(strings.TrimSpace(string(v0923Body))))
	if !plannerBuiltinPromptSHA256[fmt.Sprintf("%x", sum0923[:])] {
		t.Fatalf("lost builtin fingerprint planner v0.9.23")
	}
	if body := agent.PlannerPromptBody(); !strings.Contains(body, "no_output") {
		t.Fatalf("builtin planner prompt missing no_output guidance")
	}
}
