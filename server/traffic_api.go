// 流量录制器的 HTTP 接口。
// 从 server.go 拆出：纯搬运，零行为变化。
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/traffic"
)

func (s *Server) getTraffic(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeJSON(w, 200, map[string]any{"enabled": false, "exchanges": []any{}})
		return
	}
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 0)
	size := atoiDefault(q.Get("size"), 100)
	ex, matched, _ := tr.Page(traffic.PageQuery{
		Host:         q.Get("host"),
		Method:       q.Get("method"),
		Query:        q.Get("q"),
		Body:         q.Get("body"),
		Path:         q.Get("path"),
		Status:       q.Get("status"),
		Source:       q.Get("source"),
		ExcludeNoise: q.Get("exclude_noise") == "1" || strings.EqualFold(q.Get("exclude_noise"), "true"),
		RespMin:      int64(atoiDefault(q.Get("resp_min"), -1)),
		RespMax:      int64(atoiDefault(q.Get("resp_max"), -1)),
		Sort:         q.Get("sort"),
		Order:        q.Get("order"),
	}, page, size)
	count, _ := tr.Count() // global total, for the stat card
	writeJSON(w, 200, map[string]any{
		"enabled":   s.m.TrafficEnabled(), // reflect the capture toggle
		"proxy":     s.m.ProxyAddr(),
		"count":     count,   // total recorded (unfiltered)
		"total":     matched, // rows matching the current filter (for pagination)
		"page":      page,
		"size":      size,
		"exchanges": trafficDTOs(ex),
	})
}

// getTrafficHosts returns distinct recorded hosts with counts, for the page's
// target picker (pick a host → filter the list, then delete it).
func (s *Server) getTrafficHosts(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeJSON(w, 200, map[string]any{"hosts": []any{}})
		return
	}
	hosts, err := tr.Hosts()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"hosts": hosts})
}

// deleteTraffic removes recorded traffic for every host containing the query's
// host substring (the page's host filter is substring-based, so what you
// filtered is what gets deleted): index rows + each host's file tree, then
// garbage-collects blobs no remaining exchange references. Empty host → 400.
// Returns the number of exchanges deleted.
func (s *Server) deleteTraffic(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		writeErr(w, 400, "missing host")
		return
	}
	n, err := tr.DeleteHost(host)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// deleteTrafficHosts removes traffic for a set of EXACT hosts (JSON body
// {"hosts": [...]}) — the batch path for the page's multi-select delete. Exact
// match, so picking "api.example.com" never sweeps "api.example.com.cn".
// Returns the number of exchanges deleted.
func (s *Server) deleteTrafficHosts(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	var req struct {
		Hosts []string `json:"hosts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid body")
		return
	}
	hosts := make([]string, 0, len(req.Hosts))
	for _, h := range req.Hosts {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		writeErr(w, 400, "missing hosts")
		return
	}
	n, err := tr.DeleteHostsExact(hosts)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// deleteAllTraffic purges every recorded exchange, then compacts the index so
// the space is actually returned to the filesystem — an emptied index is the one
// moment a full rewrite is cheap. Evidence already bound to a finding lives in
// the evidence store and is deliberately left alone. Returns the number of
// exchanges deleted and the bytes of index reclaimed.
func (s *Server) deleteAllTraffic(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	n, reclaimed, err := tr.DeleteAll()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n, "reclaimed": reclaimed})
}

// getTrafficExchange returns the full raw request/response of one exchange,
// read on demand from the traffic tree (bodies are not in the paged list).
func (s *Server) getTrafficExchange(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, 400, "missing id")
		return
	}
	req, resp, err := tr.Get(id)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	out := map[string]any{"req": req, "resp": resp}
	if preview := tr.ProtobufPreviewForExchange(id); preview != "" {
		out["protobuf_preview"] = preview
	}
	writeJSON(w, 200, out)
}

// getTrafficBlob streams one oversized body by its sha256. Bodies past the inline
// threshold are not carried by the exchange endpoint — it returns a preview plus
// an "@blob sha256:<hash>" pointer — so this is how the UI fetches them whole.
// Streamed rather than buffered: these are the bodies too large to hold in memory.
func (s *Server) getTrafficBlob(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	hash := strings.TrimSpace(r.URL.Query().Get("hash"))
	if hash == "" {
		writeErr(w, 400, "missing hash")
		return
	}
	f, size, err := tr.Blob(hash)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", hash+".bin"))
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("[traffic] 下载 blob %s 中断：%v", hash, err)
	}
}

func (s *Server) getTrafficCA(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	fmtType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("fmt")))
	switch fmtType {
	case "", "pem":
		data, err := tr.CACertPEM()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.pem"`)
		_, _ = w.Write(data)
	case "crt", "der":
		data, err := tr.CACertDER()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.crt"`)
		_, _ = w.Write(data)
	case "android", "0":
		hash, err := tr.CASubjectHash()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		data, err := tr.CACertPEM()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.0"`, hash))
		_, _ = w.Write(data)
	case "mobileconfig", "ios":
		data, err := tr.AppleMobileConfig("BLADE")
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/x-apple-aspen-config")
		w.Header().Set("Content-Disposition", `attachment; filename="blade-mitm-ca.mobileconfig"`)
		_, _ = w.Write(data)
	default:
		writeErr(w, 400, "fmt 必须为 pem / crt / android / mobileconfig")
	}
}

func (s *Server) getTrafficMobileSetup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.buildMobileSetupInfo(r.Host))
}

func (s *Server) controlTrafficMobileProxy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action     string   `json:"action"` // start | stop
		ListenAddr string   `json:"listen_addr"`
		CIDRs      []string `json:"allow_cidrs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "start":
		if err := s.m.StartMobileProxy(req.ListenAddr, req.CIDRs); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	case "stop":
		if err := s.m.StopMobileProxy(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	default:
		writeErr(w, 400, "action 必须是 start 或 stop")
		return
	}
	writeJSON(w, 200, s.buildMobileSetupInfo(r.Host))
}

func (s *Server) getTrafficPinningAlerts(w http.ResponseWriter, _ *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeJSON(w, 200, map[string]any{"alerts": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"alerts": tr.PinningAlerts()})
}

func (s *Server) clearTrafficPinningAlerts(w http.ResponseWriter, _ *http.Request) {
	if tr := s.m.Traffic(); tr != nil {
		tr.ClearPinningAlerts()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
