package server

import (
	"strconv"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestCompactorRuntimeUsesIndependentProfile(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	profileID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: "compactor-test", Format: "openai", Model: "compact-model", APIKey: "test-key",
		Streaming: false, MaxTokens: 321, ContextWindowK: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.pg.DeleteProfile(profileID) }()
	if err := m.pg.SetSetting(settingCompactorLLMProfile, strconv.FormatInt(profileID, 10)); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingCompactorLLMProfile) }()

	task, err := m.CreateTask("compactor profile", "independent summary model", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	s := newAdmissionTestServer(m, nil)
	s.providers.byProfile = map[int64]*provEntry{}
	rt := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "compactor", forcedProfileFn: s.compactorLLMProfileID}
	c, ok := rt.compactionLLM()
	if !ok || c.Provider == nil || c.MaxTokens != 321 || !c.NonStreaming {
		t.Fatalf("compaction override=%+v ok=%v", c, ok)
	}
	sel, err := rt.current()
	if err != nil || sel.provider == nil {
		t.Fatalf("forced runtime provider: %+v err=%v", sel, err)
	}
	if sel.profileID != 0 {
		t.Fatalf("forced profile must not mutate the task LLM chain, profileID=%d", sel.profileID)
	}
	if err := m.pg.SetSetting(settingCompactorLLMProfile, "0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.compactionLLM(); ok {
		t.Fatal("cleared setting must fall back to the agent provider")
	}
}
