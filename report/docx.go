package report

import (
	"archive/zip"
	"bytes"
	"strconv"
	"strings"
)

// SrcDOCX renders the SRC submission (see SrcMarkdown) as a .docx built with the
// standard library only — a minimal OOXML package (content types, rels, styles,
// document). No python-docx / LibreOffice dependency, so the single binary can
// always produce a submittable document; the version styled by the user's
// render_docx.py remains available from the exported .md.
func SrcDOCX(in SrcInput) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct{ name, body string }{
		{"[Content_Types].xml", docxContentTypes},
		{"_rels/.rels", docxRootRels},
		{"word/_rels/document.xml.rels", docxDocRels},
		{"word/styles.xml", docxStyles},
		{"word/document.xml", docxDocument(in)},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const docxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`

const docxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

const docxDocRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

// docxStyles: minimal style set — Normal (CJK font), 标题, 小节标题, 元信息, 代码块
// （等宽 + 浅灰底）；分隔线用段落下边框实现。字体名按 Word 惯例（微软雅黑/Consolas）。
const docxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults>
<w:rPrDefault><w:rPr><w:rFonts w:ascii="Segoe UI" w:hAnsi="Segoe UI" w:eastAsia="微软雅黑"/><w:sz w:val="21"/><w:szCs w:val="21"/></w:rPr></w:rPrDefault>
<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault>
</w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="SrcTitle"><w:name w:val="SrcTitle"/><w:basedOn w:val="Normal"/>
<w:pPr><w:spacing w:after="60"/></w:pPr>
<w:rPr><w:b/><w:sz w:val="34"/><w:color w:val="1F3A68"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="SrcHeading"><w:name w:val="SrcHeading"/><w:basedOn w:val="Normal"/>
<w:pPr><w:spacing w:before="120" w:after="60"/></w:pPr>
<w:rPr><w:b/><w:sz w:val="26"/><w:color w:val="1F3A68"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="SrcMeta"><w:name w:val="SrcMeta"/><w:basedOn w:val="Normal"/>
<w:pPr><w:spacing w:after="40"/></w:pPr>
<w:rPr><w:color w:val="595959"/><w:sz w:val="19"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="SrcCode"><w:name w:val="SrcCode"/><w:basedOn w:val="Normal"/>
<w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F4F4F4"/><w:spacing w:after="120" w:line="240" w:lineRule="auto"/><w:ind w:left="113"/></w:pPr>
<w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas" w:eastAsia="等线"/><w:sz w:val="18"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="SrcRule"><w:name w:val="SrcRule"/><w:basedOn w:val="Normal"/>
<w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="BFBFBF"/></w:pBdr><w:spacing w:after="120"/></w:pPr>
<w:rPr><w:sz w:val="2"/></w:rPr></w:style>
</w:styles>`

func docxDocument(in SrcInput) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	b.WriteString(docxPara("SrcTitle", nz(in.Title, "（待补：谁的站、做成了啥）")))
	b.WriteString(docxMeta("目标网站URL：" + nz(in.TargetURL, "（待补）")))
	b.WriteString(docxSeverity(SrcSeverityLabel(in.Severity)))
	b.WriteString(docxRule())

	b.WriteString(docxPara("SrcHeading", "漏洞描述"))
	b.WriteString(docxPara("", nz(in.Description, "（待补：第一人称大白话写做成了什么；没登录态用「我没登录」，有会话用「我拿自己的会话」）")))
	b.WriteString(docxRule())
	b.WriteString(docxPara("SrcHeading", "漏洞危害"))
	b.WriteString(docxPara("", nz(in.Impact, "（待补：只写最终伤害，每种伤害单独一行；不写步骤）")))
	b.WriteString(docxRule())

	b.WriteString(docxPara("SrcHeading", "涉及接口清单"))
	if len(in.Endpoints) == 0 {
		b.WriteString(docxPara("", "（无已登记接口：证据流量未绑定，请在发现详情绑定 proof/verification 流量后重新导出）"))
	} else {
		for i, ep := range in.Endpoints {
			line := strings.TrimSpace(strings.TrimSpace(ep.Method) + " " + ep.URL)
			if strings.TrimSpace(ep.Source) != "" {
				line += "（" + strings.TrimSpace(ep.Source) + "）"
			}
			b.WriteString(docxPara("", strconv.Itoa(i+1)+"、"+line))
		}
	}
	b.WriteString(docxRule())

	b.WriteString(docxPara("SrcHeading", "复现步骤"))
	if len(in.Steps) == 0 {
		b.WriteString(docxPara("", "（无整包证据：请在发现详情绑定 proof/verification 流量后重新导出）"))
	} else {
		for i, st := range in.Steps {
			b.WriteString(docxPara("", strconv.Itoa(i+1)+"、"+nz(st.Desc, "打穿步骤")))
			if raw := strings.TrimRight(st.Raw, "\r\n"); strings.TrimSpace(raw) != "" {
				b.WriteString(docxPara("SrcCode", raw))
			}
			if i < len(in.Steps)-1 {
				b.WriteString(docxRule())
			}
		}
	}
	b.WriteString(docxRule())
	b.WriteString(docxPara("SrcHeading", "修复建议"))
	b.WriteString(docxPara("", nz(in.Fix, "（待补：贴合本漏洞、可落地的整改方案）")))
	for _, n := range in.Notes {
		if strings.TrimSpace(n) == "" {
			continue
		}
		b.WriteString(docxRule())
		b.WriteString(docxMeta("备注：" + strings.TrimSpace(n)))
	}

	b.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>`)
	b.WriteString(`</w:body></w:document>`)
	return b.String()
}

func docxPara(style, text string) string {
	if style == "" {
		return "<w:p>" + docxRuns(text) + "</w:p>"
	}
	return `<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>` + docxRuns(text) + `</w:p>`
}

func docxMeta(text string) string {
	return `<w:p><w:pPr><w:pStyle w:val="SrcMeta"/></w:pPr>` + docxRuns(text) + `</w:p>`
}

// docxSeverity renders 漏洞等级 with the severity color (严重红/高危橙/中危黄/低危绿).
func docxSeverity(label string) string {
	color := map[string]string{"严重": "C0392B", "高危": "D35400", "中危": "B7770D", "低危": "1A7A4A"}[label]
	if color == "" {
		color = "595959"
	}
	return `<w:p><w:pPr><w:pStyle w:val="SrcMeta"/></w:pPr><w:r><w:rPr><w:b/><w:color w:val="` + color + `"/></w:rPr><w:t xml:space="preserve">漏洞等级：` + docxEscape(label) + `</w:t></w:r></w:p>`
}

func docxRule() string {
	return `<w:p><w:pPr><w:pStyle w:val="SrcRule"/></w:pPr></w:p>`
}

// docxRuns splits text on newlines into runs joined by <w:br/> (keeps a raw request
// packet as one shaded paragraph, which reads like a code block in Word).
func docxRuns(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			b.WriteString("<w:r><w:br/></w:r>")
		}
		if ln == "" {
			continue
		}
		b.WriteString(`<w:r><w:t xml:space="preserve">` + docxEscape(ln) + `</w:t></w:r>`)
	}
	return b.String()
}

// docxEscape escapes XML text and drops control characters XML 1.0 forbids
// (raw traffic can carry NULs and other C0 bytes).
func docxEscape(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}
