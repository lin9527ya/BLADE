package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManualVisionWire(t *testing.T) {
	for _, tc := range []struct{ format, message, expect string }{{"anthropic", `{"messages":[{"role":"user","content":[{"type":"text","text":"?"}]}]}`, `"source":{"data":"AQI="`}, {"openai", `{"messages":[{"role":"user","content":"?"}]}`, `data:image/png;base64,AQI=`}, {"openai-responses", `{"input":[{"role":"user","content":"?"}]}`, `"type":"input_image"`}} {
		req, _ := http.NewRequest("POST", "http://localhost", strings.NewReader(tc.message))
		out, err := addManualImages(req, manualImages{tc.format, []ChatImage{{MIME: "image/png", Data: []byte{1, 2}}}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(out.Body)
		out.Body.Close()
		if !strings.Contains(string(b), tc.expect) {
			t.Fatalf("%s: %s", tc.format, b)
		}
	}
}

func TestManualVisionModelEndpoints(t *testing.T) {
	for _, c := range []struct{ kind, response, marker string }{
		{"anthropic", `{"type":"message","role":"assistant","content":[{"type":"text","text":"blue"}],"stop_reason":"end_turn"}`, `"media_type":"image/png"`},
		{"openai", `{"choices":[{"message":{"role":"assistant","content":"blue"},"finish_reason":"stop"}]}`, `"type":"image_url"`},
		{"openai-responses", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"blue"}]}]}`, `"type":"input_image"`},
	} {
		t.Run(c.kind, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), c.marker) || !strings.Contains(string(raw), "AQI=") || strings.Contains(string(raw), `"tools":[{`) {
					t.Errorf("wire body %s", raw)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, c.response)
			}))
			defer up.Close()
			cfg := ConfigFrom(c.kind, "mock", up.URL, "mock-key", "")
			provider, err := cfg.NewProvider()
			if err != nil {
				t.Fatal(err)
			}
			answer, err := ManualImageAnswer(t.Context(), provider, c.kind, "color?", []ChatImage{{"image/png", []byte{1, 2}}})
			if err != nil || answer != "blue" {
				t.Fatalf("%s %v", answer, err)
			}
		})
	}
}
