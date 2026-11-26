package mobile

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"
)

const (
	bplistMagic          = "bplist00"
	maxPlistDecodeBytes  = 8 << 20
	maxBplistObjectCount = 100000
	maxPlistDepth        = 64
)

// ParseInfoPlist parses Apple XML or binary ("bplist00") Info.plist into PackageMetadata.
func ParseInfoPlist(raw []byte, meta *PackageMetadata) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty Info.plist")
	}
	if len(raw) > maxPlistDecodeBytes {
		return fmt.Errorf("Info.plist exceeds limit")
	}
	var root any
	var err error
	if bytes.HasPrefix(raw, []byte(bplistMagic)) {
		root, err = parseBinaryPlist(raw)
	} else {
		root, err = parseXMLPlist(raw)
	}
	if err != nil {
		return err
	}
	dict, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("Info.plist root is not a dictionary")
	}
	applyInfoPlistToMeta(dict, meta)
	return nil
}

func applyInfoPlistToMeta(dict map[string]any, meta *PackageMetadata) {
	if v, ok := dict["CFBundleIdentifier"].(string); ok && v != "" {
		meta.PackageName = strings.TrimSpace(v)
	}
	for _, key := range []string{"CFBundleDisplayName", "CFBundleName", "CFBundleExecutable"} {
		if v, ok := dict[key].(string); ok && strings.TrimSpace(v) != "" {
			meta.AppName = strings.TrimSpace(v)
			break
		}
	}
	if v, ok := dict["CFBundleShortVersionString"].(string); ok && v != "" {
		meta.VersionName = strings.TrimSpace(v)
	}
	if v, ok := dict["CFBundleVersion"].(string); ok && v != "" {
		meta.VersionCode = strings.TrimSpace(v)
	}
	if atsRaw, ok := dict["NSAppTransportSecurity"].(map[string]any); ok {
		ats := &ATSSummary{}
		if b, ok := atsRaw["NSAllowsArbitraryLoads"].(bool); ok {
			ats.AllowsArbitraryLoads = b
			if b {
				meta.Warnings = appendUnique(meta.Warnings, "NSAllowsArbitraryLoads=true disables iOS ATS TLS enforcement globally")
			}
		}
		if exMap, ok := atsRaw["NSExceptionDomains"].(map[string]any); ok {
			for dom := range exMap {
				ats.ExceptionDomains = appendUnique(ats.ExceptionDomains, dom)
				if host := sanitizeCandidateHost(dom); host != "" {
					meta.CandidateDomains = appendUnique(meta.CandidateDomains, host)
				}
			}
		}
		meta.ATS = ats
	}
	if urlTypes, ok := dict["CFBundleURLTypes"].([]any); ok {
		for _, entry := range urlTypes {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			schemes, ok := m["CFBundleURLSchemes"].([]any)
			if !ok {
				continue
			}
			for _, s := range schemes {
				if str, ok := s.(string); ok && strings.TrimSpace(str) != "" {
					meta.URLSchemes = appendUnique(meta.URLSchemes, strings.TrimSpace(str))
				}
			}
		}
	}
}

