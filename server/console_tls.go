package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type consoleTLS struct {
	mu      sync.Mutex
	dir     string
	serving bool
}

// The private bundle is always stored outside the browsable data workspace.
type consoleBundle struct {
	Mode             string
	Host             string
	Certificate, Key string
	CA, CAKey        string
}
type consoleCertInfo struct {
	Mode        string    `json:"mode"`
	Host        string    `json:"host"`
	SAN         []string  `json:"san"`
	Expires     time.Time `json:"expires"`
	Fingerprint string    `json:"fingerprint"`
	CA          string    `json:"ca,omitempty"`
}

func (c *consoleTLS) read(name string) (*consoleBundle, error) {
	b, err := os.ReadFile(filepath.Join(c.dir, name+".json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cert consoleBundle
	if err = json.Unmarshal(b, &cert); err != nil {
		return nil, err
	}
	return &cert, nil
}
func (c *consoleTLS) save(name string, cert *consoleBundle) error {
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.dir, ".cert-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(cert)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(c.dir, name+".json"))
}
func validConsoleHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsUnspecified()
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func (b *consoleBundle) inspect() (tls.Certificate, consoleCertInfo, error) {
	if !validConsoleHost(b.Host) {
		return tls.Certificate{}, consoleCertInfo{}, errors.New("请填写浏览器使用的域名或 IP")
	}
	pair, err := tls.X509KeyPair([]byte(b.Certificate), []byte(b.Key))
	if err != nil {
		return pair, consoleCertInfo{}, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return pair, consoleCertInfo{}, err
	}
	sans := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		sans = append(sans, ip.String())
	}
	sum := sha256.Sum256(leaf.Raw)
	info := consoleCertInfo{Mode: b.Mode, Host: b.Host, SAN: sans, Expires: leaf.NotAfter, Fingerprint: hex.EncodeToString(sum[:]), CA: b.CA}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return pair, info, errors.New("证书不在有效期内")
	}
	for i := 1; i < len(pair.Certificate); i++ {
		parent, e := x509.ParseCertificate(pair.Certificate[i])
		if e != nil {
			return pair, info, e
		}
		child, e := x509.ParseCertificate(pair.Certificate[i-1])
		if e != nil {
			return pair, info, e
		}
		if now.Before(parent.NotBefore) || !now.Before(parent.NotAfter) || child.CheckSignatureFrom(parent) != nil {
			return pair, info, errors.New("证书链无效或已过期")
		}
	}
	if err = leaf.VerifyHostname(b.Host); err != nil {
		return pair, info, errors.New("证书不匹配访问地址")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		serverAuth := false
		for _, u := range leaf.ExtKeyUsage {
			if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
				serverAuth = true
			}
		}
		if !serverAuth {
			return pair, info, errors.New("证书不支持服务器认证")
		}
	}
	return pair, info, nil
}

