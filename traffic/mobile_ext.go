package traffic

import (
	"crypto/md5"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

// PinningAlert records an intercepted TLS connection failure indicative of SSL/TLS certificate pinning.
type PinningAlert struct {
	Host          string `json:"host"`
	Reason        string `json:"reason"`
	Source        string `json:"source"` // mobile | web
	Count         int    `json:"count"`
	FirstSeenUnix int64  `json:"first_seen_unix"`
	LastSeenUnix  int64  `json:"last_seen_unix"`
}

var mobileNoiseHostSuffixes = []string{
	"umeng.com",
	"umengcloud.com",
	"bugly.qq.com",
	"sensorsdata.cn",
	"growingio.com",
	"getui.com",
	"jpush.cn",
	"jiguang.cn",
	"crashlytics.com",
	"app-measurement.com",
	"firebaseinstallations.googleapis.com",
	"firebaselogging-pa.googleapis.com",
	"connectivitycheck.gstatic.com",
	"clients3.google.com",
	"clients4.google.com",
	"safebrowsing.googleapis.com",
	"sentry.io",
	"appsflyer.com",
	"adjust.com",
	"doubleclick.net",
	"push.apple.com",
	"configuration.apple.com",
	"mesu.apple.com",
	"init.ess.apple.com",
	"metrics.icloud.com",
}

// IsNoiseHost reports whether host is a well-known mobile OS telemetry/analytics/push host.
func IsNoiseHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(hostOnly(host)))
	if h == "" {
		return false
	}
	for _, suf := range mobileNoiseHostSuffixes {
		if h == suf || strings.HasSuffix(h, "."+suf) {
			return true
		}
	}
	return false
}

// CACertPEM returns the public PEM CA certificate bytes.
func (t *Traffic) CACertPEM() ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("traffic proxy disabled")
	}
	return ioReadFile(t.CACertPath())
}

// CACertDER returns the public DER-encoded CA certificate bytes (.crt / .cer).
func (t *Traffic) CACertDER() ([]byte, error) {
	pemBytes, err := t.CACertPEM()
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, fmt.Errorf("invalid CA PEM")
	}
	return blk.Bytes, nil
}

// CASubjectHash returns the legacy OpenSSL X509_NAME_hash (<8 hex chars>) used by Android system certs.
func (t *Traffic) CASubjectHash() (string, error) {
	der, err := t.CACertDER()
	if err != nil {
		return "", err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", err
	}
	sum := md5.Sum(cert.RawSubject)
	return fmt.Sprintf("%08x", binary.LittleEndian.Uint32(sum[:4])), nil
}

