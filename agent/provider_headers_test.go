package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lin9527ya/Norma/transcript"
)

func TestQuotaAwareTransportAppliesCustomHeaders(t *testing.T) {
	var gotUA, gotRoute, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotRoute = r.Header.Get("X-Route")
		gotSession = r.Header.Get("X-Session")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := quotaAwareHTTPClient("", "X-Session", map[string]string{
		"User-Agent": "claude-cli/2.1.0 (external, cli)",
		"X-Route":    "relay",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := transcript.WithSessionID(context.Background(), "exp1-worker-slot1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotUA != "claude-cli/2.1.0 (external, cli)" || gotRoute != "relay" || gotSession != "exp1-worker-slot1" {
		t.Fatalf("headers UA=%q route=%q session=%q", gotUA, gotRoute, gotSession)
	}
}
