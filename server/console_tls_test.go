package server

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConsoleTLSRoundTrip(t *testing.T) {
	s := &Server{consoleTLS: consoleTLS{dir: t.TempDir()}}
	b, err := generateConsoleBundle("192.168.1.14", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, info, err := b.inspect()
	if err != nil || info.Fingerprint == "" {
		t.Fatalf("%+v %v", info, err)
	}
	b.Host = "localhost"
	if _, _, err = b.inspect(); err == nil {
		t.Fatal("accepted wrong SAN")
	}
	b.Host = "192.168.1.14"
	if err = s.consoleTLS.save("active", b); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.ConsoleTLSConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	ts.TLS = cfg
	ts.StartTLS()
	defer ts.Close()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(b.CA))
	client := &http.Client{Timeout: time.Second * 3, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: b.Host, MinVersion: tls.VersionTLS12}}}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	candidate, err := generateConsoleBundle("localhost", b)
	if err != nil || candidate.CA != b.CA {
		t.Fatalf("CA renewal: %v", err)
	}
}

func TestConsoleTLSFailedImportPreservesActive(t *testing.T) {
	s := &Server{consoleTLS: consoleTLS{dir: t.TempDir()}}
	good, err := generateConsoleBundle("localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.consoleTLS.save("active", good); err != nil {
		t.Fatal(err)
	}
	another, err := generateConsoleBundle("localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		peer, host, cert, key string
		code                  int
	}{
		{"192.0.2.9:12345", "localhost", good.Certificate, good.Key, 403},
		{"127.0.0.1:2345", "wrong.example", good.Certificate, good.Key, 400},
		{"127.0.0.1:2345", "localhost", good.Certificate, another.Key, 400},
	} {
		body, _ := json.Marshal(map[string]string{"action": "import", "host": tc.host, "certificate": tc.cert, "key": tc.key})
		r := httptest.NewRequest("POST", "http://localhost/api/system/tls", bytes.NewReader(body))
		r.RemoteAddr = tc.peer
		w := httptest.NewRecorder()
		s.consoleTLSAPI(w, r)
		if w.Code != tc.code || strings.Contains(w.Body.String(), "PRIVATE KEY") {
			t.Fatalf("response %d", w.Code)
		}
	}
	b, err := s.consoleTLS.read("active")
	if err != nil || b.Certificate != good.Certificate {
		t.Fatal("lost saved certificate")
	}
}

func TestConsoleTLSExpiredLeaf(t *testing.T) {
	b, err := generateConsoleBundle("localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := pem.Decode([]byte(b.CA))
	keyPEM, _ := pem.Decode([]byte(b.CAKey))
	leafKeyPEM, _ := pem.Decode([]byte(b.Key))
	ca, err := x509.ParseCertificate(caPEM.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := x509.ParseECPrivateKey(keyPEM.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := x509.ParseECPrivateKey(leafKeyPEM.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(123), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &leafKey.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	b.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
	if _, _, err = b.inspect(); err == nil {
		t.Fatal("expired leaf accepted")
	}
}
