package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNmapXML(t *testing.T) {
	res := &scanIngestResult{}
	if err := parseNmapXML(strings.NewReader(`<?xml version="1.0"?>
<nmaprun><host><status state="up"/><address addr="10.0.0.5" addrtype="ipv4"/>
<hostnames><hostname name="app.example.com" type="user"/></hostnames>
<ports><port protocol="tcp" portid="22"><state state="open"/><service name="ssh"/></port>
<port protocol="tcp" portid="443"><state state="open"/><service name="https"/></port></ports></host></nmaprun>`), res); err != nil {
		t.Fatal(err)
	}
	if len(res.Assets) != 3 { // ip + ssh service + https service
		t.Fatalf("assets=%+v", res.Assets)
	}
	if len(res.Facts) != 1 || !strings.Contains(res.Facts[0]["summary"].(string), "2 个端口") {
		t.Fatalf("facts=%+v", res.Facts)
	}
	if len(res.URLs) != 1 || !strings.Contains(res.URLs[0], "https://10.0.0.5:443") {
		t.Fatalf("urls=%+v", res.URLs)
	}
}

func TestParseScanJSONL(t *testing.T) {
	res := &scanIngestResult{}
	if err := parseNucleiJSONL(strings.NewReader(`{"template-id":"CVE-2026-1","info":{"name":"demo","severity":"high"},"host":"https://app.example.com","matched-at":"https://app.example.com/x"}`+"\n"), res); err != nil {
		t.Fatal(err)
	}
	if len(res.Assets) != 1 || len(res.Facts) != 1 {
		t.Fatalf("nuclei assets=%+v facts=%+v", res.Assets, res.Facts)
	}
	res = &scanIngestResult{}
	if err := parseHTTPXJSONL(strings.NewReader(`{"url":"https://app.example.com","status_code":200,"title":"App","tech":["nginx"],"content_length":42,"host":"app.example.com"}`+"\n"), res); err != nil {
		t.Fatal(err)
	}
	if len(res.Assets) != 1 || res.Assets[0]["status_code"] != 200 {
		t.Fatalf("httpx assets=%+v", res.Assets)
	}
	res = &scanIngestResult{}
	if err := parseFFUFJSON(strings.NewReader(`{"results":[{"input":"admin","status":200,"length":10,"url":"https://app.example.com/admin"}]}`), res); err != nil {
		t.Fatal(err)
	}
	if len(res.Assets) != 1 || len(res.Facts) != 1 {
		t.Fatalf("ffuf assets=%+v facts=%+v", res.Assets, res.Facts)
	}
}

func TestResolveScanPathConfinesToWorkDir(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "scan.json")
	if err := os.WriteFile(inside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveScanPath(dir, "scan.json"); err != nil || got != inside {
		t.Fatalf("inside path: %q %v", got, err)
	}
	if _, err := resolveScanPath(dir, "../outside.json"); err == nil {
		t.Fatal("parent traversal must be refused")
	}
	if _, err := resolveScanPath(dir, "/etc/passwd"); err == nil {
		t.Fatal("absolute path outside work dir must be refused")
	}
	outside := t.TempDir()
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(filepath.Join(outside, "x.json"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := resolveScanPath(dir, "link.json"); err == nil {
		t.Fatal("symlink escaping work dir must be refused")
	}
}
