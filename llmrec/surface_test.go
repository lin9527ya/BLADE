package llmrec

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/lin9527ya/Norma/llm"
	"github.com/lin9527ya/blade/db"
)

type surfaceProvider struct{ completeProvider }

func (surfaceProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if c := SurfaceFrom(ctx); c != nil {
		c.Set(db.RequestSurface{Observed: true, Model: "fixture-model", SystemSHA: "s", ToolsSHA: "t", ParametersSHA: "p", EndpointSHA: "e", SystemBytes: 4, ToolsBytes: 8})
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("ok")}}, "stop", llm.Usage{InputTokens: 1}, nil
}

func TestRequestSurfaceRecordedWithoutRawBodies(t *testing.T) {
	dsn := os.Getenv("BLADE_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated BLADE_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	profile := "surface-" + uuid.NewString()
	// enabled=false disables raw body recording; the structural surface is still metered.
	rec := Wrap(surfaceProvider{}, pg, "fixture-model", profile, "", "", func() bool { return false }, "0.9.24-c3")
	if _, _, _, err := rec.Complete(t.Context(), llm.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := pg.QueryRow(`SELECT COALESCE(request_surface::text,'') FROM llm_usage WHERE profile_name=$1`, profile).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_, _ = pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile)
	for _, want := range []string{`"observed": true`, `"model": "fixture-model"`, `"build_version": "0.9.24-c3"`, `"system_sha": "s"`, `"attempts": 1`} {
		if !contains(raw, want) {
			t.Fatalf("missing %s: %s", want, raw)
		}
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
