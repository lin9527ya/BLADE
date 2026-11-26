package traffic

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// MobileTransparentBridge accepts raw TCP streams redirected via Android Root
// `iptables REDIRECT` + `adb reverse`, peeks either TLS ClientHello SNI or plain
// HTTP Host headers, and tunnels them cleanly through BLADE's recording MITM
// proxy (marking traffic source="mobile"). This captures Flutter, Cronet, Go/Rust,
// and C++ native sockets that bypass Android's global http_proxy setting.
type MobileTransparentBridge struct {
	traffic    *Traffic
	listenAddr string

	mu         sync.Mutex
	listener   net.Listener
	running    bool
	closed     bool
	acceptDone chan struct{}
	conns      sync.WaitGroup
}

// NewMobileTransparentBridge creates a loopback transparent SNI/HTTP bridge into traffic.
func NewMobileTransparentBridge(tr *Traffic, listenAddr string) (*MobileTransparentBridge, error) {
	if tr == nil {
		return nil, fmt.Errorf("recording traffic proxy must be initialized first")
	}
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	return &MobileTransparentBridge{
		traffic:    tr,
		listenAddr: listenAddr,
	}, nil
}

// Bind acquires the TCP listener synchronously.
func (b *MobileTransparentBridge) Bind() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return fmt.Errorf("transparent bridge closed")
	}
	if b.listener != nil {
		b.mu.Unlock()
		return nil
	}
	addr := b.listenAddr
	b.mu.Unlock()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen transparent bridge on %s: %w", addr, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		_ = ln.Close()
		return fmt.Errorf("transparent bridge closed")
	}
	if b.listener == nil {
		b.listener = ln
		b.listenAddr = ln.Addr().String()
	} else {
		_ = ln.Close()
	}
	return nil
}

// Addr returns the bound listener address.
func (b *MobileTransparentBridge) Addr() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.listenAddr
}

// Port returns the active numeric port.
func (b *MobileTransparentBridge) Port() int {
	addr := b.Addr()
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		var p int
		_, _ = fmt.Sscanf(portStr, "%d", &p)
		return p
	}
	return 0
}

// Running reports whether the transparent bridge accept loop is active.
func (b *MobileTransparentBridge) Running() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

// StartWithReady runs the accept loop until Close is called.
func (b *MobileTransparentBridge) StartWithReady(ready chan<- struct{}) error {
	signalReady := func() {
		if ready != nil {
			close(ready)
			ready = nil
		}
	}
	if err := b.Bind(); err != nil {
		signalReady()
		return err
	}
	b.mu.Lock()
	if b.closed {
		ln := b.listener
		b.listener = nil
		b.mu.Unlock()
		if ln != nil {
			_ = ln.Close()
		}
		signalReady()
		return nil
	}
	ln := b.listener
	done := make(chan struct{})
	b.acceptDone = done
	b.running = true
	listenAddr := b.listenAddr
	b.mu.Unlock()
	signalReady()

	log.Printf("[mobile-transparent] iptables SNI/HTTP bridge listening on %s -> %s", listenAddr, b.traffic.proxyHostPort())
	for {
		c, err := ln.Accept()
		if err != nil {
			b.mu.Lock()
			b.running = false
			b.mu.Unlock()
			close(done)
			return nil
		}
		b.conns.Add(1)
		go func(conn net.Conn) {
			defer b.conns.Done()
			b.handleConn(conn)
		}(c)
	}
}

// Close shuts down the listener and waits for active handlers cleanly.
func (b *MobileTransparentBridge) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	ln := b.listener
	done := b.acceptDone
	b.running = false
	b.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	if done != nil {
		<-done
	}
	b.conns.Wait()
	return err
}

