package db

import (
	"strings"
	"testing"
)

func cveFixture(t *testing.T) (*DB, *AssetStore, int64) {
	t.Helper()
	d, taskID, _ := coverageMatrixFixture(t)
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM cve_library WHERE cve_id LIKE 'CVE-2099-%'`)
	})
	return d, d.Assets(), taskID
}

func addCVEService(t *testing.T, as *AssetStore, taskID int64, url string, techs []string, favicon string) int64 {
	t.Helper()
	id, err := as.UpsertHTTPService(UpsertHTTPServiceReq{
		URL: url, Technologies: techs, FaviconMMH3: favicon, TaskID: taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestParseVersionConstraint(t *testing.T) {
	cases := []struct {
		raw, version string
		ok, comp     bool
	}{
		{"", "1.2.3", true, true},
		{"*", "anything", true, true},
		{">= 1.2.0, < 1.3.0", "1.2.5", true, true},
		{">= 1.2.0, < 1.3.0", "1.3.0", false, true},
		{"~1.2.3", "1.2.9", true, true},
		{"~1.2.3", "1.3.0", false, true},
		{"^1.2.3", "1.9.0", true, true},
		{"^1.2.3", "2.0.0", false, true},
		{"^0.2.3", "0.2.9", true, true},
		{"^0.2.3", "0.3.0", false, true},
		{"^0.0.3", "0.0.4", false, true},
		{"1.2.x", "1.2.9", true, true},
		{"1.2.x", "1.3.0", false, true},
		{"1.x", "1.9.0", true, true},
		{"!= 1.2.3", "1.2.4", true, true},
		{"1.2.3p1", "1.2.3", true, true},
	}
	for _, tc := range cases {
		c, err := ParseVersionConstraint(tc.raw)
		if err != nil {
			t.Fatalf("ParseVersionConstraint(%q): %v", tc.raw, err)
		}
		ok, comp := c.SatisfiedBy(tc.version)
		if ok != tc.ok || comp != tc.comp {
			t.Errorf("%q against %q = (%v,%v), want (%v,%v)", tc.version, tc.raw, ok, comp, tc.ok, tc.comp)
		}
	}
	for _, bad := range []string{"||", ">= ", "abc", "1.2 - 1.3"} {
		if _, err := ParseVersionConstraint(bad); err == nil {
			t.Errorf("ParseVersionConstraint(%q) should fail", bad)
		}
	}
	if ok, comp := mustConstraint(t, ">= 2.0").SatisfiedBy("not-a-version"); ok || comp {
		t.Fatal("unparseable asset version must be comparable=false")
	}
}

func mustConstraint(t *testing.T, raw string) VersionConstraint {
	t.Helper()
	c, err := ParseVersionConstraint(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSplitProductVersion(t *testing.T) {
	cases := []struct{ in, product, version string }{
		{"WordPress 6.2", "wordpress", "6.2"},
		{"nginx/1.24.0", "nginx", "1.24.0"},
		{"Apache Tomcat/9.0.65", "apache tomcat", "9.0.65"},
		{"OpenSSH_8.9p1", "openssh", "8.9p1"},
		{"wordpress", "wordpress", ""},
		{"Apache/2.4.49 (Unix)", "apache", "2.4.49"},
	}
	for _, tc := range cases {
		p, v := splitProductVersion(tc.in)
		if p != tc.product || v != tc.version {
			t.Errorf("splitProductVersion(%q) = (%q,%q), want (%q,%q)", tc.in, p, v, tc.product, tc.version)
		}
	}
}

func TestCVECreateListUpdate(t *testing.T) {
	_, as, _ := cveFixture(t)
	c, err := as.CreateCVE(CVE{
		CVEID: "cve-2099-0001", Title: "test-cve: Log4j RCE", Severity: SeverityCritical, CVSSScore: 10,
		Description: "## 影响\n远程代码执行", AffectedProducts: []string{"Log4j"}, VersionConstraint: ">= 2.0.0, < 2.15.0",
		Fingerprint: PlaybookFingerprint{Technologies: []string{"Log4j"}},
		ProbeGuide:  "访问 ${jndi:ldap://...}", Refs: []string{"https://example.com/cve"},
		ExploitScript: "#!/bin/sh\necho pwn\n", ExploitLanguage: "bash", ExploitFilename: "cve.sh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.CVEID != "CVE-2099-0001" || c.Revision != 1 || !c.HasExploit || c.ExploitSHA256 == "" {
		t.Fatalf("create: %+v", c)
	}
	got, err := as.GetCVEByCVEID("CVE-2099-0001")
	if err != nil || got == nil || got.Description == "" || got.ExploitScript == "" {
		t.Fatalf("get by cve id: %+v %v", got, err)
	}
	list, total, err := as.ListCVEs(CVEFilter{Q: "log4j", Status: "active"})
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("list: %+v total=%d err=%v", list, total, err)
	}
	if list[0].ExploitScript != "" || !list[0].HasExploit {
		t.Fatalf("list must not load script body but must report has_exploit: %+v", list[0])
	}
	got.Title = "test-cve: updated"
	got.AffectedProducts = []string{"log4j"}
	updated, err := as.UpdateCVE(got.ID, *got)
	if err != nil || updated == nil || updated.Revision != 2 || updated.Title != "test-cve: updated" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	cleared, err := as.ClearCVEScript(got.ID)
	if err != nil || cleared == nil || cleared.HasExploit || cleared.ExploitSHA256 != "" {
		t.Fatalf("clear script: %+v %v", cleared, err)
	}
	if _, err := as.SetCVEStatus(got.ID, CVEStatusDeprecated, ""); err == nil {
		t.Fatal("deprecate without reason should fail")
	}
	if n, err := as.SetCVEStatus(got.ID, CVEStatusDeprecated, "测试废弃"); err != nil || n != 1 {
		t.Fatalf("deprecate: n=%d err=%v", n, err)
	}
	if _, total, _ := as.ListCVEs(CVEFilter{Status: "active"}); total != 0 {
		t.Fatal("deprecated CVE must not appear in active list")
	}
}

func TestCVEMatchingProductVersion(t *testing.T) {
	_, as, taskID := cveFixture(t)
	_, err := as.CreateCVE(CVE{
		CVEID: "CVE-2099-0002", Title: "test-cve: Log4j", Severity: SeverityCritical,
		AffectedProducts: []string{"log4j"}, VersionConstraint: ">= 2.0.0, < 2.15.0",
		Fingerprint: PlaybookFingerprint{Technologies: []string{"log4j"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	compatibleID := addCVEService(t, as, taskID, "http://cve-log4j.matrix-test.example", []string{"Apache Log4j 2.14.1"}, "")
	matches, err := as.MatchCVEsForAsset(taskID, compatibleID, 5)
	if err != nil || len(matches) != 1 {
		t.Fatalf("compatible match: %+v %v", matches, err)
	}
	if matches[0].VersionMatch != "compatible" || matches[0].MatchedProduct == "" || matches[0].Score < 40 {
		t.Fatalf("compatible detail: %+v", matches[0])
	}
	unknownID := addCVEService(t, as, taskID, "http://cve-log4j-unknown.matrix-test.example", []string{"log4j"}, "")
	matches, err = as.MatchCVEsForAsset(taskID, unknownID, 5)
	if err != nil || len(matches) != 1 || matches[0].VersionMatch != "version_unknown" {
		t.Fatalf("unknown version should be a candidate: %+v %v", matches, err)
	}
	mismatchID := addCVEService(t, as, taskID, "http://cve-log4j-new.matrix-test.example", []string{"log4j 2.17.0"}, "")
	m, err := as.MatchCVEForAsset(taskID, "CVE-2099-0002", mismatchID)
	if err != nil || m == nil || m.Score != 0 || m.VersionMatch != "mismatch" {
		t.Fatalf("mismatch must be excluded with reasons: %+v %v", m, err)
	}
	if !strings.Contains(strings.Join(m.MatchReasons, " "), "不满足") {
		t.Fatalf("mismatch reasons: %+v", m.MatchReasons)
	}
	taskMatches, err := as.MatchCVEsForTask(taskID, 5)
	if err != nil || len(taskMatches) == 0 || taskMatches[0].AssetID == 0 {
		t.Fatalf("task match: %+v %v", taskMatches, err)
	}
}

func TestCVEMatchingGenericNeedsVersion(t *testing.T) {
	_, as, taskID := cveFixture(t)
	_, err := as.CreateCVE(CVE{
		CVEID: "CVE-2099-0003", Title: "test-cve: nginx", AffectedProducts: []string{"nginx"},
		VersionConstraint: ">= 1.20.0, < 1.25.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	unknownID := addCVEService(t, as, taskID, "http://cve-nginx.matrix-test.example", []string{"nginx"}, "")
	if matches, _ := as.MatchCVEsForAsset(taskID, unknownID, 5); len(matches) != 0 {
		t.Fatalf("generic product without version must not match: %+v", matches)
	}
	knownID := addCVEService(t, as, taskID, "http://cve-nginx-2.matrix-test.example", []string{"nginx/1.24.0"}, "")
	matches, err := as.MatchCVEsForAsset(taskID, knownID, 5)
	if err != nil || len(matches) != 1 || matches[0].VersionMatch != "compatible" {
		t.Fatalf("generic+version should match: %+v %v", matches, err)
	}
}

func TestCVEMatchingFaviconFallback(t *testing.T) {
	_, as, taskID := cveFixture(t)
	_, err := as.CreateCVE(CVE{
		CVEID: "CVE-2099-0004", Title: "test-cve: favicon", Fingerprint: PlaybookFingerprint{FaviconMMH3: "-777"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := addCVEService(t, as, taskID, "http://cve-favicon.matrix-test.example", nil, "-777")
	matches, err := as.MatchCVEsForAsset(taskID, id, 5)
	if err != nil || len(matches) != 1 || matches[0].Score < 100 {
		t.Fatalf("favicon match: %+v %v", matches, err)
	}
}

func TestCVEValidationAndCounters(t *testing.T) {
	_, as, _ := cveFixture(t)
	for _, c := range []CVE{
		{CVEID: "not-a-cve", Title: "x", AffectedProducts: []string{"x"}},
		{CVEID: "CVE-2099-0005", Title: "x", AffectedProducts: []string{"x"}, VersionConstraint: "||"},
		{CVEID: "CVE-2099-0006", Title: "x", AffectedProducts: []string{"x"}, ExploitScript: "echo", ExploitLanguage: "brainfuck"},
		{CVEID: "CVE-2099-0007", Title: "x"},
	} {
		if _, err := as.CreateCVE(c); err == nil {
			t.Fatalf("invalid CVE should be rejected: %+v", c)
		}
	}
	c, err := as.CreateCVE(CVE{CVEID: "CVE-2099-0008", Title: "test-cve: counters", AffectedProducts: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.ExploitSHA256 != "" || c.HasExploit {
		t.Fatal("no script should mean has_exploit=false")
	}
	as.TouchCVEUses([]int64{c.ID})
	as.RecordCVEExecution(c.ID)
	got, _ := as.GetCVE(c.ID)
	if got.Uses != 1 || got.Executions != 1 || got.LastUsedAt == nil || got.LastExecutedAt == nil {
		t.Fatalf("counters: %+v", got)
	}
}
