package mobile

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Android binary XML chunk types (frameworks/base/libs/androidfw/include/androidfw/ResourceTypes.h).
const (
	resNullType              = 0x0000
	resStringPoolType        = 0x0001
	resXMLType               = 0x0003
	resXMLResourceMapType    = 0x0180
	resXMLStartNamespaceType = 0x0100
	resXMLEndNamespaceType   = 0x0101
	resXMLStartElementType   = 0x0102
	resXMLEndElementType     = 0x0103
	resXMLCDataType          = 0x0104
	stringPoolUTF8Flag       = 1 << 8
	androidAttrNoIndex       = 0xffffffff
	maxAXMLDecodeBytes       = 16 << 20
	maxAXMLStringPoolEntries = 200000
	maxAXMLElementDepth      = 256
)

// Android typed resource value types (Res_value::dataType).
const (
	resTypeNull       = 0x00
	resTypeReference  = 0x01
	resTypeString     = 0x03
	resTypeFloat      = 0x04
	resTypeIntDec     = 0x10
	resTypeIntHex     = 0x11
	resTypeIntBoolean = 0x12
)

type axmlDoc struct {
	root *axmlElement
}

type axmlElement struct {
	name     string
	attrs    map[string]string
	children []*axmlElement
}

func (e *axmlElement) attr(keys ...string) (string, bool) {
	if e == nil {
		return "", false
	}
	for _, k := range keys {
		if v, ok := e.attrs[k]; ok {
			return v, true
		}
	}
	return "", false
}

// ParseAndroidManifest decodes either binary AndroidManifest.xml (AXML) or plain XML.
func ParseAndroidManifest(raw []byte, meta *PackageMetadata) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty AndroidManifest.xml")
	}
	if len(raw) > maxAXMLDecodeBytes {
		return fmt.Errorf("AndroidManifest.xml exceeds limit (%d bytes)", len(raw))
	}
	var doc *axmlDoc
	var err error
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("<")) {
		doc, err = parsePlainManifestXML(raw)
	} else {
		doc, err = parseBinaryAXML(raw)
	}
	if err != nil {
		return err
	}
	if doc == nil || doc.root == nil {
		return fmt.Errorf("manifest has no root element")
	}
	applyManifestToMeta(doc.root, meta)
	return nil
}

func parsePlainManifestXML(raw []byte) (*axmlDoc, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var stack []*axmlElement
	var root *axmlElement
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse plain manifest xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := &axmlElement{name: t.Name.Local, attrs: make(map[string]string, len(t.Attr))}
			for _, a := range t.Attr {
				el.attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, el)
			} else {
				root = el
			}
			if len(stack) >= maxAXMLElementDepth {
				return nil, fmt.Errorf("manifest xml nesting too deep")
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return &axmlDoc{root: root}, nil
}

func parseBinaryAXML(data []byte) (*axmlDoc, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("truncated axml header")
	}
	topType := binary.LittleEndian.Uint16(data[0:2])
	topHdrSize := int(binary.LittleEndian.Uint16(data[2:4]))
	topSize := int(binary.LittleEndian.Uint32(data[4:8]))
	if topType != resXMLType && topType != resNullType {
		return nil, fmt.Errorf("unexpected axml magic type 0x%04x", topType)
	}
	if topHdrSize < 8 || topHdrSize > len(data) {
		topHdrSize = 8
	}
	limit := len(data)
	if topSize > topHdrSize && topSize < limit {
		limit = topSize
	}

	var stringsPool []string
	var stack []*axmlElement
	var root *axmlElement

	off := topHdrSize
	for off+8 <= limit {
		cType := binary.LittleEndian.Uint16(data[off : off+2])
		cHdrSize := int(binary.LittleEndian.Uint16(data[off+2 : off+4]))
		cSize := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		if cSize < 8 || off+cSize > limit {
			break
		}
		if cHdrSize < 8 || cHdrSize > cSize {
			off += cSize
			continue
		}
		chunk := data[off : off+cSize]
		switch cType {
		case resStringPoolType:
			pool, err := decodeAXMLStringPool(chunk)
			if err != nil {
				return nil, err
			}
			stringsPool = pool
		case resXMLResourceMapType, resXMLStartNamespaceType, resXMLEndNamespaceType, resXMLCDataType:
			// Namespace/resource ID maps aren't required once element/attr local names resolve from stringsPool.
		case resXMLStartElementType:
			el, err := decodeAXMLStartElement(chunk, cHdrSize, stringsPool)
			if err != nil {
				return nil, err
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, el)
			} else {
				root = el
			}
			if len(stack) >= maxAXMLElementDepth {
				return nil, fmt.Errorf("axml element depth exceeded")
			}
			stack = append(stack, el)
		case resXMLEndElementType:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		off += cSize
	}
	if root == nil {
		return nil, fmt.Errorf("no elements decoded from axml")
	}
	return &axmlDoc{root: root}, nil
}

