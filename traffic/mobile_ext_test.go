package traffic

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
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