// CheckMachOFairPlayEncrypted inspects Mach-O load commands (single arch or fat header)
// for LC_ENCRYPTION_INFO / LC_ENCRYPTION_INFO_64 with cryptid > 0.
func CheckMachOFairPlayEncrypted(raw []byte) (encrypted bool, detected bool) {
	if len(raw) < 32 {
		return false, false
	}
	magicBE := binary.BigEndian.Uint32(raw[:4])
	magicLE := binary.LittleEndian.Uint32(raw[:4])
	// Fat/Universal binary: jump to first slice.
	if magicBE == 0xcafebabe || magicBE == 0xcafebabf {
		nfat := int(binary.BigEndian.Uint32(raw[4:8]))
		if nfat <= 0 || nfat > 16 {
			return false, false
		}
		entrySize := 20
		if magicBE == 0xcafebabf {
			entrySize = 32
		}
		if 8+entrySize > len(raw) {
			return false, false
		}
		var sliceOff uint64
		if magicBE == 0xcafebabf {
			sliceOff = binary.BigEndian.Uint64(raw[8+8 : 8+16])
		} else {
			sliceOff = uint64(binary.BigEndian.Uint32(raw[8+8 : 8+12]))
		}
		if sliceOff >= uint64(len(raw)) {
			return false, false
		}
		return CheckMachOFairPlayEncrypted(raw[sliceOff:])
	}

	var is64 bool
	switch magicLE {
	case 0xfeedface:
		is64 = false
	case 0xfeedfacf:
		is64 = true
	default:
		return false, false
	}
	hdrSize := 28
	if is64 {
		hdrSize = 32
	}
	if len(raw) < hdrSize {
		return false, false
	}
	ncmds := int(binary.LittleEndian.Uint32(raw[16:20]))
	sizeofcmds := int(binary.LittleEndian.Uint32(raw[20:24]))
	off := hdrSize
	end := hdrSize + sizeofcmds
	if end > len(raw) {
		end = len(raw)
	}
	const (
		lcEncryptionInfo   = 0x21
		lcEncryptionInfo64 = 0x2C
	)
	for i := 0; i < ncmds && off+8 <= end; i++ {
		cmd := binary.LittleEndian.Uint32(raw[off : off+4])
		cmdsize := int(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		if cmdsize < 8 || off+cmdsize > end {
			break
		}
		if (cmd == lcEncryptionInfo || cmd == lcEncryptionInfo64) && cmdsize >= 20 {
			cryptID := binary.LittleEndian.Uint32(raw[off+16 : off+20])
			return cryptID != 0, true
		}
		off += cmdsize
	}
	return false, true
}

// ParseMobileProvision extracts embedded XML entitlements & team metadata from iOS embedded.mobileprovision.
func ParseMobileProvision(raw []byte, meta *PackageMetadata) error {
	start := bytes.Index(raw, []byte("<?xml"))
	if start < 0 {
		start = bytes.Index(raw, []byte("<plist"))
	}
	end := bytes.LastIndex(raw, []byte("</plist>"))
	if start < 0 || end <= start {
		return fmt.Errorf("plist payload not found in mobileprovision")
	}
	plistSlice := raw[start : end+len("</plist>")]
	root, err := parseXMLPlist(plistSlice)
	if err != nil {
		return err
	}
	dict, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("mobileprovision root is not dict")
	}
	prov := &ProvisioningSummary{}
	if tn, ok := dict["TeamName"].(string); ok {
		prov.TeamName = strings.TrimSpace(tn)
	}
	if all, ok := dict["ProvisionsAllDevices"].(bool); ok {
		prov.ProvisionsAll = all
	}
	if ents, ok := dict["Entitlements"].(map[string]any); ok {
		if aid, ok := ents["application-identifier"].(string); ok {
			prov.AppID = strings.TrimSpace(aid)
		}
		if gta, ok := ents["get-task-allow"].(bool); ok {
			prov.GetTaskAllow = gta
			if gta {
				meta.Warnings = appendUnique(meta.Warnings, "iOS embedded.mobileprovision sets get-task-allow=true (debugger attachment permitted)")
			}
		}
		if kags, ok := ents["keychain-access-groups"].([]any); ok {
			for _, v := range kags {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					prov.KeychainAccessGroup = appendUnique(prov.KeychainAccessGroup, strings.TrimSpace(s))
				}
			}
		}
		if doms, ok := ents["com.apple.developer.associated-domains"].([]any); ok {
			for _, v := range doms {
				s, ok := v.(string)
				if !ok || strings.TrimSpace(s) == "" {
					continue
				}
				s = strings.TrimSpace(s)
				prov.AssociatedDomains = appendUnique(prov.AssociatedDomains, s)
				// Extract bare domain from applinks:api.example.com / webcredentials:...
				_, hostPart, hasPrefix := strings.Cut(s, ":")
				if !hasPrefix {
					hostPart = s
				}
				if h := sanitizeCandidateHost(hostPart); h != "" {
					meta.CandidateDomains = appendUnique(meta.CandidateDomains, h)
				}
			}
		}
	}
	meta.Provisioning = prov
	return nil
}

