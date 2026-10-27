package db

import "testing"

func TestLLMProfileCustomHeadersRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	_, _ = d.Exec(`DELETE FROM llm_profiles WHERE name IN ('custom-headers-test','custom-headers-bad')`)
	id, err := d.SaveProfile(&LLMProfile{
		Name: "custom-headers-test", Format: "openai-responses", Model: "gpt-test", APIKey: "sk-test",
		CustomHeaders: map[string]string{"User-Agent": "claude-cli/2.1.0 (external, cli)", "X-Route": "a:b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteProfile(id) })
	got, err := d.ProfileByID(id)
	if err != nil || got == nil {
		t.Fatalf("profile: %+v %v", got, err)
	}
	if got.CustomHeaders["User-Agent"] != "claude-cli/2.1.0 (external, cli)" || got.CustomHeaders["X-Route"] != "a:b" {
		t.Fatalf("headers = %+v", got.CustomHeaders)
	}
	for _, bad := range []map[string]string{
		{"Host": "evil"},
		{"Content-Type": "text/plain"},
		{"Bad Name": "x"},
		{"X-Test": "line\nbreak"},
	} {
		p := &LLMProfile{Name: "custom-headers-bad", Format: "openai", Model: "x", CustomHeaders: bad}
		if _, err := d.SaveProfile(p); err == nil {
			t.Fatalf("bad headers %v must be rejected", bad)
		}
	}
}
