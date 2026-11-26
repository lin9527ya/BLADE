package traffic

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MobileProxy exposes a secondary authenticated HTTP/CONNECT proxy intended for
// mobile devices on a trusted local network. Unauthenticated proxy requests fail
// closed with 407 Proxy Authentication Required, and client IPs must fall within
// the configured CIDR allowlist. Authenticated flows forward cleanly into BLADE's
// local MITM recorder and are tagged source="mobile".
type MobileProxy struct {
	traffic     *Traffic
	listenAddr  string
	allowCIDRs  []*net.IPNet
	rawCIDRs    []string
	username    string
	password    string
	tokenSecret []byte

	mu        sync.Mutex
	listener  net.Listener
	server    *http.Server
	running   bool
	closed    bool
	serveDone chan struct{}
}

// MobileSetupInfo describes LAN & USB proxy onboarding details plus one-time CA download URLs.
type MobileSetupInfo struct {
	Enabled         bool     `json:"enabled"`
	ListenAddr      string   `json:"listen_addr,omitempty"`
	Username        string   `json:"username,omitempty"`
	Password        string   `json:"password,omitempty"`
	AllowCIDRs      []string `json:"allow_cidrs,omitempty"`
	CAToken         string   `json:"ca_token,omitempty"`
	CAHashAndroid   string   `json:"ca_hash_android,omitempty"`
	USBReverseCmd   string   `json:"usb_reverse_cmd"`
	USBProxySetCmd  string   `json:"usb_proxy_set_cmd"`
	USBProxyClrCmd  string   `json:"usb_proxy_clear_cmd"`
	LocalMITMPort   int      `json:"local_mitm_port"`
	RecommendedURLs []string `json:"recommended_urls,omitempty"`
}

// NewMobileProxy constructs an authenticated LAN mobile proxy forwarding to traffic.
func NewMobileProxy(tr *Traffic, listenAddr string, cidrs []string, username, password string) (*MobileProxy, error) {
	if tr == nil {
		return nil, fmt.Errorf("recording traffic proxy must be initialized before mobile proxy")
	}
	if len(cidrs) == 0 {
		cidrs = DefaultMobileProxyCIDRs()
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid mobile proxy CIDR %q: %w", raw, err)
		}
		nets = append(nets, ipnet)
	}
	if len(nets) == 0 {
		return nil, fmt.Errorf("at least one CIDR allowlist entry required")
	}
	if strings.TrimSpace(username) == "" {
		username = "blade"
	}
	if strings.TrimSpace(password) == "" {
		password = randomHex(10)
	}
	sec := make([]byte, 32)
	_, _ = rand.Read(sec)
	return &MobileProxy{
		traffic:     tr,
		listenAddr:  strings.TrimSpace(listenAddr),
		allowCIDRs:  nets,
		rawCIDRs:    append([]string(nil), cidrs...),
		username:    username,
		password:    password,
		tokenSecret: sec,
	}, nil
}

// DefaultMobileProxyCIDRs returns loopback + RFC1918 private LAN ranges.
func DefaultMobileProxyCIDRs() []string {
	return []string{"127.0.0.1/32", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
}

// Bind acquires the TCP listener synchronously so callers can inspect the resolved
// address and detect port conflicts before serving begins.
func (mp *MobileProxy) Bind() error {
	mp.mu.Lock()
	if mp.closed {
		mp.mu.Unlock()
		return fmt.Errorf("mobile proxy closed")
	}
	if mp.listener != nil {
		mp.mu.Unlock()
		return nil
	}
	addr := mp.listenAddr
	mp.mu.Unlock()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen mobile proxy on %s: %w", addr, err)
	}
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mp.closed {
		_ = ln.Close()
		return fmt.Errorf("mobile proxy closed")
	}
	if mp.listener == nil {
		mp.listener = ln
		mp.listenAddr = ln.Addr().String()
	} else {
		_ = ln.Close()
	}
	return nil
}

// Start binds the TCP listener (if not already bound) and begins serving
// authenticated mobile proxy traffic until Close is called.
func (mp *MobileProxy) Start() error {
	return mp.StartWithReady(nil)
}

