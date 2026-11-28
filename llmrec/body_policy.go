package llmrec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
)

const ErrorBodyLimit = 64 << 10

var secretText = regexp.MustCompile(`(?i)(bearer[ \t]+[^\s,"'\\]+|sk-[a-zA-Z0-9_-]{8,}|(?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|client[_-]?secret|password|secret|authorization|cookie)\s*[:=]\s*[^\r\n,;}]+)`)
var quotedSecret = regexp.MustCompile(`(?i)("(?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|secret|authorization|cookie|set-cookie|x-api-key)"\s*:\s*)"(?:\\.|[^"\\])*(?:"|$)`)
var auditHeader = regexp.MustCompile(`(?im)^(\s*(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key)\s*:)\s*[^\r\n]*`)
var auditUserInfo = regexp.MustCompile(`(?i)(https?://)[^/\s@"<>]+@`)
var authLine = regexp.MustCompile(`(?im)^([ \t]*(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key)[ \t]*:)[^\r\n]*`)
var urlCredentials = regexp.MustCompile(`(?i)(https?://)[^/\s@"<>]+@`)
var imageURL = regexp.MustCompile(`(?i)data:image/[^,\s"']+,[A-Za-z0-9+/=_-]*`)

func redactText(s string) string {
	s = auditHeader.ReplaceAllString(s, "${1} [redacted]")
	s = auditUserInfo.ReplaceAllString(s, "${1}[redacted]@")
	s = authLine.ReplaceAllString(s, "${1} [redacted]")
	s = urlCredentials.ReplaceAllString(s, "${1}[redacted]@")
	s = quotedSecret.ReplaceAllString(s, `${1}"[redacted]"`)
	return secretText.ReplaceAllString(imageURL.ReplaceAllStringFunc(s, func(url string) string {
		info, bytes, _ := strings.Cut(url, ",")
		typ, _, _ := strings.Cut(strings.TrimPrefix(info, "data:"), ";")
		return fmt.Sprintf("[%s %d bytes]", typ, len(strings.TrimRight(bytes, "="))*6/8)
	}), "[redacted]")
}
func scrubValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if name, ok := x["name"].(string); ok {
			switch strings.ToLower(name) {
			case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key":
				if _, has := x["value"]; has {
					x["value"] = "[redacted]"
				}
			}
		}
		if x["type"] == "base64" {
			if mt, ok := x["media_type"].(string); ok && strings.HasPrefix(mt, "image/") {
				if data, ok := x["data"].(string); ok {
					x["data_bytes"] = (len(strings.TrimRight(data, "=")) * 6) / 8
				}
				delete(x, "data")
			}
		}
		for k, value := range x {
			key := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(k))
			switch key {
			case "authorization", "proxyauthorization", "cookie", "setcookie", "apikey", "xapikey", "token", "accesstoken", "refreshtoken", "password", "secret", "clientsecret":
				x[k] = "[redacted]"
			default:
				x[k] = scrubValue(value)
			}
		}
	case []any:
		for i := range x {
			x[i] = scrubValue(x[i])
		}
	case string:
		return redactText(x)
	}
	return v
}
func auditBody(s string, limited bool) string {
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	sensitive := false
	for _, word := range []string{"image", "base64", "bearer", "sk-", "token", "key", "password", "secret", "authorization", "cookie"} {
		if strings.Contains(lower, word) {
			sensitive = true
			break
		}
	}
	if !sensitive && !strings.Contains(s, `\u`) && !strings.Contains(s, "@") {
		return boundAudit(s, limited)
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if json.Valid([]byte(s)) && dec.Decode(&v) == nil {
		original, _ := json.Marshal(v)
		b, _ := json.Marshal(scrubValue(v))
		if string(original) != string(b) {
			s = string(b)
		}
	} else {
		// Incomplete wire captures can cut a base64 block or JSON secret mid-value.
		if strings.Contains(s, `"base64"`) || strings.Contains(s, `"image_url"`) || strings.Contains(s, `"input_image"`) {
			return "[image payload omitted]"
		}
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			if tail, ok := strings.CutPrefix(line, "data: "); ok && json.Valid([]byte(tail)) {
				lines[i] = "data: " + auditBody(tail, false)
			} else {
				lines[i] = redactText(line)
			}
		}
		s = strings.Join(lines, "\n")
	}
	return boundAudit(s, limited)
}
func boundAudit(s string, limited bool) string {
	if limited && len(s) > ErrorBodyLimit {
		end := ErrorBodyLimit - len("…")
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		return s[:end] + "…"
	}
	return s
}
func sanitizeAudit(r *db.LLMRecord, limited bool) {
	for _, field := range []*string{&r.RequestBody, &r.ResponseBody, &r.RawRequest, &r.RawResponse, &r.Error} {
		*field = auditBody(*field, limited)
	}
}
func (r *Recorder) WithBodyMode(mode func() string) *Recorder { r.bodyMode = mode; return r }
func (r *Recorder) recordingMode() string {
	if r.bodyMode != nil {
		return r.bodyMode()
	}
	if r.enabled == nil || r.enabled() {
		return "full"
	}
	return "off"
}