func decodeAXMLStringPool(chunk []byte) ([]string, error) {
	if len(chunk) < 28 {
		return nil, fmt.Errorf("truncated axml string pool header")
	}
	hdrSize := int(binary.LittleEndian.Uint16(chunk[2:4]))
	strCount := int(binary.LittleEndian.Uint32(chunk[8:12]))
	flags := binary.LittleEndian.Uint32(chunk[16:20])
	stringsStart := int(binary.LittleEndian.Uint32(chunk[20:24]))
	if strCount < 0 || strCount > maxAXMLStringPoolEntries {
		return nil, fmt.Errorf("unreasonable string pool count: %d", strCount)
	}
	if hdrSize < 28 {
		hdrSize = 28
	}
	offsetsEnd := hdrSize + strCount*4
	if offsetsEnd > len(chunk) || stringsStart < offsetsEnd || stringsStart > len(chunk) {
		return nil, fmt.Errorf("corrupt axml string pool layout")
	}
	isUTF8 := (flags & stringPoolUTF8Flag) != 0
	out := make([]string, strCount)
	for i := 0; i < strCount; i++ {
		relOff := int(binary.LittleEndian.Uint32(chunk[hdrSize+i*4 : hdrSize+(i+1)*4]))
		pos := stringsStart + relOff
		if pos < stringsStart || pos >= len(chunk) {
			continue
		}
		if isUTF8 {
			out[i] = readPoolUTF8(chunk[pos:])
		} else {
			out[i] = readPoolUTF16(chunk[pos:])
		}
	}
	return out, nil
}

func readPoolUTF8(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	// Skip UTF-16 char count, then read encoded byte length.
	idx := 1
	if (b[0] & 0x80) != 0 {
		idx++
	}
	if idx >= len(b) {
		return ""
	}
	byteLen := int(b[idx])
	idx++
	if (byteLen & 0x80) != 0 {
		if idx >= len(b) {
			return ""
		}
		byteLen = ((byteLen & 0x7f) << 8) | int(b[idx])
		idx++
	}
	if idx+byteLen > len(b) {
		byteLen = len(b) - idx
	}
	if byteLen <= 0 {
		return ""
	}
	return string(b[idx : idx+byteLen])
}

func readPoolUTF16(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	u16Len := int(binary.LittleEndian.Uint16(b[0:2]))
	idx := 2
	if (u16Len & 0x8000) != 0 {
		if len(b) < 4 {
			return ""
		}
		u16Len = ((u16Len & 0x7fff) << 16) | int(binary.LittleEndian.Uint16(b[2:4]))
		idx = 4
	}
	byteLen := u16Len * 2
	if idx+byteLen > len(b) {
		byteLen = (len(b) - idx) &^ 1
	}
	if byteLen <= 0 {
		return ""
	}
	codes := make([]uint16, byteLen/2)
	for i := range codes {
		codes[i] = binary.LittleEndian.Uint16(b[idx+i*2 : idx+i*2+2])
	}
	return string(utf16.Decode(codes))
}