// AppleMobileConfig renders an iOS/iPadOS .mobileconfig profile installing BLADE's root CA.
func (t *Traffic) AppleMobileConfig(orgName string) ([]byte, error) {
	der, err := t.CACertDER()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(orgName) == "" {
		orgName = "BLADE Security Assessment"
	}
	b64 := base64.StdEncoding.EncodeToString(der)
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadCertificateFileName</key>
			<string>blade-mitm-ca.cer</string>
			<key>PayloadContent</key>
			<data>%s</data>
			<key>PayloadDescription</key>
			<string>BLADE MITM Root CA for authorized mobile security testing</string>
			<key>PayloadDisplayName</key>
			<string>%s Root CA</string>
			<key>PayloadIdentifier</key>
			<string>com.blade.mitm.ca</string>
			<key>PayloadType</key>
			<string>com.apple.security.root</string>
			<key>PayloadUUID</key>
			<string>7E61F07A-9C35-4E42-9328-262044A4D501</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>PayloadDisplayName</key>
	<string>BLADE MITM CA Profile</string>
	<key>PayloadIdentifier</key>
	<string>com.blade.mitm.profile</string>
	<key>PayloadRemovalDisallowed</key>
	<false/>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>7E61F07A-9C35-4E42-9328-262044A4D502</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
</dict>
</plist>
`, b64, html.EscapeString(orgName))
	return []byte(xml), nil
}

// RegisterMobileClientAddr tags an active local socket address as originating from the LAN mobile proxy.
func (t *Traffic) RegisterMobileClientAddr(remoteAddr string) {
	if t == nil || remoteAddr == "" {
		return
	}
	t.mobileAddrMu.Lock()
	if t.mobileAddrs == nil {
		t.mobileAddrs = make(map[string]struct{})
	}
	t.mobileAddrs[remoteAddr] = struct{}{}
	t.mobileAddrMu.Unlock()
}

// UnregisterMobileClientAddr removes a finished LAN mobile proxy connection tag.
func (t *Traffic) UnregisterMobileClientAddr(remoteAddr string) {
	if t == nil || remoteAddr == "" {
		return
	}
	t.mobileAddrMu.Lock()
	delete(t.mobileAddrs, remoteAddr)
	t.mobileAddrMu.Unlock()
}

func (t *Traffic) flowSource(f *mproxy.Flow) string {
	if f != nil && f.Request != nil && strings.EqualFold(f.Request.Header.Get("X-Blade-Traffic-Source"), "mobile") {
		return "mobile"
	}
	if f != nil && f.ConnContext != nil && f.ConnContext.ClientConn != nil && f.ConnContext.ClientConn.Conn != nil {
		addr := f.ConnContext.ClientConn.Conn.RemoteAddr().String()
		t.mobileAddrMu.RLock()
		_, ok := t.mobileAddrs[addr]
		t.mobileAddrMu.RUnlock()
		if ok {
			return "mobile"
		}
	}
	return "web"
}

// RecordPinningAlert records a potential certificate-pinning rejection for UI/agent telemetry.
func (t *Traffic) RecordPinningAlert(host, reason, source string) {
	host = strings.ToLower(strings.TrimSpace(hostOnly(host)))
	if t == nil || host == "" {
		return
	}
	now := time.Now().Unix()
	t.pinningMu.Lock()
	defer t.pinningMu.Unlock()
	if t.pinningAlerts == nil {
		t.pinningAlerts = make(map[string]*PinningAlert)
	}
	a, ok := t.pinningAlerts[host]
	if !ok {
		if len(t.pinningAlerts) >= 200 {
			// Evict oldest entry.
			var oldK string
			var oldTS int64
			for k, v := range t.pinningAlerts {
				if oldK == "" || v.LastSeenUnix < oldTS {
					oldK, oldTS = k, v.LastSeenUnix
				}
			}
			delete(t.pinningAlerts, oldK)
		}
		a = &PinningAlert{Host: host, Reason: reason, Source: source, FirstSeenUnix: now}
		t.pinningAlerts[host] = a
	}
	a.Count++
	a.LastSeenUnix = now
	if reason != "" {
		a.Reason = reason
	}
}

// PinningAlerts lists detected TLS pinning rejections sorted newest first.
func (t *Traffic) PinningAlerts() []PinningAlert {
	if t == nil {
		return nil
	}
	t.pinningMu.Lock()
	defer t.pinningMu.Unlock()
	out := make([]PinningAlert, 0, len(t.pinningAlerts))
	for _, a := range t.pinningAlerts {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenUnix > out[j].LastSeenUnix })
	return out
}

// ClearPinningAlerts clears recorded pinning failure alerts.
func (t *Traffic) ClearPinningAlerts() {
	if t == nil {
		return
	}
	t.pinningMu.Lock()
	t.pinningAlerts = make(map[string]*PinningAlert)
	t.pinningMu.Unlock()
}

func isLikelyPinningErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, pat := range []string{"bad certificate", "unknown ca", "certificate unknown", "certificate_unknown", "unknown_ca", "tls:", "handshake", "remote error"} {
		if strings.Contains(s, pat) {
			return true
		}
	}
	return false
}

// ServeCertPortal serves the public CA certificate & mobile onboarding portal (http://mitm.it or /ca?token=...).
func (t *Traffic) ServeCertPortal(w http.ResponseWriter, r *http.Request) {
	switch strings.ToLower(strings.TrimSpace(r.URL.Path)) {
	case "/cert/pem", "/blade-ca.pem":
		data, err := t.CACertPEM()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.pem"`)
		_, _ = w.Write(data)
		return
	case "/cert/crt", "/blade-ca.crt", "/cert/cer":
		data, err := t.CACertDER()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.crt"`)
		_, _ = w.Write(data)
		return
	case "/cert/mobileconfig":
		data, err := t.AppleMobileConfig("BLADE")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-apple-aspen-config")
		w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.mobileconfig"`)
		_, _ = w.Write(data)
		return
	}
	// Format query parameter (used by /ca?token=...&fmt=...)
	fmtParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("fmt")))
	switch fmtParam {
	case "pem":
		data, err := t.CACertPEM()
		if err == nil {
			w.Header().Set("Content-Type", "application/x-pem-file")
			w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.pem"`)
			_, _ = w.Write(data)
			return
		}
	case "crt", "der":
		data, err := t.CACertDER()
		if err == nil {
			w.Header().Set("Content-Type", "application/x-x509-ca-cert")
			w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.crt"`)
			_, _ = w.Write(data)
			return
		}
	case "android", "0":
		hash, err := t.CASubjectHash()
		data, pErr := t.CACertPEM()
		if err == nil && pErr == nil {
			w.Header().Set("Content-Type", "application/x-pem-file")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.0"`, hash))
			_, _ = w.Write(data)
			return
		}
	case "mobileconfig", "ios":
		data, err := t.AppleMobileConfig("BLADE")
		if err == nil {
			w.Header().Set("Content-Type", "application/x-apple-aspen-config")
			w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.mobileconfig"`)
			_, _ = w.Write(data)
			return
		}
	}

	hash, _ := t.CASubjectHash()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>BLADE MITM CA Certificate</title>