// StartWithReady binds if needed, signals ready once the server state is active,
// and blocks serving requests until Close is called.
func (mp *MobileProxy) StartWithReady(ready chan<- struct{}) error {
	signalReady := func() {
		if ready != nil {
			close(ready)
			ready = nil
		}
	}
	if err := mp.Bind(); err != nil {
		signalReady()
		return err
	}
	mp.mu.Lock()
	if mp.closed {
		ln := mp.listener
		mp.listener = nil
		mp.mu.Unlock()
		if ln != nil {
			_ = ln.Close()
		}
		signalReady()
		return nil
	}
	ln := mp.listener
	srv := &http.Server{
		Addr:              ln.Addr().String(),
		Handler:           mp,
		ReadHeaderTimeout: 30 * time.Second,
	}
	done := make(chan struct{})
	mp.server = srv
	mp.serveDone = done
	mp.running = true
	listenAddr := mp.listenAddr
	user := mp.username
	cidrs := strings.Join(mp.rawCIDRs, ",")
	mp.mu.Unlock()
	signalReady()

	log.Printf("[mobile-proxy] authenticated LAN proxy listening on %s (user=%s cidrs=%s)", listenAddr, user, cidrs)
	err := srv.Serve(ln)
	mp.mu.Lock()
	mp.running = false
	mp.mu.Unlock()
	close(done)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Close shuts down the mobile proxy listener cleanly and waits for Serve to exit.
func (mp *MobileProxy) Close() error {
	if mp == nil {
		return nil
	}
	mp.mu.Lock()
	mp.closed = true
	srv := mp.server
	ln := mp.listener
	done := mp.serveDone
	mp.running = false
	mp.mu.Unlock()
	var err error
	if srv != nil {
		err = srv.Close()
	} else if ln != nil {
		err = ln.Close()
	}
	if done != nil {
		<-done
	}
	return err
}

// Addr returns the active listen address (or configured address before Start).
func (mp *MobileProxy) Addr() string {
	if mp == nil {
		return ""
	}
	mp.mu.Lock()
	defer mp.mu.Unlock()
	return mp.listenAddr
}

// Running reports whether the LAN mobile proxy is currently serving.
func (mp *MobileProxy) Running() bool {
	if mp == nil {
		return false
	}
	mp.mu.Lock()
	defer mp.mu.Unlock()
	return mp.running
}

// Credentials returns the dynamic username/password and allowed CIDRs.
func (mp *MobileProxy) Credentials() (user, pass string, cidrs []string) {
	if mp == nil {
		return "", "", nil
	}
	return mp.username, mp.password, append([]string(nil), mp.rawCIDRs...)
}

// MintCAToken issues an HMAC-signed short-lived token (TTL 30m) for passwordless QR CA download.
func (mp *MobileProxy) MintCAToken(ttl time.Duration) string {
	if mp == nil {
		return ""
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	mac := hmac.New(sha256.New, mp.tokenSecret)
	_, _ = mac.Write([]byte(exp))
	sig := hex.EncodeToString(mac.Sum(nil))
	return exp + "." + sig
}

func (mp *MobileProxy) verifyCAToken(tok string) bool {
	parts := strings.SplitN(strings.TrimSpace(tok), ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, mp.tokenSecret)
	_, _ = mac.Write([]byte(parts[0]))
	expected := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) == 1
}

func (mp *MobileProxy) ipAllowed(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	for _, n := range mp.allowCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (mp *MobileProxy) authorizeProxyHeader(r *http.Request) bool {
	hdr := strings.TrimSpace(r.Header.Get("Proxy-Authorization"))
	if !strings.HasPrefix(hdr, "Basic ") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(hdr, "Basic ")))
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(mp.username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(mp.password)) == 1
	return userOK && passOK
}

func (mp *MobileProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !mp.ipAllowed(r.RemoteAddr) {
		http.Error(w, "forbidden source IP", http.StatusForbidden)
		return
	}
	// Direct HTTP portal request for CA install before proxy is configured on the handset.
	if r.Method == http.MethodGet && (r.URL.Path == "/ca" || strings.HasPrefix(r.URL.Path, "/cert/")) && !r.URL.IsAbs() {
		if !mp.verifyCAToken(r.URL.Query().Get("token")) {
			http.Error(w, "invalid or expired CA token", http.StatusForbidden)
			return
		}
		mp.traffic.ServeCertPortal(w, r)
		return
	}

	if !mp.authorizeProxyHeader(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="BLADE Mobile Proxy"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}

	if r.Method == http.MethodConnect {
		mp.handleConnect(w, r)
		return
	}
	mp.handleHTTPForward(w, r)
}

func (mp *MobileProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	upConn, err := net.DialTimeout("tcp", mp.traffic.proxyHostPort(), 10*time.Second)
	if err != nil {
		http.Error(w, "upstream MITM dial failed", http.StatusBadGateway)
		return
	}
	localAddr := upConn.LocalAddr().String()
	mp.traffic.RegisterMobileClientAddr(localAddr)
	defer mp.traffic.UnregisterMobileClientAddr(localAddr)
	defer upConn.Close()

	// Forward CONNECT handshake to local go-mitmproxy (unauthenticated on loopback).
	if _, err := fmt.Fprintf(upConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", r.Host, r.Host); err != nil {
		http.Error(w, "upstream CONNECT write failed", http.StatusBadGateway)
		return
	}
	br := bufio.NewReader(upConn)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		http.Error(w, "upstream CONNECT failed", http.StatusBadGateway)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream MITM rejected CONNECT", resp.StatusCode)
		return
	}

	clientConn, clientRW, err := hj.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if br.Buffered() > 0 {
			_, _ = io.CopyN(clientConn, br, int64(br.Buffered()))
		}
		_, _ = io.Copy(clientConn, upConn)
		if tc, ok := clientConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upConn, clientRW)
		if tc, ok := upConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
}

func (mp *MobileProxy) handleHTTPForward(w http.ResponseWriter, r *http.Request) {
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.Header.Del("Proxy-Authorization")
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Set("X-Blade-Traffic-Source", "mobile")

	proxyDial := func(req *http.Request) (*http.Response, error) {
		pURL, _ := url.Parse("http://" + mp.traffic.proxyHostPort())
		tr := &http.Transport{
			Proxy: http.ProxyURL(pURL),
		}
		defer tr.CloseIdleConnections()
		return tr.RoundTrip(req)
	}
	resp, err := proxyDial(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func randomHex(bytesLen int) string {
	b := make([]byte, bytesLen)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