func (b *MobileTransparentBridge) handleConn(clientConn net.Conn) {
	defer clientConn.Close()
	_ = clientConn.SetReadDeadline(time.Now().Add(10 * time.Second))

	br := bufio.NewReaderSize(clientConn, 16384)
	hdr, err := br.Peek(5)
	if err != nil && len(hdr) == 0 {
		return
	}

	// Case 1: TLS ClientHello (0x16 0x03 ...) -> read exact TLS record length (never blocking past record!)
	if len(hdr) >= 5 && hdr[0] == 0x16 && hdr[1] == 0x03 {
		recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
		need := 5 + recLen
		if need > 16384 {
			need = 16384
		}
		if need < 5 {
			need = 5
		}
		peeked, _ := br.Peek(need)
		_ = clientConn.SetReadDeadline(time.Time{})
		sni := ExtractTLSSNI(peeked)
		if sni == "" {
			return
		}
		target := net.JoinHostPort(sni, "443")
		upConn, err := net.DialTimeout("tcp", b.traffic.proxyHostPort(), 10*time.Second)
		if err != nil {
			return
		}
		localAddr := upConn.LocalAddr().String()
		b.traffic.RegisterMobileClientAddr(localAddr)
		defer b.traffic.UnregisterMobileClientAddr(localAddr)
		defer upConn.Close()

		if _, err := fmt.Fprintf(upConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nX-Blade-Traffic-Source: mobile\r\n\r\n", target, target); err != nil {
			return
		}
		ubr := bufio.NewReader(upConn)
		resp, err := http.ReadResponse(ubr, &http.Request{Method: http.MethodConnect})
		if err != nil {
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(upConn, br)
			_ = upConn.Close()
		}()
		go func() {
			defer wg.Done()
			if ubr.Buffered() > 0 {
				_, _ = io.CopyN(clientConn, ubr, int64(ubr.Buffered()))
			}
			_, _ = io.Copy(clientConn, upConn)
			_ = clientConn.Close()
		}()
		wg.Wait()
		return
	}

	// Case 2: Plaintext HTTP -> ReadRequest, forward via BLADE HTTP proxy with mobile source tag.
	_ = clientConn.SetReadDeadline(time.Time{})
	req, err := http.ReadRequest(br)
	if err != nil || req.Host == "" {
		return
	}
	req.RequestURI = ""
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL.Host == "" {
		req.URL.Host = req.Host
	}
	req.Header.Set("X-Blade-Traffic-Source", "mobile")

	pURL, err := url.Parse("http://" + b.traffic.proxyHostPort())
	if err != nil {
		return
	}
	tr := &http.Transport{Proxy: http.ProxyURL(pURL)}
	defer tr.CloseIdleConnections()
	resp, err := tr.RoundTrip(req)
	if err != nil {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(clientConn)
}

// ExtractTLSSNI extracts the TLS server_name extension hostname from raw ClientHello bytes.
func ExtractTLSSNI(rec []byte) string {
	// TLS Record header: 5 bytes [content_type=0x16][ver 2B][length 2B]
	if len(rec) < 9 || rec[0] != 0x16 {
		return ""
	}
	pos := 5
	if pos+4 > len(rec) || rec[pos] != 0x01 { // HandshakeType == ClientHello (1)
		return ""
	}
	pos += 4 // skip handshake header [type 1B][len 3B]
	if pos+2+32+1 > len(rec) {
		return ""
	}
	pos += 2 + 32 // version + random
	sessLen := int(rec[pos])
	pos += 1 + sessLen
	if pos+2 > len(rec) {
		return ""
	}
	csLen := int(binary.BigEndian.Uint16(rec[pos : pos+2]))
	pos += 2 + csLen
	if pos+1 > len(rec) {
		return ""
	}
	compLen := int(rec[pos])
	pos += 1 + compLen
	if pos+2 > len(rec) {
		return ""
	}
	extTotal := int(binary.BigEndian.Uint16(rec[pos : pos+2]))
	pos += 2
	extEnd := pos + extTotal
	if extEnd > len(rec) {
		extEnd = len(rec)
	}
	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(rec[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(rec[pos+2 : pos+4]))
		pos += 4
		if pos+extLen > extEnd {
			break
		}
		if extType == 0 && extLen >= 5 { // server_name extension
			listLen := int(binary.BigEndian.Uint16(rec[pos : pos+2]))
			p := pos + 2
			end := p + listLen
			if end > pos+extLen {
				end = pos + extLen
			}
			for p+3 <= end {
				nameType := rec[p]
				nameLen := int(binary.BigEndian.Uint16(rec[p+1 : p+3]))
				p += 3
				if p+nameLen > end {
					break
				}
				if nameType == 0 && nameLen > 0 { // host_name
					return string(rec[p : p+nameLen])
				}
				p += nameLen
			}
		}
		pos += extLen
	}
	return ""
}
