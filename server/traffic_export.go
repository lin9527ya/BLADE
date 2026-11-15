package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lin9527ya/blade/traffic"
)

var trafficSensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
	"x-auth-token":        true,
	"x-csrf-token":        true,
}

const trafficExportMask = "REDACTED"

type trafficExportEntry struct {
	meta     *traffic.ExchangeMeta
	req      *http.Request
	resp     *http.Response
	reqBody  []byte
	respBody []byte
}

func trafficRawExchange(tr *traffic.Traffic, id string) (*trafficExportEntry, error) {
	meta, err := tr.Meta(id)
	if err != nil {
		return nil, err
	}
	reqRaw, respRaw, err := tr.Get(id)
	if err != nil {
		return nil, err
	}
	entry := &trafficExportEntry{meta: meta}
	if req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(reqRaw))); err == nil {
		entry.req = req
		entry.reqBody, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	if resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(respRaw)), entry.req); err == nil {
		entry.resp = resp
		entry.respBody, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	return entry, nil
}

func trafficMaskHeader(name, value string, mask bool) string {
	if mask && trafficSensitiveHeaders[strings.ToLower(strings.TrimSpace(name))] {
		return trafficExportMask
	}
	return value
}

func trafficShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func trafficCurl(entry *trafficExportEntry, mask bool) string {
	method := entry.meta.Method
	url := entry.meta.URL
	if method == "" && entry.req != nil {
		method = entry.req.Method
	}
	if url == "" && entry.req != nil {
		scheme := "https"
		if entry.req.URL.Scheme != "" {
			scheme = entry.req.URL.Scheme
		}
		url = scheme + "://" + entry.req.Host + entry.req.URL.RequestURI()
	}
	if method == "" {
		method = "GET"
	}
	parts := []string{"curl", "-X", trafficShellQuote(method), trafficShellQuote(url)}
	if entry.req != nil {
		names := make([]string, 0, len(entry.req.Header))
		for name := range entry.req.Header {
			if strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Connection") {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			for _, value := range entry.req.Header.Values(name) {
				parts = append(parts, "-H", trafficShellQuote(name+": "+trafficMaskHeader(name, value, mask)))
			}
		}
	}
	if len(entry.reqBody) > 0 {
		parts = append(parts, "--data-raw", trafficShellQuote(string(entry.reqBody)))
	}
	return strings.Join(parts, " ")
}

type harHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harPostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type harRequest struct {
	Method      string       `json:"method"`
	URL         string       `json:"url"`
	HTTPVersion string       `json:"httpVersion"`
	Headers     []harHeader  `json:"headers"`
	QueryString []harHeader  `json:"queryString"`
	Cookies     []harHeader  `json:"cookies"`
	HeadersSize int          `json:"headersSize"`
	BodySize    int          `json:"bodySize"`
	PostData    *harPostData `json:"postData,omitempty"`
}

type harResponse struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Headers     []harHeader `json:"headers"`
	Cookies     []harHeader `json:"cookies"`
	Content     struct {
		Size     int    `json:"size"`
		MimeType string `json:"mimeType"`
		Text     string `json:"text"`
	} `json:"content"`
	RedirectURL string `json:"redirectURL"`
	HeadersSize int    `json:"headersSize"`
	BodySize    int    `json:"bodySize"`
}

type harEntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	Time            int64       `json:"time"`
	Request         harRequest  `json:"request"`
	Response        harResponse `json:"response"`
	Cache           struct{}    `json:"cache"`
	Timings         struct {
		Send    int `json:"send"`
		Wait    int `json:"wait"`
		Receive int `json:"receive"`
	} `json:"timings"`
}