// StageConsolePEM imports files locally without sending a private key over HTTP.
func StageConsolePEM(root, host, certPath, keyPath string) error {
	read := func(path string) (string, error) {
		f, e := os.Open(path)
		if e != nil {
			return "", e
		}
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if len(b) > 1<<20 {
			return "", errors.New("PEM 文件超过 1 MiB")
		}
		return string(b), e
	}
	cert, e := read(certPath)
	if e != nil {
		return e
	}
	key, e := read(keyPath)
	if e != nil {
		return e
	}
	b := &consoleBundle{Mode: "import", Host: host, Certificate: cert, Key: key}
	if _, _, e = b.inspect(); e != nil {
		return e
	}
	c := consoleTLS{dir: filepath.Join(root, ".blade-tls")}
	return c.save("candidate", b)
}
func consoleSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
func generateConsoleBundle(host string, previous *consoleBundle) (*consoleBundle, error) {
	var ca *x509.Certificate
	var signer *ecdsa.PrivateKey
	b := &consoleBundle{Mode: "selfsigned", Host: host}
	now := time.Now()
	if previous != nil && previous.Mode == "selfsigned" {
		cr, _ := pem.Decode([]byte(previous.CA))
		key, _ := pem.Decode([]byte(previous.CAKey))
		if cr == nil || key == nil {
			return nil, errors.New("本地 CA 文件损坏")
		}
		var err error
		ca, err = x509.ParseCertificate(cr.Bytes)
		if err != nil {
			return nil, err
		}
		signer, err = x509.ParseECPrivateKey(key.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := ca.PublicKey.(*ecdsa.PublicKey)
		if !ok || !pub.Equal(&signer.PublicKey) {
			return nil, errors.New("CA 密钥不匹配")
		}
		if !now.Before(ca.NotAfter) {
			return generateConsoleBundle(host, nil)
		}
		b.CA, b.CAKey = previous.CA, previous.CAKey
	} else {
		var err error
		signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		serial, err := consoleSerial()
		if err != nil {
			return nil, err
		}
		t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "BLADE Console Local CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		raw, err := x509.CreateCertificate(rand.Reader, t, t, &signer.PublicKey, signer)
		if err != nil {
			return nil, err
		}
		ca, err = x509.ParseCertificate(raw)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(signer)
		if err != nil {
			return nil, err
		}
		b.CA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
		b.CAKey = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := consoleSerial()
	if err != nil {
		return nil, err
	}
	until := now.AddDate(1, 0, 0)
	if ca.NotAfter.Before(until) {
		until = ca.NotAfter
	}
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-5 * time.Minute), NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = []string{host}
	}
	raw, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, signer)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	b.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
	b.Key = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	return b, nil
}
func (s *Server) ConsoleTLSConfig(forceHTTP bool) (*tls.Config, error) {
	c := &s.consoleTLS
	c.mu.Lock()
	defer c.mu.Unlock()
	if forceHTTP {
		c.serving = false
		return nil, nil
	}
	b, err := c.read("active")
	if err != nil || b == nil {
		return nil, err
	}
	pair, _, err := b.inspect()
	if err != nil {
		return nil, err
	}
	c.serving = true
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}
func (s *Server) consoleTLSAPI(w http.ResponseWriter, r *http.Request) {
	c := &s.consoleTLS
	c.mu.Lock()
	defer c.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		out := map[string]any{"serving": c.serving}
		for _, name := range []string{"active", "candidate"} {
			b, err := c.read(name)
			if err != nil {
				writeErr(w, 500, "读取证书配置失败")
				return
			}
			if b != nil {
				_, info, err := b.inspect()
				out[name] = info
				if err != nil {
					out[name+"_error"] = err.Error()
				}
			}
		}
		writeJSON(w, 200, out)
		return
	}
	var req struct{ Action, Host, Certificate, Key, Fingerprint string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 3<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "证书请求无效")
		return
	}
	var err error
	switch req.Action {
	case "import", "generate":
		host := strings.TrimSpace(req.Host)
		if !validConsoleHost(host) {
			writeErr(w, 400, "请填写浏览器使用的域名或 IP")
			return
		}
		b := &consoleBundle{Mode: "import", Host: host, Certificate: req.Certificate, Key: req.Key}
		if req.Action == "import" {
			peer, _, _ := net.SplitHostPort(r.RemoteAddr)
			hostname := r.Host
			if h, _, e := net.SplitHostPort(hostname); e == nil {
				hostname = h
			}
			local := net.ParseIP(peer).IsLoopback() && (net.ParseIP(strings.Trim(hostname, "[]")).IsLoopback() || strings.EqualFold(hostname, "localhost"))
			forwarded := r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != ""
			if r.TLS == nil && (!local || forwarded) {
				writeErr(w, 403, "请通过本机回环地址或 HTTPS 导入私钥")
				return
			}
		} else {
			previous, e := c.read("active")
			if e != nil {
				writeErr(w, 500, "读取活动证书失败")
				return
			}
			if previous == nil {
				previous, e = c.read("candidate")
				if e != nil {
					writeErr(w, 500, "读取候选证书失败")
					return
				}
			}
			b, err = generateConsoleBundle(host, previous)
		}
		if err == nil {
			_, _, err = b.inspect()
		}
		if err == nil {
			err = c.save("candidate", b)
		}
	case "enable":
		b, e := c.read("candidate")
		if e != nil || b == nil {
			writeErr(w, 409, "请先准备证书")
			return
		}
		_, info, e := b.inspect()
		if e != nil {
			writeErr(w, 400, e.Error())
			return
		}
		if req.Fingerprint != info.Fingerprint {
			writeErr(w, 409, "候选证书已变化，请刷新")
			return
		}
		err = c.save("active", b)
	case "disable":
		err = os.Remove(filepath.Join(c.dir, "active.json"))
		if os.IsNotExist(err) {
			err = nil
		}
	default:
		writeErr(w, 400, "未知的证书操作")
		return
	}
	if err != nil {
		writeErr(w, 400, fmt.Sprintf("证书配置失败：%s", err))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
