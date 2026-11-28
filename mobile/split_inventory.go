package mobile

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InspectUploadedPackage includes manifest-only inventory for an installed
// APK set. A successful inventory does not imply source analysis coverage.
func InspectUploadedPackage(path, hint string) (*PackageMetadata, error) {
	meta, err := InspectPackageWithHint(path, hint)
	if err == nil || !strings.EqualFold(filepath.Ext(path), ".apks") {
		return meta, err
	}
	outer, e := zip.OpenReader(path)
	if e != nil {
		return nil, e
	}
	defer outer.Close()
	var base *zip.File
	var splits []string
	for _, f := range outer.File {
		if !isSafeZipEntryName(f.Name) || !strings.HasSuffix(strings.ToLower(f.Name), ".apk") {
			continue
		}
		splits = append(splits, f.Name)
		switch strings.ToLower(filepath.Base(f.Name)) {
		case "base.apk", "base-master.apk", "universal.apk":
			if base != nil {
				return nil, fmt.Errorf("分包包含多个候选基础包，请选择单个基础包核对")
			}
			base = f
		}
	}
	if base == nil {
		return nil, fmt.Errorf("未识别 APKS 基础包布局")
	}
	if base.UncompressedSize64 > 128<<20 {
		return nil, fmt.Errorf("基础包超过清单读取上限")
	}
	raw, e := readBoundedZipFile(base, 128<<20)
	if e != nil {
		return nil, e
	}
	nested, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		return nil, e
	}
	for _, f := range nested.File {
		if !strings.EqualFold(f.Name, "AndroidManifest.xml") {
			continue
		}
		if f.UncompressedSize64 > maxAXMLDecodeBytes {
			return nil, fmt.Errorf("基础包清单超过读取上限")
		}
		data, e := readBoundedZipFile(f, maxAXMLDecodeBytes)
		if e != nil {
			return nil, e
		}
		var basic PackageMetadata
		if e = ParseAndroidManifest(data, &basic); e != nil {
			return nil, e
		}
		sha, e := SampleSHA256(path)
		if e != nil {
			return nil, e
		}
		stat, e := os.Stat(path)
		if e != nil {
			return nil, e
		}
		return &PackageMetadata{Platform: PlatformAndroid, FileType: "apks", PackageName: basic.PackageName, VersionName: basic.VersionName, VersionCode: basic.VersionCode, AppName: basic.AppName, MinSDK: basic.MinSDK, TargetSDK: basic.TargetSDK, SampleName: filepath.Base(path), SHA256: sha, SizeBytes: stat.Size(), InspectedAtUnix: time.Now().Unix(), SplitAPKNames: splits,
			Checks: []InspectionStage{{Name: "split_base_manifest", Status: "complete", Checked: 1, Total: 1}, {Name: "source_output", Status: "not_checked", Note: "此分包布局仅提供基础清单信息"}}}, nil
	}
	return nil, fmt.Errorf("基础包中没有可读的 AndroidManifest.xml")
}
