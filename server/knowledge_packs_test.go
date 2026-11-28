package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func sampleKnowledgePack(t *testing.T, hash string, extra bool) []byte {
	t.Helper()
	entries := []byte("{\"id\":\"doc\",\"title\":\"指南\",\"body\":\"轮换证书\",\"source\":\"内部手册\"}\n")
	if hash == "" {
		hash = fmt.Sprintf("%x", sha256.Sum256(entries))
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, _ := z.Create("manifest.json")
	fmt.Fprintf(f, `{"id":"ops","name":"运维","version":"1","license":"internal","sources":["manual"],"entry_count":1,"sha256":"%s"}`, hash)
	f, _ = z.Create("entries.jsonl")
	f.Write(entries)
	if extra {
		z.Create("../unexpected")
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestKnowledgePackValidation(t *testing.T) {
	m, rows, e := readKnowledgePack(sampleKnowledgePack(t, "", false))
	if e != nil || m.ID != "ops" || len(rows) != 1 {
		t.Fatalf("%+v %v", m, e)
	}
	for _, data := range [][]byte{sampleKnowledgePack(t, "wrong", false), sampleKnowledgePack(t, "", true)} {
		if _, _, e := readKnowledgePack(data); e == nil {
			t.Fatal("invalid ZIP accepted")
		}
	}
}

func TestCompanyKnowledgePackHTTPIsolation(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := &Server{m: &Manager{pg: d}}
	mux := http.NewServeMux()
	s.registerTaskRoutes(mux)
	var companies []int64
	for i := 0; i < 2; i++ {
		id, _, err := d.Companies().UpsertCompany(fmt.Sprintf("pack-fixture-%d-%d", time.Now().UnixNano(), i), "")
		if err != nil {
			t.Fatal(err)
		}
		companies = append(companies, id)
		defer d.Exec(`DELETE FROM companies WHERE id=$1`, id)
	}
	request := func(company int64, method, extra, role string, data []byte, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, fmt.Sprintf("/api/companies/%d/knowledge-packs%s", company, extra), bytes.NewReader(data))
		r = r.WithContext(withPrincipal(r.Context(), runPrincipal{Role: role, Actor: "fixture"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d: %s", method, extra, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	raw := sampleKnowledgePack(t, "", false)
	var first, again struct{ ID int64 }
	json.Unmarshal(request(companies[0], "POST", "", "admin", raw, 200), &first)
	json.Unmarshal(request(companies[0], "POST", "", "admin", raw, 200), &again)
	if first.ID == 0 || first != again {
		t.Fatalf("non-idempotent: %+v %+v", first, again)
	}
	path := fmt.Sprintf("/%d", first.ID)
	request(companies[0], "GET", path+"/entries", "read", nil, 404)
	request(companies[1], "POST", path+"/review", "admin", []byte(`{"status":"approved"}`), 404)
	request(companies[0], "POST", path+"/review", "operator", []byte(`{"status":"approved"}`), 403)
	request(companies[0], "POST", path+"/review", "admin", []byte(`{"status":"approved"}`), 200)
	if body := request(companies[0], "GET", path+"/entries?q=certificate", "read", nil, 200); !bytes.Contains(body, []byte(`"entries":[]`)) {
		t.Fatal(string(body))
	}
	if body := request(companies[0], "GET", path+"/entries", "read", nil, 200); !bytes.Contains(body, []byte("轮换证书")) {
		t.Fatal(string(body))
	}
	request(companies[1], "GET", path+"/entries", "read", nil, 404)
	request(companies[0], "POST", path+"/review", "admin", []byte(`{"status":"disabled"}`), 200)
	request(companies[0], "GET", path+"/entries", "read", nil, 404)
}
