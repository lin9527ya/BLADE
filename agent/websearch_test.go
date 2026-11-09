package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func braveHTMLFixture() string {
	return `<html><body><main>
<div class="snippet svelte" data-pos="0" data-type="web">
  <div class="result-body"><div class="result-content">
    <a href="https://www.nopcommerce.com/en/release-notes" class="svelte l1">
      <div class="title search-snippet-title">Release notes - nopCommerce</div>
    </a>
    <div class="generic-snippet"><div class="content">Release Notes - <strong>nopCommerce 5.00.0</strong> (October 27th, 2026)</div></div>
  </div></div>
</div>
<div class="snippet svelte" data-pos="1" data-type="web">
  <div class="result-body"><div class="result-content">
    <a href="https://github.com/nopSolutions/nopCommerce/releases" class="svelte l1">
      <div class="title search-snippet-title">Releases · nopSolutions/nopCommerce</div>
    </a>
    <div class="generic-snippet"><div class="content">Source releases</div></div>
  </div></div>
</div>
<div class="snippet nested"><a href="https://example.com/ignored">Nested</a></div>
</main></body></html>`
}

func TestParseBraveHTML(t *testing.T) {
	results, err := parseBraveHTML([]byte(braveHTMLFixture()), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(results), results)
	}
	if results[0].URL != "https://www.nopcommerce.com/en/release-notes" || !strings.Contains(results[0].Title, "nopCommerce") {
		t.Fatalf("unexpected first result: %+v", results[0])
	}
	if results[0].Position != 1 || results[1].Position != 2 {
		t.Fatalf("positions not set: %+v", results)
	}
}

func TestWebSearchBraveHTMLFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			t.Errorf("missing q")
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, braveHTMLFixture())
	}))
	defer srv.Close()
	old := braveHTMLEndpoint
	braveHTMLEndpoint = srv.URL
	defer func() { braveHTMLEndpoint = old }()

	// brave-free without a key fails immediately; Brave HTML must take over.
	results, actual, fallback, err := WebSearchProbe(WebSearchOpts{Enabled: true, Backend: "brave-free"}, "nopCommerce", 2)
	if err != nil {
		t.Fatal(err)
	}
	if actual != "brave-html" || fallback != "brave-free" || len(results) != 2 {
		t.Fatalf("unexpected fallback: actual=%q fallback=%q results=%d", actual, fallback, len(results))
	}

	results, actual, _, err = WebSearchProbe(WebSearchOpts{Enabled: true, Backend: "brave-html"}, "nopCommerce", 1)
	if err != nil || actual != "brave-html" || len(results) != 1 {
		t.Fatalf("explicit brave-html: actual=%q results=%d err=%v", actual, len(results), err)
	}
}

func TestNewWebSearchToolName(t *testing.T) {
	tool := NewWebSearchTool(WebSearchOpts{Enabled: true, Backend: "brave-html"})
	if tool.Name() != "web_search" {
		t.Fatalf("name=%q", tool.Name())
	}
	if !strings.Contains(tool.Description(), "Brave") {
		t.Fatalf("description does not mention fallback: %s", tool.Description())
	}
}
