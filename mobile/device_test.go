package mobile

import (
	"context"
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

func TestDeviceListLockAndProxyLifecycle(t *testing.T) {
	mgr := NewManager("")
	var calls []string
	mgr.runner = func(_ context.Context, _ []string, name string, args ...string) (string, string, int, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if len(args) >= 2 && args[0] == "devices" && args[1] == "-l" {
			return "List of devices attached\n192.168.1.50:5555      device product:panther model:Pixel_7 transport_id:2\nemulator-5554          device product:sdk model:Emulator transport_id:1\n", "", 0, nil
		}
		if len(args) >= 4 && args[0] == "-s" && args[2] == "shell" {
			cmd := args[3]
			if strings.Contains(cmd, "echo MODEL=") {
				return "MODEL=Pixel 7\nMANUF=Google\nREL=14\nSDK=34\nABI=arm64-v8a\nPROXY=:0\nROOT=su\nCA=system_apex\nFRIDA=running:4321\nTOP=com.example.app\n", "", 0, nil
			}
			if cmd == "settings get global http_proxy" {
				return "10.0.0.2:8080\n", "", 0, nil
			}
		}
		return "ok", "", 0, nil
	}

	ctx := context.Background()
	devs, err := mgr.ListDevices(ctx, nil)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2", len(devs))
	}
	wifi := devs[0] // sorted: 192.168.1.50:5555 < emulator-5554
	if wifi.ConnectionType != ConnWiFi || !wifi.Rooted || wifi.RootMethod != "su" || wifi.FridaPID != 4321 || wifi.APILevel != 34 {
		t.Fatalf("unexpected wifi device metadata: %+v", wifi)
	}
	usb := devs[1]
	if usb.ConnectionType != ConnUSB || usb.Serial != "emulator-5554" {
		t.Fatalf("unexpected usb device metadata: %+v", usb)
	}

	// Exclusive lock tests.
	if err := mgr.BindTask(10, wifi.Serial); err != nil {
		t.Fatalf("BindTask 10: %v", err)
	}
	if err := mgr.BindTask(11, wifi.Serial); err == nil {
		t.Fatal("expected busy lock error for task 11")
	}
	if freed := mgr.UnbindTask(10); freed != wifi.Serial {
		t.Fatalf("UnbindTask freed %q, want %q", freed, wifi.Serial)
	}

	// SetupProxy + ClearProxy lifecycle.
	proxyAddr, err := mgr.SetupProxy(ctx, wifi.Serial, "usb_reverse", 9899, "")
	if err != nil || proxyAddr != "127.0.0.1:9899" {
		t.Fatalf("SetupProxy: addr=%q err=%v", proxyAddr, err)
	}
	if err := mgr.ClearProxy(ctx, wifi.Serial); err != nil {
		t.Fatalf("ClearProxy: %v", err)
	}
	sawRestore := false
	for _, c := range calls {
		if strings.Contains(c, "reverse tcp:9899 tcp:9899") || strings.Contains(c, "settings put global http_proxy 10.0.0.2:8080") {
			sawRestore = sawRestore || strings.Contains(c, "10.0.0.2:8080")
		}
	}
	if !sawRestore {
		t.Errorf("expected ClearProxy to restore original proxy 10.0.0.2:8080, calls=%v", calls)
	}
}

func TestBuildIPCProbeArgsAndValidation(t *testing.T) {
	if _, _, err := BuildIPCProbeArgs(IPCProbeRequest{
		Kind:      "activity",
		Package:   "com.example.app",
		Component: ".MainActivity; rm -rf /",
	}); err == nil {
		t.Fatal("expected shell injection characters to be rejected")
	}
	args, _, err := BuildIPCProbeArgs(IPCProbeRequest{
		Kind:    "deeplink",
		Package: "com.example.app",
		URI:     "myapp://pay/callback?order=1",
		Extras:  map[string]string{"token": "abc"},
	})
	if err != nil {
		t.Fatalf("valid deeplink probe rejected: %v", err)
	}
	if !slices.Contains(args, "myapp://pay/callback?order=1") || !slices.Contains(args, "--es") {
		t.Errorf("unexpected args: %v", args)
	}
	if err := ValidatePackageName("com.example;reboot"); err == nil {
		t.Fatal("expected invalid package name rejection")
	}
}

func TestParseBinaryPlist(t *testing.T) {
	// Construct minimal bplist00: dict { "CFBundleIdentifier": "com.bin.test" }
	// Objects: 0=dict(1 key/val: k=1, v=2), 1=ascii "CFBundleIdentifier", 2=ascii "com.bin.test"
	var buf []byte
	buf = append(buf, []byte("bplist00")...)
	off0 := len(buf)
	buf = append(buf, 0xd1, 0x01, 0x02) // dict len=1, key ref 1, val ref 2

	writeASCII := func(s string) int {
		off := len(buf)
		if len(s) < 15 {
			buf = append(buf, byte(0x50|len(s)))
		} else {
			buf = append(buf, 0x5f, 0x10, byte(len(s)))
		}
		buf = append(buf, []byte(s)...)
		return off
	}
	off1 := writeASCII("CFBundleIdentifier")
	off2 := writeASCII("com.bin.test")

	offTable := len(buf)
	buf = append(buf, byte(off0), byte(off1), byte(off2))

	trailer := make([]byte, 32)
	trailer[6] = 1 // offsetIntSize
	trailer[7] = 1 // objectRefSize
	binary.BigEndian.PutUint64(trailer[8:16], 3)
	binary.BigEndian.PutUint64(trailer[16:24], 0)
	binary.BigEndian.PutUint64(trailer[24:32], uint64(offTable))
	buf = append(buf, trailer...)

	var meta PackageMetadata
	if err := ParseInfoPlist(buf, &meta); err != nil {
		t.Fatalf("ParseInfoPlist(bplist): %v", err)
	}
	if meta.PackageName != "com.bin.test" {
		t.Fatalf("got package %q, want com.bin.test", meta.PackageName)
	}
}
