package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestAssetExportDoesNotSendSuccessfulPartialData(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	for _, format := range []string{"json", "csv"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/assets/export", nil)
		servePreparedAssetExport(w, r, format, func(output io.Writer) error {
			return writeAssetExportRows(output, format, nil, func(consume func(*db.Asset) error) error {
				for i := 0; i < 1001; i++ {
					if err := consume(&db.Asset{ID: int64(i), Type: "ip", IP: "192.0.2.1"}); err != nil {
						return err
					}
				}
				return errors.New("injected scan failure after former first page")
			})
		})
		if w.Code != 500 || w.Header().Get("Content-Disposition") != "" {
			t.Fatalf("%s partial result looked successful: %d", format, w.Code)
		}
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload["error"] == nil {
			t.Fatalf("unexpected partial response: %s", w.Body.String())
		}
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatal("export staging file leaked")
	}
}

func TestPreparedAssetExportKeepsCSVProtectionAndEmptyJSON(t *testing.T) {
	var output bytes.Buffer
	if err := writeAssetExportRows(&output, "csv", nil, func(consume func(*db.Asset) error) error {
		return consume(&db.Asset{Domain: "=formula", PageTitle: "+formula", Type: "root_domain"})
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(output.String(), "\ufeff"))).ReadAll()
	if err != nil || rows[1][0] != "'=formula" || rows[1][4] != "'+formula" {
		t.Fatalf("formula protection lost: %v %v", rows, err)
	}
	output.Reset()
	if err := writeAssetExportRows(&output, "json", nil, func(func(*db.Asset) error) error { return nil }); err != nil || output.String() != "[]" {
		t.Fatal("empty export invalid")
	}
}