func decodeAXMLStartElement(chunk []byte, hdrSize int, pool []string) (*axmlElement, error) {
	// ResXMLTree_node(8+8) + ResXMLTree_attrExt(20):
	// ext starts at hdrSize if hdrSize == 16; standard Android encodes hdrSize=16, ext at 16..36, attrs at 36+.
	extStart := 16
	if hdrSize > extStart && hdrSize < len(chunk) {
		extStart = hdrSize
	}
	if extStart+20 > len(chunk) {
		return nil, fmt.Errorf("truncated axml start element")
	}
	ext := chunk[extStart:]
	nameIdx := binary.LittleEndian.Uint32(ext[4:8])
	attrStart := int(binary.LittleEndian.Uint16(ext[8:10]))
	attrSize := int(binary.LittleEndian.Uint16(ext[10:12]))
	attrCount := int(binary.LittleEndian.Uint16(ext[12:14]))
	if attrSize < 20 {
		attrSize = 20
	}
	attrBase := extStart + attrStart
	elName := poolStr(pool, nameIdx)
	attrs := make(map[string]string, attrCount)
	for i := 0; i < attrCount; i++ {
		p := attrBase + i*attrSize
		if p+20 > len(chunk) {
			break
		}
		aNameIdx := binary.LittleEndian.Uint32(chunk[p+4 : p+8])
		rawValIdx := binary.LittleEndian.Uint32(chunk[p+8 : p+12])
		dataType := chunk[p+15]
		dataVal := binary.LittleEndian.Uint32(chunk[p+16 : p+20])
		key := poolStr(pool, aNameIdx)
		if key == "" {
			continue
		}
		val := resolveAXMLAttrValue(pool, rawValIdx, dataType, dataVal)
		attrs[key] = val
	}
	return &axmlElement{name: elName, attrs: attrs}, nil
}

func resolveAXMLAttrValue(pool []string, rawValIdx uint32, dataType byte, dataVal uint32) string {
	switch dataType {
	case resTypeString:
		if s := poolStr(pool, dataVal); s != "" {
			return s
		}
		return poolStr(pool, rawValIdx)
	case resTypeIntBoolean:
		if dataVal != 0 {
			return "true"
		}
		return "false"
	case resTypeIntDec:
		return strconv.FormatInt(int64(int32(dataVal)), 10)
	case resTypeIntHex:
		return fmt.Sprintf("0x%x", dataVal)
	case resTypeReference:
		if s := poolStr(pool, rawValIdx); s != "" {
			return s
		}
		return fmt.Sprintf("@0x%08x", dataVal)
	case resTypeNull:
		return poolStr(pool, rawValIdx)
	default:
		if s := poolStr(pool, rawValIdx); s != "" {
			return s
		}
		return strconv.FormatUint(uint64(dataVal), 10)
	}
}

func poolStr(pool []string, idx uint32) string {
	if idx == androidAttrNoIndex || int(idx) >= len(pool) {
		return ""
	}
	return strings.TrimSpace(pool[idx])
}

func applyManifestToMeta(root *axmlElement, meta *PackageMetadata) {
	if root.name == "manifest" {
		if pkg, ok := root.attr("package"); ok {
			meta.PackageName = pkg
		}
		if vc, ok := root.attr("versionCode"); ok {
			meta.VersionCode = vc
		}
		if vn, ok := root.attr("versionName"); ok {
			meta.VersionName = vn
		}
	}
	for _, child := range root.children {
		switch child.name {
		case "uses-sdk":
			if v, ok := child.attr("minSdkVersion"); ok {
				meta.MinSDK, _ = strconv.Atoi(v)
			}
			if v, ok := child.attr("targetSdkVersion"); ok {
				meta.TargetSDK, _ = strconv.Atoi(v)
			}
		case "uses-permission", "uses-permission-sdk-23":
			if name, ok := child.attr("name"); ok && name != "" {
				meta.Permissions = append(meta.Permissions, PermissionSummary{
					Name:      name,
					Dangerous: isDangerousPermission(name),
				})
			}
		case "application":
			if label, ok := child.attr("label"); ok && label != "" && !strings.HasPrefix(label, "@0x") {
				meta.AppName = label
			}
			if v, ok := child.attr("debuggable"); ok && v == "true" {
				meta.Debuggable = true
				meta.Warnings = appendUnique(meta.Warnings, "AndroidManifest enabled android:debuggable=true")
			}
			if v, ok := child.attr("allowBackup"); ok {
				meta.AllowBackup = (v == "true")
			} else {
				// Default allowBackup on Android is true unless explicitly disabled.
				meta.AllowBackup = true
			}
			if v, ok := child.attr("usesCleartextTraffic"); ok {
				b := (v == "true")
				meta.CleartextTraffic = &b
				if b {
					meta.Warnings = appendUnique(meta.Warnings, "android:usesCleartextTraffic=true allows plain HTTP traffic")
				}
			}
			if v, ok := child.attr("networkSecurityConfig"); ok && v != "" {
				meta.NetworkSecurityCfg = v
			}
			for _, comp := range child.children {
				parseComponentElement(comp, meta)
			}
		}
	}
}

