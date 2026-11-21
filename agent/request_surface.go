package agent

import (
	"encoding/json"
	"github.com/lin9527ya/blade/db"
	"net/http"
)

func observedRequestSurface(req *http.Request, body string) db.RequestSurface {
	sig, err := requestCacheSignature(body)
	if err != nil || sig.Model == "" {
		return db.RequestSurface{}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &fields) != nil {
		return db.RequestSurface{}
	}
	params := map[string]json.RawMessage{}
	for _, key := range []string{"model", "max_tokens", "max_completion_tokens", "max_output_tokens", "thinking", "reasoning", "temperature", "top_p", "tool_choice", "parallel_tool_calls"} {
		if v, ok := fields[key]; ok {
			params[key] = v
		}
	}
	raw, _ := json.Marshal(params)
	format := "chat"
	if _, ok := fields["system"]; ok {
		format = "anthropic"
	}
	if _, ok := fields["input"]; ok {
		format = "responses"
	}
	// Userinfo, query strings and auth/routing headers are intentionally excluded.
	endpoint := req.URL.Scheme + "://" + req.URL.Host + req.URL.EscapedPath()
	endpointRaw, _ := json.Marshal(endpoint)
	return db.RequestSurface{Observed: true, Model: sig.Model, Format: format, SystemSHA: sig.System.SHA256, ToolsSHA: sig.Tools.SHA256, ParametersSHA: cachePartOf(raw).SHA256, EndpointSHA: cachePartOf(endpointRaw).SHA256, SystemBytes: sig.System.Bytes, ToolsBytes: sig.Tools.Bytes}
}