// InspectMachOBinary performs pure-Go Mach-O binary hardening and ObjC/Swift endpoint inspection.
func InspectMachOBinary(path string, raw []byte, meta *PackageMetadata) {
	if len(raw) < 32 {
		return
	}
	const (
		machoFlagAllowStackExec = 0x20000
		machoFlagPIE            = 0x200000
	)
	inspectSlice := func(mf *macho.File, enc *bool) BinarySecurityReport {
		rep := BinarySecurityReport{
			Path:              path,
			Format:            "macho",
			Arch:              mf.Cpu.String(),
			PIE:               mf.Flags&machoFlagPIE != 0,
			NX:                mf.Flags&machoFlagAllowStackExec == 0,
			FairPlayEncrypted: enc,
		}
		var arc bool
		if syms, err := mf.ImportedSymbols(); err == nil {
			for _, s := range syms {
				switch s {
				case "_objc_release", "_swift_release":
					arc = true
				case "___stack_chk_fail", "__stack_chk_fail":
					rep.StackCanary = true
				}
			}
		}
		if mf.Symtab != nil {
			for _, s := range mf.Symtab.Syms {
				switch s.Name {
				case "_objc_release", "_swift_release":
					arc = true
				case "___stack_chk_fail", "__stack_chk_fail":
					rep.StackCanary = true
				}
			}
		}
		rep.ARC = &arc
		for _, secName := range []string{"__cstring", "__objc_methname", "__objc_classname"} {
			if sec := mf.Section(secName); sec != nil && sec.Size > 0 && sec.Size <= 16<<20 {
				if data, err := sec.Data(); err == nil && len(data) > 0 {
					scanTextBufferForEndpoints(data, meta)
					extractSBOMHints(data, meta)
				}
			}
		}
		return rep
	}

	var encPtr *bool
	if enc, ok := CheckMachOFairPlayEncrypted(raw); ok {
		encPtr = &enc
	}
	if ff, err := macho.NewFatFile(bytes.NewReader(raw)); err == nil && len(ff.Arches) > 0 {
		defer ff.Close()
		rep := inspectSlice(ff.Arches[0].File, encPtr)
		meta.NativeBinaries = append(meta.NativeBinaries, rep)
		return
	}
	if mf, err := macho.NewFile(bytes.NewReader(raw)); err == nil {
		defer mf.Close()
		meta.NativeBinaries = append(meta.NativeBinaries, inspectSlice(mf, encPtr))
	}
}

func parseXMLPlist(raw []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse xml plist: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "plist" {
			return readXMLPlistNextValue(dec, 0)
		}
	}
}

func readXMLPlistNextValue(dec *xml.Decoder, depth int) (any, error) {
	if depth > maxPlistDepth {
		return nil, fmt.Errorf("xml plist depth exceeded")
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return decodeXMLElement(dec, t.Name.Local, depth)
		case xml.EndElement:
			return nil, io.EOF
		}
	}
}

func decodeXMLElement(dec *xml.Decoder, tag string, depth int) (any, error) {
	switch tag {
	case "dict":
		m := map[string]any{}
		curKey := ""
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					s, err := readXMLText(dec)
					if err != nil {
						return nil, err
					}
					curKey = s
				} else {
					v, err := decodeXMLElement(dec, t.Name.Local, depth+1)
					if err != nil {
						return nil, err
					}
					if curKey != "" {
						m[curKey] = v
						curKey = ""
					}
				}
			case xml.EndElement:
				if t.Name.Local == "dict" {
					return m, nil
				}
			}
		}
	case "array":
		var arr []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := decodeXMLElement(dec, t.Name.Local, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			case xml.EndElement:
				if t.Name.Local == "array" {
					return arr, nil
				}
			}
		}
	case "string", "data", "date", "integer", "real":
		return readXMLText(dec)
	case "true":
		_, _ = readXMLText(dec)
		return true, nil
	case "false":
		_, _ = readXMLText(dec)
		return false, nil
	default:
		return readXMLText(dec)
	}
}

func readXMLText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