<style>body{font-family:system-ui,sans-serif;max-width:560px;margin:2rem auto;padding:0 1rem;line-height:1.5}
.btn{display:block;padding:.8rem 1rem;margin:.6rem 0;border-radius:8px;background:#111827;color:#fff;text-decoration:none;text-align:center;font-weight:600}
small{color:#6b7280}</style></head><body>
<h2>BLADE Mobile CA Portal</h2>
<p>Install this Root CA on authorized test devices to inspect HTTPS API traffic.</p>
<a class="btn" href="?fmt=crt">Download Android / Generic CA (.crt)</a>
<a class="btn" href="?fmt=mobileconfig">Download iOS Profile (.mobileconfig)</a>
<a class="btn" href="?fmt=pem">Download PEM (.pem)</a>
<p><small>Rooted Android system cert filename: <code>%s.0</code><br>iOS: Install profile -> Settings -> General -> About -> Certificate Trust Settings -> Enable Full Trust.</small></p>
</body></html>`, html.EscapeString(hash)))
}

func (s *sink) AccessProxyServer(req *http.Request, res http.ResponseWriter) {
	s.t.ServeCertPortal(res, req)
}

func (s *sink) HTTPConnectError(f *mproxy.Flow, err error) {
	if f == nil || f.Request == nil || err == nil {
		return
	}
	host := ""
	if f.Request.URL != nil {
		host = f.Request.URL.Hostname()
	}
	if host == "" {
		host = f.Request.Raw().Host
	}
	if isLikelyPinningErr(err) {
		s.t.RecordPinningAlert(host, err.Error(), s.t.flowSource(f))
	}
}

func (s *sink) WebSocketEnd(f *mproxy.Flow) {
	if f == nil || f.Request == nil || f.WebScoket == nil || len(f.WebScoket.Messages) == 0 {
		return
	}
	s.t.recordWebSocket(f)
}

func (s *sink) SSEEnd(f *mproxy.Flow) {
	if f == nil || f.Request == nil || f.SSE == nil || len(f.SSE.Events) == 0 {
		return
	}
	s.t.recordSSE(f)
}

func (t *Traffic) recordWebSocket(f *mproxy.Flow) {
	host := f.Request.URL.Hostname()
	urlStr := f.Request.URL.String()
	tmpl := db.TemplatePath(f.Request.URL.EscapedPath())
	var upBuf, downBuf strings.Builder
	for i, msg := range f.WebScoket.Messages {
		if i >= 120 {
			fmt.Fprintf(&downBuf, "... (%d total frames)\n", len(f.WebScoket.Messages))
			break
		}
		target := &downBuf
		dir := "S->C"
		if msg.FromClient {
			target = &upBuf
			dir = "C->S"
		}
		bodyText := formatWSFramePayload(msg.Content)
		fmt.Fprintf(target, "[%s #%d type=%d len=%d] %s\n", dir, i+1, msg.Type, len(msg.Content), bodyText)
	}
	t.recordSyntheticExchange(host, "WS", tmpl, urlStr, 101, "application/x-websocket", t.flowSource(f),
		fmt.Sprintf("WS %s\n%s", f.Request.URL.RequestURI(), requestHeaderLines(f.Request)),
		[]byte(upBuf.String()),
		"HTTP 101 Switching Protocols\nUpgrade: websocket",
		[]byte(downBuf.String()),
	)
}

func (t *Traffic) recordSSE(f *mproxy.Flow) {
	host := f.Request.URL.Hostname()
	urlStr := f.Request.URL.String()
	tmpl := db.TemplatePath(f.Request.URL.EscapedPath())
	var evBuf strings.Builder
	for i, ev := range f.SSE.Events {
		if i >= 200 {
			fmt.Fprintf(&evBuf, "... (%d total SSE events)\n", len(f.SSE.Events))
			break
		}
		fmt.Fprintf(&evBuf, "[event=%s id=%s] %s\n", ev.Event, ev.ID, ev.Data)
	}
	status := 200
	if f.Response != nil && f.Response.StatusCode > 0 {
		status = f.Response.StatusCode
	}
	t.recordSyntheticExchange(host, "SSE", tmpl, urlStr, status, "text/event-stream", t.flowSource(f),
		fmt.Sprintf("%s %s\n%s", f.Request.Method, f.Request.URL.RequestURI(), requestHeaderLines(f.Request)),
		f.Request.Body,
		fmt.Sprintf("HTTP %d\nContent-Type: text/event-stream", status),
		[]byte(evBuf.String()),
	)
}

func (t *Traffic) recordSyntheticExchange(host, method, tmpl, urlStr string, status int, ct, src, reqHead string, reqBody []byte, respHead string, respBody []byte) {
	n := t.seq.Add(1)
	now := time.Now()
	id := newExchangeID(now, n)
	reqB := t.spill(reqBody, ct)
	respB := t.spill(respBody, ct)
	if reqB.hash != "" {
		defer t.retainInflightBlob(reqB.hash, -1)
	}
	if respB.hash != "" {
		defer t.retainInflightBlob(respB.hash, -1)
	}
	noise := 0
	if IsNoiseHost(host) {
		noise = 1
	}

	t.wmu.Lock()
	defer t.wmu.Unlock()
	tx, err := t.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,req_state,resp_state,req_stored_len,resp_stored_len,source,is_noise,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'')`,
		id, now.Unix(), host, method, tmpl, urlStr, status, ct,
		len(reqBody), len(respBody), reqB.state, respB.state, reqB.storedLen, respB.storedLen, src, noise)
	if err != nil {
		return
	}
	rowid, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,req_blob,resp_head,resp_body,resp_blob) VALUES(?,?,?,?,?,?,?)`,
		id, reqHead, reqB.inline, nullIfEmpty(reqB.hash), respHead, respB.inline, nullIfEmpty(respB.hash)); err != nil {
		return
	}
	if t.fts && rowid > 0 {
		idx := strings.Join([]string{urlStr, reqHead, reqB.index, respHead, respB.index}, "\n")
		_, _ = tx.Exec(`INSERT INTO ex_fts(rowid,content) VALUES(?,?)`, rowid, idx)
	}
	_ = tx.Commit()
}

func formatWSFramePayload(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) && !isBinaryBody("", b) {
		return clip(string(b), 512)
	}
	if preview, ok := DecodeProtobufPreview(b); ok {
		return preview
	}
	return "hex:" + hex.EncodeToString(clipBytes(b, 128))
}

// DecodeProtobufPreview performs schema-less Protobuf wire-format inspection.
func DecodeProtobufPreview(raw []byte) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	// Trim optional 5-byte gRPC frame header if present.
	if len(raw) > 5 && (raw[0] == 0 || raw[0] == 1) {
		gLen := binary.BigEndian.Uint32(raw[1:5])
		if int(gLen) == len(raw)-5 {
			raw = raw[5:]
		}
	}
	var b strings.Builder
	fields, ok := decodeProtoMessage(raw, 0, &b)
	if !ok || fields == 0 {
		return "", false
	}
	return clip(strings.TrimSpace(b.String()), 4096), true
}

func decodeProtoMessage(buf []byte, depth int, out *strings.Builder) (int, bool) {
	if depth > 4 || len(buf) == 0 {
		return 0, false
	}
	pos := 0
	fields := 0
	indent := strings.Repeat("  ", depth)
	for pos < len(buf) {
		key, n := binary.Uvarint(buf[pos:])
		if n <= 0 || key == 0 {
			return fields, false
		}
		pos += n
		fieldNum := key >> 3
		wireType := key & 0x07
		if fieldNum == 0 || fieldNum > 536870911 {
			return fields, false
		}
		switch wireType {
		case 0: // Varint
			val, vn := binary.Uvarint(buf[pos:])
			if vn <= 0 {
				return fields, false
			}
			pos += vn
			fmt.Fprintf(out, "%sf%d (varint): %d\n", indent, fieldNum, val)
		case 1: // 64-bit
			if pos+8 > len(buf) {
				return fields, false
			}
			v := binary.LittleEndian.Uint64(buf[pos : pos+8])
			pos += 8
			fmt.Fprintf(out, "%sf%d (fixed64): 0x%x\n", indent, fieldNum, v)
		case 2: // Length-delimited
			ln, lvn := binary.Uvarint(buf[pos:])
			if lvn <= 0 || int(ln) < 0 || pos+lvn+int(ln) > len(buf) {
				return fields, false
			}
			pos += lvn
			sub := buf[pos : pos+int(ln)]
			pos += int(ln)
			if len(sub) > 0 && depth < 3 {
				var nested strings.Builder
				if nf, ok := decodeProtoMessage(sub, depth+1, &nested); ok && nf >= 1 && !looksLikeReadableText(sub) {
					fmt.Fprintf(out, "%sf%d (msg):\n%s", indent, fieldNum, nested.String())
					fields++
					continue
				}
			}
			if looksLikeReadableText(sub) {
				fmt.Fprintf(out, "%sf%d (str): %q\n", indent, fieldNum, clip(string(sub), 200))
			} else {
				fmt.Fprintf(out, "%sf%d (bytes[%d]): %s\n", indent, fieldNum, len(sub), hex.EncodeToString(clipBytes(sub, 48)))
			}
		case 5: // 32-bit
			if pos+4 > len(buf) {
				return fields, false
			}
			v := binary.LittleEndian.Uint32(buf[pos : pos+4])
			pos += 4
			fmt.Fprintf(out, "%sf%d (fixed32): 0x%x\n", indent, fieldNum, v)
		default:
			return fields, false
		}
		fields++
		if out.Len() > 6000 {
			break
		}
	}
	return fields, fields > 0
}

func looksLikeReadableText(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// ProtobufPreviewForExchange inspects stored raw bytes of an exchange for schema-less Protobuf wire data.
func (t *Traffic) ProtobufPreviewForExchange(id string) string {
	if t == nil || t.db == nil || strings.TrimSpace(id) == "" {
		return ""
	}
	var ct string
	var reqBody, respBody []byte
	err := t.db.QueryRow(`
