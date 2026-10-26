package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func cveAPIRequest(t *testing.T, s *Server, method, path string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, body)
	}
	token, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestCVEsAPI(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM cve_library WHERE cve_id LIKE 'CVE-2098-%'`) })

	input := db.CVE{
		CVEID: "cve-2098-0001", Title: "CVE API test", Severity: db.SeverityHigh, CVSSScore: 8.8,
		Description: "详细描述", ProbeGuide: "探测指引", AffectedProducts: []string{"log4j"},
		VersionConstraint: ">= 2.0.0, < 2.15.0",
		Fingerprint:       db.PlaybookFingerprint{Technologies: []string{"log4j"}},
		ExploitScript:     "#!/bin/sh\necho api-test\n", ExploitLanguage: "bash", ExploitFilename: "cve.sh",
	}
	body, _ := json.Marshal(input)
	w := request("POST", "/api/cves", string(body))
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created db.CVE
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == 0 || !created.HasExploit {
		t.Fatalf("create body: %v %s", err, w.Body)
	}
	if created.CVEID != "CVE-2098-0001" {
		t.Fatalf("cve id not normalized: %s", created.CVEID)
	}

	w = request("GET", "/api/cves?status=all&q=CVE", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "CVE-2098-0001") || strings.Contains(w.Body.String(), "echo api-test") {
		t.Fatalf("list must include metadata but not script: %d %s", w.Code, w.Body)
	}
	w = request("GET", fmt.Sprintf("/api/cves/%d", created.ID), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "echo api-test") || !strings.Contains(w.Body.String(), "探测指引") {
		t.Fatalf("detail must include script and probe guide: %d %s", w.Code, w.Body)
	}

	// 重复 CVE 编号 409。
	if w = request("POST", "/api/cves", string(body)); w.Code != 409 {
		t.Fatalf("duplicate should 409: %d %s", w.Code, w.Body)
	}
	// 非法 CVE 编号 400。
	bad := input
	bad.CVEID = "not-a-cve"
	bb, _ := json.Marshal(bad)
	if w = request("POST", "/api/cves", string(bb)); w.Code != 400 {
		t.Fatalf("bad cve id should 400: %d %s", w.Code, w.Body)
	}

	// 更新标题；script 保持不变。
	updated := created
	updated.Title = "CVE API test updated"
	ub, _ := json.Marshal(updated)
	w = request("PUT", fmt.Sprintf("/api/cves/%d", created.ID), string(ub))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "updated") {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}

	// 废弃必须给理由。
	if w = request("PATCH", fmt.Sprintf("/api/cves/%d", created.ID), `{"status":"deprecated"}`); w.Code != 400 {
		t.Fatalf("deprecate without reason should 400: %d %s", w.Code, w.Body)
	}
	w = request("PATCH", fmt.Sprintf("/api/cves/%d", created.ID), `{"status":"deprecated","reason":"测试"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "deprecated") {
		t.Fatalf("deprecate: %d %s", w.Code, w.Body)
	}
	w = request("GET", "/api/cves?status=active&q=CVE", "")
	if strings.Contains(w.Body.String(), "CVE-2098-0001") {
		t.Fatalf("deprecated should not appear in active list: %s", w.Body)
	}

	// multipart 脚本上传：从 .py 后缀推语言，SHA 由服务端算。
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "exploit.py")
	_, _ = fw.Write([]byte("print('uploaded')\n"))
	_ = mw.Close()
	w = cveAPIRequest(t, s, http.MethodPost, fmt.Sprintf("/api/cves/%d/script", created.ID), &buf, mw.FormDataContentType())
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"exploit_language":"python3"`) ||
		!strings.Contains(w.Body.String(), "uploaded") {
		t.Fatalf("script upload: %d %s", w.Code, w.Body)
	}

	w = request("DELETE", fmt.Sprintf("/api/cves/%d/script", created.ID), "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "uploaded") {
		t.Fatalf("clear script: %d %s", w.Code, w.Body)
	}

	w = request("DELETE", fmt.Sprintf("/api/cves/%d", created.ID), "")
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	w = request("GET", fmt.Sprintf("/api/cves/%d", created.ID), "")
	if w.Code != 404 {
		t.Fatalf("deleted get should 404: %d %s", w.Code, w.Body)
	}
}

func TestCVEsAPIRequiresAuth(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	r := httptest.NewRequest("GET", "/api/cves", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("unauthenticated cves should 401, got %d", w.Code)
	}
}

// TestSeedCVEInterceptToolMigration：老库已保存 intercept_enabled_tools 时，
// cve_exploit 必须被追加进规则面，且迁移幂等、不覆盖用户已有工具选择。
func TestSeedCVEInterceptToolMigration(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	if err := pg.SetSetting("intercept_enabled_tools", `["Bash","run_execution"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`DELETE FROM settings WHERE key='intercept_cve_tool_v1'`); err != nil {
		t.Fatal(err)
	}
	s.seedCVEInterceptTool()
	names, err := s.m.interceptor.GetEnabledTools()
	if err != nil {
		t.Fatal(err)
	}
	has := func(list []string, want string) bool {
		for _, n := range list {
			if n == want {
				return true
			}
		}
		return false
	}
	if !has(names, "Bash") || !has(names, "run_execution") || !has(names, "cve_exploit") {
		t.Fatalf("migration should append without dropping: %+v", names)
	}
	before := len(names)
	if _, err := pg.Exec(`DELETE FROM settings WHERE key='intercept_cve_tool_v1'`); err != nil {
		t.Fatal(err)
	}
	s.seedCVEInterceptTool()
	names, _ = s.m.interceptor.GetEnabledTools()
	if len(names) != before {
		t.Fatalf("migration should be idempotent: %d → %d", before, len(names))
	}
}

// TestCVEToolsSeededAndBound：新工具必须进入 tool catalog，并按 code default 绑定到
// lookup_cves=worker/planner/mainagent、cve_exploit=worker/planner。
func TestCVEToolsSeededAndBound(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	contains := func(list []string, want string) bool {
		for _, n := range list {
			if n == want {
				return true
			}
		}
		return false
	}
	lookup, err := s.m.pg.GetTool("lookup_cves")
	if err != nil || lookup == nil || !contains(lookup.Agents, "worker") ||
		!contains(lookup.Agents, "planner") || !contains(lookup.Agents, "mainagent") {
		t.Fatalf("lookup_cves seeded/bound wrong: %+v %v", lookup, err)
	}
	exploit, err := s.m.pg.GetTool("cve_exploit")
	if err != nil || exploit == nil || !contains(exploit.Agents, "worker") || !contains(exploit.Agents, "planner") {
		t.Fatalf("cve_exploit seeded/bound wrong: %+v %v", exploit, err)
	}
	if contains(exploit.Agents, "mainagent") {
		t.Fatalf("cve_exploit must not bind mainagent (no ExecService): %+v", exploit.Agents)
	}
}
