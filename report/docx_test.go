package report

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// TestSrcDOCXStructure pins the docx package: a valid zip with the five OOXML
// parts, a document.xml that parses as XML, the 8 blocks present, the raw packet
// kept inside the shaded code style, severity colored by level, and special
// characters escaped so they round-trip through an XML parser.
func TestSrcDOCXStructure(t *testing.T) {
	in := SrcInput{
		Title:       `XSS & <script> "引号" 测试`,
		TargetURL:   "https://example.com/a?b=1&c=2",
		Severity:    "high",
		Description: "我拿自己的会话做成了越权读取。",
		Impact:      "能批量读取他人订单。",
		Endpoints:   []SrcEndpoint{{Method: "POST", URL: "https://example.com/api/order", Source: "proof 流量 #7"}},
		Steps:       []SrcStep{{Desc: "打穿。", Raw: "POST /api/order HTTP/1.1\r\nHost: example.com\r\nContent-Length: 2\r\n\r\nid=1"}},
		Fix:         "加对象级鉴权。",
		Notes:       []string{"发现 #7 · 证据版本 v3"},
	}
	blob, err := SrcDOCX(in)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	wantParts := map[string]bool{
		"[Content_Types].xml": false, "_rels/.rels": false,
		"word/_rels/document.xml.rels": false, "word/styles.xml": false, "word/document.xml": false,
	}
	var doc, styles string
	for _, f := range zr.File {
		if _, ok := wantParts[f.Name]; ok {
			wantParts[f.Name] = true
		}
		if f.Name != "word/document.xml" && f.Name != "word/styles.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == "word/document.xml" {
			doc = string(b)
		} else {
			styles = string(b)
		}
	}
	for name, seen := range wantParts {
		if !seen {
			t.Fatalf("docx 缺少部件 %s", name)
		}
	}
	var v struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("document.xml 非法 XML: %v", err)
	}
	// 取所有字符数据：转义是否正确（解析后应与原文一致）
	var all []string
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if cd, ok := tok.(xml.CharData); ok {
			all = append(all, string(cd))
		}
	}
	text := strings.Join(all, "")
	for _, want := range []string{
		`XSS & <script> "引号" 测试`,
		"https://example.com/a?b=1&c=2",
		"漏洞等级：高危",
		"漏洞描述", "漏洞危害", "涉及接口清单", "复现步骤", "修复建议",
		"POST /api/order HTTP/1.1", "Host: example.com", "id=1",
		"1、POST https://example.com/api/order（proof 流量 #7）",
		"备注：发现 #7 · 证据版本 v3",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("document 缺少 %q", want)
		}
	}
	// 版式：代码块/分隔线/标题样式存在；高危用橙色
	for _, want := range []string{"SrcCode", "SrcRule", "SrcTitle", "SrcHeading"} {
		if !strings.Contains(styles, want) {
			t.Fatalf("styles.xml 缺少样式 %s", want)
		}
	}
	if !strings.Contains(doc, `w:color w:val="D35400"`) {
		t.Fatal("高危等级缺少橙色标注")
	}
	// 原始报文里的控制字符不得让 XML 失效（\r 去除、正文保留）
	if strings.Contains(doc, "\r") {
		t.Fatal("document.xml 不应含裸 CR")
	}
}