SELECT COALESCE(e.content_type,''), b.req_body, b.resp_body
FROM exchanges e JOIN exchange_bodies b ON e.id=b.id WHERE e.id=?`, id).Scan(&ct, &reqBody, &respBody)
	if err != nil {
		return ""
	}
	isProtoCT := strings.Contains(strings.ToLower(ct), "protobuf") || strings.Contains(strings.ToLower(ct), "grpc")
	var parts []string
	if len(reqBody) > 0 && (isProtoCT || isBinaryBody("", reqBody)) {
		if p, ok := DecodeProtobufPreview(reqBody); ok {
			parts = append(parts, "[Request Protobuf]\n"+p)
		}
	}
	if len(respBody) > 0 && (isProtoCT || isBinaryBody("", respBody)) {
		if p, ok := DecodeProtobufPreview(respBody); ok {
			parts = append(parts, "[Response Protobuf]\n"+p)
		}
	}
	return strings.Join(parts, "\n\n")
}

func ioReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// proxyHostPort returns the normalized host:port target of Traffic's HTTP proxy listener.
func (t *Traffic) proxyHostPort() string {
	if t == nil {
		return ""
	}
	raw := strings.TrimSpace(t.ProxyAddr())
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return u.Host
		}
	}
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "https://")
	return strings.TrimSuffix(raw, "/")
}

// RecordSyntheticMobileExchange records plaintext HTTP/HTTPS captures from Frida hooks
// (e.g. native SSL_write/SSL_read or OkHttp interceptors bypassing proxy paths).
func (t *Traffic) RecordSyntheticMobileExchange(host, method, rawURL string, status int, reqHead, reqBody, respHead, respBody, source string) {
	if t == nil || t.db == nil || strings.TrimSpace(rawURL) == "" {
		return
	}
	if source == "" {
		source = "mobile_hook"
	}
	if method == "" {
		method = "GET"
	}
	tmpl := "/"
	if u, err := url.Parse(rawURL); err == nil {
		if host == "" {
			host = u.Hostname()
		}
		if u.EscapedPath() != "" {
			tmpl = db.TemplatePath(u.EscapedPath())
		}
	}
	host = hostOnly(host)
	ct := "text/plain"
	for _, ln := range strings.Split(respHead, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ln)), "content-type:") {
			_, v, _ := strings.Cut(ln, ":")
			ct = strings.TrimSpace(v)
			break
		}
	}
	t.recordSyntheticExchange(host, strings.ToUpper(method), tmpl, rawURL, status, ct, source,
		reqHead, []byte(reqBody), respHead, []byte(respBody))
}
