package traffic

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProtobufPreviewNoiseAndPinningAlerts(t *testing.T) {
	// Synthesize wire protobuf: field 1 varint (wire type 0)=42, field 2 length-delimited (wire type 2)="alice"
	var pb []byte
	pb = binary.AppendUvarint(pb, 1<<3)
	pb = binary.AppendUvarint(pb, 42)
	pb = binary.AppendUvarint(pb, (2<<3)|2)
	pb = binary.AppendUvarint(pb, 5)
	pb = append(pb, []byte("alice")...)

	preview, ok := DecodeProtobufPreview(pb)
	if !ok {
		t.Fatal("DecodeProtobufPreview failed")
	}
	if !strings.Contains(preview, "f1 (varint): 42") || !strings.Contains(preview, `f2 (str): "alice"`) {
		t.Fatalf("unexpected protobuf preview:\n%s", preview)
	}

	if !IsNoiseHost("ulogs.umeng.com") || IsNoiseHost("api.acme-corp.cn") {
		t.Fatal("IsNoiseHost classification mismatch")
	}

	tr, err := Open(filepath.Join(t.TempDir(), "traffic"), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer tr.Close()

	tr.RecordPinningAlert("api.bank.cn", "tls: bad certificate", "mobile")
	tr.RecordPinningAlert("api.bank.cn", "tls: bad certificate", "mobile")
	alerts := tr.PinningAlerts()
	if len(alerts) != 1 || alerts[0].Count != 2 || alerts[0].Source != "mobile" {
		t.Fatalf("unexpected pinning alerts: %+v", alerts)
	}
	tr.ClearPinningAlerts()
	if len(tr.PinningAlerts()) != 0 {
		t.Fatal("expected pinning alerts cleared")
	}

	// Verify CA bundle formats and Apple mobileconfig.
	der, err := tr.CACertDER()
	if err != nil {
		t.Fatalf("CACertDER: %v", err)
	}
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatalf("invalid CA DER: %v", err)
	}
	hash, err := tr.CASubjectHash()
	if err != nil || len(hash) != 8 {
		t.Fatalf("CASubjectHash: hash=%q err=%v", hash, err)
	}
	mc, err := tr.AppleMobileConfig("BLADE")
	if err != nil || !strings.Contains(string(mc), "com.apple.security.root") {
		t.Fatalf("AppleMobileConfig invalid: %v", err)
	}
}

func TestMobileProxyAuthCIDRAndCAToken(t *testing.T) {
	tr, err := Open(filepath.Join(t.TempDir(), "traffic"), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer tr.Close()

	mp, err := NewMobileProxy(tr, "127.0.0.1:0", []string{"127.0.0.1/32"}, "u1", "secret99")
	if err != nil {
		t.Fatalf("NewMobileProxy: %v", err)
	}

	// 1. Disallowed source IP -> 403
	reqBadIP := httptest.NewRequest(http.MethodGet, "http://api.example.test/", nil)
	reqBadIP.RemoteAddr = "8.8.8.8:12345"
	rr := httptest.NewRecorder()
	mp.ServeHTTP(rr, reqBadIP)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for outside CIDR, got %d", rr.Code)
	}

	// 2. Missing Proxy-Authorization -> 407
	reqNoAuth := httptest.NewRequest(http.MethodConnect, "api.example.test:443", nil)
	reqNoAuth.RemoteAddr = "127.0.0.1:22222"
	rr = httptest.NewRecorder()
	mp.ServeHTTP(rr, reqNoAuth)
	if rr.Code != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407 without Proxy-Authorization, got %d", rr.Code)
	}

	// 3. Wrong credentials -> 407
	reqWrongAuth := httptest.NewRequest(http.MethodConnect, "api.example.test:443", nil)
	reqWrongAuth.RemoteAddr = "127.0.0.1:22222"
	reqWrongAuth.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("u1:wrong")))
	rr = httptest.NewRecorder()
	mp.ServeHTTP(rr, reqWrongAuth)
	if rr.Code != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407 for bad password, got %d", rr.Code)
	}

	// 4. Signed CA portal token works over direct HTTP GET without Proxy-Authorization.
	tok := mp.MintCAToken(5 * time.Minute)
	reqCA := httptest.NewRequest(http.MethodGet, "/ca?fmt=pem&token="+tok, nil)
	reqCA.RemoteAddr = "127.0.0.1:33333"
	rr = httptest.NewRecorder()
	mp.ServeHTTP(rr, reqCA)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "BEGIN CERTIFICATE") {
		t.Fatalf("expected valid CA PEM with signed token, code=%d body=%s", rr.Code, rr.Body.String())
	}

	// Tampered token rejected -> 403.
	reqBadTok := httptest.NewRequest(http.MethodGet, "/ca?fmt=pem&token="+tok+"bad", nil)
	reqBadTok.RemoteAddr = "127.0.0.1:33333"
	rr = httptest.NewRecorder()
	mp.ServeHTTP(rr, reqBadTok)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for invalid CA token, got %d", rr.Code)
	}
}

