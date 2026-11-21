package agent

import (
	"net/http"
	"strings"
	"testing"
)

func TestObservedRequestSurfaceIsStableAndExcludesCredentials(t *testing.T) {
	body1 := `{"model":"deepseek-v4","system":[{"type":"text","text":"same"}],"tools":[{"name":"b","input_schema":{}},{"name":"a","input_schema":{}}],"max_tokens":4096,"messages":[{"role":"user","content":"secret target text"}]}`
	body2 := `{ "messages": [{"content":"different secret", "role":"user"}], "max_tokens": 4096, "tools":[{"input_schema":{}, "name":"b"},{"name":"a","input_schema":{}}], "system":[{"text":"same","type":"text"}], "model":"deepseek-v4" }`
	req1, _ := http.NewRequest(http.MethodPost, "https://user:password@gateway.example/v1/messages?api_key=leak", strings.NewReader(body1))
	req2, _ := http.NewRequest(http.MethodPost, "https://gateway.example/v1/messages", strings.NewReader(body2))
	req1.Header.Set("Authorization", "Bearer secret-token")
	req2.Header.Set("Authorization", "Bearer other-token")
	a := observedRequestSurface(req1, body1)
	b := observedRequestSurface(req2, body2)
	if !a.Observed || a.Model != "deepseek-v4" || a.Format != "anthropic" || a.SystemSHA == "" || a.ToolsSHA == "" || a.ParametersSHA == "" || a.EndpointSHA == "" {
		t.Fatalf("missing structure: %+v", a)
	}
	if a.SystemSHA != b.SystemSHA || a.ToolsSHA != b.ToolsSHA || a.ParametersSHA != b.ParametersSHA || a.EndpointSHA != b.EndpointSHA {
		t.Fatalf("equivalent structure changed: %+v %+v", a, b)
	}
	all := strings.Join([]string{a.Model, a.Format, a.SystemSHA, a.ToolsSHA, a.ParametersSHA, a.EndpointSHA}, " ")
	for _, secret := range []string{"password", "leak", "secret-token", "other-token", "secret target text", "different secret"} {
		if strings.Contains(all, secret) {
			t.Fatalf("surface leaked %q: %s", secret, all)
		}
	}
}