func parseComponentElement(el *axmlElement, meta *PackageMetadata) {
	compType := ""
	switch el.name {
	case "activity", "activity-alias":
		compType = "activity"
	case "service":
		compType = "service"
	case "receiver":
		compType = "receiver"
	case "provider":
		compType = "provider"
	default:
		return
	}
	name, _ := el.attr("name")
	if name == "" {
		return
	}
	perm, _ := el.attr("permission")
	auth, _ := el.attr("authorities")
	expStr, hasExp := el.attr("exported")

	var filters []IntentFilter
	for _, sub := range el.children {
		if sub.name != "intent-filter" {
			continue
		}
		var flt IntentFilter
		for _, item := range sub.children {
			switch item.name {
			case "action":
				if v, ok := item.attr("name"); ok && v != "" {
					flt.Actions = append(flt.Actions, v)
				}
			case "category":
				if v, ok := item.attr("name"); ok && v != "" {
					flt.Categories = append(flt.Categories, v)
				}
			case "data":
				if s, ok := item.attr("scheme"); ok && s != "" {
					flt.Schemes = appendUnique(flt.Schemes, s)
					if s != "http" && s != "https" {
						meta.URLSchemes = appendUnique(meta.URLSchemes, s)
					}
				}
				if h, ok := item.attr("host"); ok && h != "" {
					flt.Hosts = appendUnique(flt.Hosts, h)
					meta.CandidateDomains = appendUnique(meta.CandidateDomains, sanitizeCandidateHost(h))
				}
				if p, ok := item.attr("pathPrefix"); ok && p != "" && flt.PathPrefix == "" {
					flt.PathPrefix = p
				}
			}
		}
		filters = append(filters, flt)
	}

	exported := false
	implicit := false
	if hasExp {
		exported = (expStr == "true")
	} else if len(filters) > 0 {
		exported = true
		implicit = true
	}
	if exported || len(filters) > 0 {
		meta.ExportedComponents = append(meta.ExportedComponents, ComponentSummary{
			Type:        compType,
			Name:        name,
			Exported:    exported,
			Implicit:    implicit,
			Permission:  perm,
			Authorities: auth,
			Filters:     filters,
		})
	}
}

func isDangerousPermission(name string) bool {
	switch name {
	case "android.permission.READ_PHONE_STATE",
		"android.permission.READ_SMS",
		"android.permission.RECEIVE_SMS",
		"android.permission.SEND_SMS",
		"android.permission.ACCESS_FINE_LOCATION",
		"android.permission.ACCESS_COARSE_LOCATION",
		"android.permission.ACCESS_BACKGROUND_LOCATION",
		"android.permission.CAMERA",
		"android.permission.RECORD_AUDIO",
		"android.permission.READ_CONTACTS",
		"android.permission.WRITE_CONTACTS",
		"android.permission.READ_EXTERNAL_STORAGE",
		"android.permission.WRITE_EXTERNAL_STORAGE",
		"android.permission.MANAGE_EXTERNAL_STORAGE",
		"android.permission.SYSTEM_ALERT_WINDOW",
		"android.permission.REQUEST_INSTALL_PACKAGES":
		return true
	default:
		return false
	}
}