func TestOpenMigratesLegacySQLiteIndex(t *testing.T) {
	dir := t.TempDir()
	idxDir := filepath.Join(dir, "_index")
	if err := os.MkdirAll(idxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := sql.Open("sqlite", filepath.Join(idxDir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacyDB.Exec(`CREATE TABLE exchanges (
		id TEXT PRIMARY KEY,
		ts INTEGER NOT NULL,
		host TEXT NOT NULL,
		method TEXT NOT NULL,
		url_template TEXT NOT NULL,
		url TEXT NOT NULL,
		status INTEGER,
		content_type TEXT,
		req_len INTEGER,
		resp_len INTEGER,
		path TEXT
	);`)
	_ = legacyDB.Close()
	if err != nil {
		t.Fatal(err)
	}

	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Open on legacy SQLite index failed: %v", err)
	}
	defer tr.Close()
}

func TestTransparentBridgeSNIAndSyntheticHookCapture(t *testing.T) {
	// 1. Verify ExtractTLSSNI parses real ClientHello produced by crypto/tls
	c1, c2 := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer c1.Close()
		tc := tls.Client(c1, &tls.Config{ServerName: "api.transparent-test.cn", InsecureSkipVerify: true})
		_ = tc.Handshake()
		errCh <- nil
	}()
	rec := make([]byte, 2048)
	n, _ := c2.Read(rec)
	_ = c2.Close()
	<-errCh
	if sni := ExtractTLSSNI(rec[:n]); sni != "api.transparent-test.cn" {
		t.Fatalf("ExtractTLSSNI got %q, want api.transparent-test.cn", sni)
	}

	// 2. Mock upstream MITM listener answering HTTP 200 CONNECT and verify TransparentBridge forwards SNI CONNECT cleanly.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotConnect := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err == nil {
			gotConnect <- req.Host
			_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		}
	}()

	tr, err := Open(t.TempDir(), ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	bridge, err := NewMobileTransparentBridge(tr, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	go func() { _ = bridge.StartWithReady(ready) }()
	<-ready
	defer bridge.Close()

	cli, err := net.DialTimeout("tcp", bridge.Addr(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		tc := tls.Client(cli, &tls.Config{ServerName: "flutter.backend.local", InsecureSkipVerify: true})
		_ = tc.Handshake()
		_ = tc.Close()
	}()
	select {
	case host := <-gotConnect:
		if host != "flutter.backend.local:443" {
			t.Fatalf("expected CONNECT flutter.backend.local:443, got %q", host)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for transparent SNI CONNECT")
	}

	// 3. Synthetic mobile_hook capture + Count()
	before, _ := tr.Count()
	tr.RecordSyntheticMobileExchange("api.hook.local", "POST", "https://api.hook.local/v1/login", 200,
		"POST /v1/login HTTP/1.1\nHost: api.hook.local", `{"u":"a"}`,
		"HTTP/1.1 200 OK\nContent-Type: application/json", `{"token":"xyz"}`, "mobile_hook")
	if after, _ := tr.Count(); after != before+1 {
		t.Fatalf("expected traffic Count increment after RecordSyntheticMobileExchange (%d -> %d)", before, after)
	}
	rows, total, err := tr.Page(PageQuery{Source: "mobile_hook", RespMin: -1, RespMax: -1}, 0, 10)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Host != "api.hook.local" {
		t.Fatalf("unexpected synthetic mobile_hook query result: total=%d rows=%+v err=%v", total, rows, err)
	}
}

func TestMobileProxyStripsInternalHeaderAndNeutralCA(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddr := ln.Addr().String()
	_ = ln.Close()

	tr, err := Open(t.TempDir(), proxyAddr)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer tr.Close()

	// Fresh CA must not contain "mitmproxy" in Subject/Issuer.
	der, err := tr.CACertDER()
	if err != nil {
		t.Fatalf("CACertDER: %v", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if strings.Contains(strings.ToLower(caCert.Subject.String()), "mitmproxy") ||
		strings.Contains(strings.ToLower(caCert.Issuer.String()), "mitmproxy") {
		t.Fatalf("root CA still exposes mitmproxy brand: subj=%q issuer=%q", caCert.Subject.String(), caCert.Issuer.String())
	}

	go func() { _ = tr.Start() }()
	// Wait briefly until proxy listener binds.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, dialErr := net.DialTimeout("tcp", proxyAddr, 50*time.Millisecond)
		if dialErr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Upstream target inspects every header received over the wire.
	var gotHeader string
	gotHit := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Blade-Traffic-Source")
		select {
		case gotHit <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	mp, err := NewMobileProxy(tr, "127.0.0.1:0", []string{"127.0.0.1/32"}, "u1", "secret99")
	if err != nil {
		t.Fatalf("NewMobileProxy: %v", err)
	}
	mpSrv := httptest.NewServer(mp)
	defer mpSrv.Close()

	mpURL, err := url.Parse(mpSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	mpURL.User = url.UserPassword("u1", "secret99")
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(mpURL)},
		Timeout:   5 * time.Second,
	}
	resp, err := client.Get(target.URL + "/api/v1/ping")
	if err != nil {
		t.Fatalf("GET via MobileProxy failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected target status: %d", resp.StatusCode)
	}
	select {
	case <-gotHit:
	case <-time.After(2 * time.Second):
		t.Fatal("target did not receive request")
	}
	if gotHeader != "" {
		t.Fatalf("X-Blade-Traffic-Source leaked upstream over wire: %q", gotHeader)
	}

	// Verify local SQLite still attributed the exchange to mobile AND didn't store the internal header.
	var storedSrc, reqHead string
	rows, total, err := tr.Page(PageQuery{Source: "mobile", RespMin: -1, RespMax: -1}, 0, 10)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("expected 1 mobile-attributed exchange, got total=%d rows=%+v err=%v", total, rows, err)
	}
	storedSrc = rows[0].Source
	if storedSrc != "mobile" {
		t.Fatalf("expected source=mobile, got %q", storedSrc)
	}
	if err := tr.DB().QueryRow(`SELECT req_head FROM exchange_bodies WHERE id=?`, rows[0].ID).Scan(&reqHead); err != nil {
		t.Fatalf("query exchange_bodies: %v", err)
	}
	if strings.Contains(strings.ToLower(reqHead), "x-blade-traffic-source") {
		t.Fatalf("req_head retained stripped internal header:\n%s", reqHead)
	}
}