// bplist00 minimal decoder implementing Apple binary property list spec.
func parseBinaryPlist(data []byte) (any, error) {
	if len(data) < 40 || string(data[:8]) != bplistMagic {
		return nil, fmt.Errorf("invalid bplist00 header")
	}
	trailer := data[len(data)-32:]
	offsetIntSize := int(trailer[6])
	objectRefSize := int(trailer[7])
	numObjects := binary.BigEndian.Uint64(trailer[8:16])
	topObject := binary.BigEndian.Uint64(trailer[16:24])
	offsetTableOffset := binary.BigEndian.Uint64(trailer[24:32])

	if numObjects == 0 || numObjects > maxBplistObjectCount {
		return nil, fmt.Errorf("unreasonable bplist object count: %d", numObjects)
	}
	if offsetIntSize < 1 || offsetIntSize > 8 || objectRefSize < 1 || objectRefSize > 8 {
		return nil, fmt.Errorf("invalid bplist integer sizes")
	}
	offTableEnd := offsetTableOffset + numObjects*uint64(offsetIntSize)
	if offsetTableOffset >= uint64(len(data)-32) || offTableEnd > uint64(len(data)-32) {
		return nil, fmt.Errorf("corrupt bplist offset table")
	}
	if topObject >= numObjects {
		return nil, fmt.Errorf("topObject out of range")
	}

	offsets := make([]int, numObjects)
	for i := uint64(0); i < numObjects; i++ {
		p := int(offsetTableOffset) + int(i)*offsetIntSize
		offsets[i] = int(readBEUint(data[p : p+offsetIntSize]))
	}

	var decodeObj func(idx uint64, depth int) (any, error)
	decodeObj = func(idx uint64, depth int) (any, error) {
		if depth > maxPlistDepth {
			return nil, fmt.Errorf("bplist recursion limit")
		}
		if idx >= numObjects {
			return nil, fmt.Errorf("bplist ref out of range")
		}
		off := offsets[idx]
		if off < 8 || off >= int(offsetTableOffset) {
			return nil, fmt.Errorf("bplist offset out of bounds")
		}
		marker := data[off]
		objType := marker >> 4
		objInfo := int(marker & 0x0f)

		readCount := func(start int) (int, int, error) {
			if objInfo != 0x0f {
				return objInfo, start, nil
			}
			if start >= len(data) {
				return 0, 0, fmt.Errorf("truncated bplist size int")
			}
			intMarker := data[start]
			if intMarker>>4 != 0x01 {
				return 0, 0, fmt.Errorf("expected int marker for length")
			}
			byteCount := 1 << int(intMarker&0x0f)
			if start+1+byteCount > len(data) || byteCount > 8 {
				return 0, 0, fmt.Errorf("truncated length int bytes")
			}
			return int(readBEUint(data[start+1 : start+1+byteCount])), start + 1 + byteCount, nil
		}

		switch objType {
		case 0x00:
			switch objInfo {
			case 0x08:
				return false, nil
			case 0x09:
				return true, nil
			default:
				return nil, nil
			}
		case 0x01: // int
			n := 1 << objInfo
			if off+1+n > len(data) || n > 8 {
				return nil, fmt.Errorf("truncated bplist int")
			}
			return int64(readBEUint(data[off+1 : off+1+n])), nil
		case 0x02: // real
			n := 1 << objInfo
			if off+1+n > len(data) {
				return nil, fmt.Errorf("truncated bplist real")
			}
			if n == 4 {
				return float64(math.Float32frombits(binary.BigEndian.Uint32(data[off+1 : off+5]))), nil
			}
			if n == 8 {
				return math.Float64frombits(binary.BigEndian.Uint64(data[off+1 : off+9])), nil
			}
			return nil, nil
		case 0x04: // data
			cnt, p, err := readCount(off + 1)
			if err != nil || p+cnt > len(data) {
				return nil, fmt.Errorf("invalid bplist data")
			}
			return append([]byte(nil), data[p:p+cnt]...), nil
		case 0x05: // ASCII string
			cnt, p, err := readCount(off + 1)
			if err != nil || p+cnt > len(data) {
				return nil, fmt.Errorf("invalid bplist ascii string")
			}
			return string(data[p : p+cnt]), nil
		case 0x06: // UTF-16BE string
			cnt, p, err := readCount(off + 1)
			if err != nil || p+cnt*2 > len(data) {
				return nil, fmt.Errorf("invalid bplist utf16 string")
			}
			codes := make([]uint16, cnt)
			for i := 0; i < cnt; i++ {
				codes[i] = binary.BigEndian.Uint16(data[p+i*2 : p+i*2+2])
			}
			return string(utf16.Decode(codes)), nil
		case 0x0a: // array
			cnt, p, err := readCount(off + 1)
			if err != nil || p+cnt*objectRefSize > len(data) {
				return nil, fmt.Errorf("invalid bplist array")
			}
			arr := make([]any, 0, cnt)
			for i := 0; i < cnt; i++ {
				ref := readBEUint(data[p+i*objectRefSize : p+(i+1)*objectRefSize])
				v, err := decodeObj(ref, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			return arr, nil
		case 0x0d: // dict
			cnt, p, err := readCount(off + 1)
			if err != nil || p+cnt*2*objectRefSize > len(data) {
				return nil, fmt.Errorf("invalid bplist dict")
			}
			m := make(map[string]any, cnt)
			valsOff := p + cnt*objectRefSize
			for i := 0; i < cnt; i++ {
				kRef := readBEUint(data[p+i*objectRefSize : p+(i+1)*objectRefSize])
				vRef := readBEUint(data[valsOff+i*objectRefSize : valsOff+(i+1)*objectRefSize])
				kv, err := decodeObj(kRef, depth+1)
				if err != nil {
					return nil, err
				}
				vv, err := decodeObj(vRef, depth+1)
				if err != nil {
					return nil, err
				}
				if ks, ok := kv.(string); ok {
					m[ks] = vv
				}
			}
			return m, nil
		default:
			return nil, nil
		}
	}
	return decodeObj(topObject, 0)
}

func readBEUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = (v << 8) | uint64(c)
	}
	return v
}