func trafficHAR(entries []*trafficExportEntry, mask bool) map[string]any {
	out := make([]harEntry, 0, len(entries))
	for _, entry := range entries {
		var h harEntry
		ts := entry.meta.TS
		if ts > 1e12 {
			h.StartedDateTime = time.UnixMilli(ts).UTC().Format(time.RFC3339Nano)
		} else {
			h.StartedDateTime = time.Unix(ts, 0).UTC().Format(time.RFC3339Nano)
		}
		h.Request.Method = entry.meta.Method
		h.Request.URL = entry.meta.URL
		h.Request.HTTPVersion = "HTTP/1.1"
		if entry.req != nil {
			if entry.req.Method != "" {
				h.Request.Method = entry.req.Method
			}
			if h.Request.URL == "" {
				h.Request.URL = "https://" + entry.req.Host + entry.req.URL.RequestURI()
			}
			h.Request.HTTPVersion = entry.req.Proto
			for name, values := range entry.req.Header {
				for _, value := range values {
					h.Request.Headers = append(h.Request.Headers, harHeader{Name: name, Value: trafficMaskHeader(name, value, mask)})
				}
			}
			for name, values := range entry.req.URL.Query() {
				for _, value := range values {
					h.Request.QueryString = append(h.Request.QueryString, harHeader{Name: name, Value: value})
				}
			}
		}
		h.Request.BodySize = len(entry.reqBody)
		if len(entry.reqBody) > 0 {
			h.Request.PostData = &harPostData{MimeType: entry.meta.ContentType, Text: string(entry.reqBody)}
		}
		h.Response.Status = entry.meta.Status
		h.Response.HTTPVersion = "HTTP/1.1"
		if entry.resp != nil {
			h.Response.Status = entry.resp.StatusCode
			h.Response.StatusText = entry.resp.Status
			h.Response.HTTPVersion = entry.resp.Proto
			for name, values := range entry.resp.Header {
				for _, value := range values {
					h.Response.Headers = append(h.Response.Headers, harHeader{Name: name, Value: trafficMaskHeader(name, value, mask)})
				}
			}
		}
		h.Response.Content.Size = len(entry.respBody)
		h.Response.Content.MimeType = entry.meta.ContentType
		h.Response.Content.Text = string(entry.respBody)
		h.Response.BodySize = len(entry.respBody)
		out = append(out, h)
	}
	return map[string]any{
		"log": map[string]any{
			"version": "1.2",
			"creator": map[string]string{"name": "BLADE", "version": "traffic-export"},
			"entries": out,
		},
	}
}

// trafficExport exports selected exchanges as cURL commands or HAR 1.2. Sensitive
// headers are masked by default; `mask=0` is an explicit operator decision.
func (s *Server) trafficExport(w http.ResponseWriter, r *http.Request) {
	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 404, "traffic disabled")
		return
	}
	ids := strings.Split(strings.TrimSpace(r.URL.Query().Get("ids")), ",")
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		writeErr(w, 400, "missing ids")
		return
	}
	if len(clean) > 200 {
		writeErr(w, 400, "一次最多导出 200 条流量")
		return
	}
	mask := r.URL.Query().Get("mask") != "0"
	entries := make([]*trafficExportEntry, 0, len(clean))
	for _, id := range clean {
		entry, err := trafficRawExchange(tr, id)
		if err != nil {
			writeErr(w, 404, fmt.Sprintf("流量 %s：%v", id, err))
			return
		}
		entries = append(entries, entry)
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	stamp := time.Now().Format("20060102-150405")
	log.Printf("[traffic-export] format=%s ids=%d mask=%v", format, len(clean), mask)
	switch format {
	case "curl":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "traffic-"+stamp+".curl.txt"))
		for i, entry := range entries {
			if i > 0 {
				_, _ = io.WriteString(w, "\n\n")
			}
			_, _ = io.WriteString(w, "# "+entry.meta.URL+"\n"+trafficCurl(entry, mask))
		}
		_, _ = io.WriteString(w, "\n")
	case "har":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "traffic-"+stamp+".har"))
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(trafficHAR(entries, mask))
	default:
		writeErr(w, 400, "format 必须是 curl 或 har")
	}
}
