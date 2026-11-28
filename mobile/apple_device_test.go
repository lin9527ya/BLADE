package mobile

import (
	"context"
	"strings"
	"testing"
)

func TestDiscoverIOSDevicesAndPortTunnelHelper(t *testing.T) {
	mgr := NewManager("")
	mgr.runner = func(_ context.Context, _ []string, _ string, args ...string) (string, string, int, error) {
		if len(args) >= 2 && args[0] == "devices" && args[1] == "-l" {
			return "List of devices attached\n", "", 0, nil
		}
		return "", "", 0, nil
	}
	mgr.iosRunner = func(_ context.Context, _ []string, name string, args ...string) (string, string, int, error) {
		switch name {
		case "idevice_id":
			return "00008110-001234567890001E\n", "", 0, nil
		case "ideviceinfo":
			return "DeviceName: Lab iPhone\nProductType: iPhone14,5\nProductVersion: 16.5\nCPUArchitecture: arm64e\n", "", 0, nil
		}
		return "", "", 1, nil
	}

	devs, err := mgr.ListDevices(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListDevices failed: %v", err)
	}
	if len(devs) != 1 || devs[0].Platform != PlatformIOS || devs[0].OSVersion != "16.5" || devs[0].ABI != "arm64e" {
		t.Fatalf("expected 1 iOS device, got %+v", devs)
	}

	cmdDesc, err := CheckIOSPortTunnel(context.Background(), mgr.iosRunner, devs[0].Serial, 27042, 27042)
	if err != nil || !strings.Contains(cmdDesc, "iproxy 27042:27042 -u 00008110-001234567890001E") {
		t.Fatalf("unexpected CheckIOSPortTunnel result: cmd=%q err=%v", cmdDesc, err)
	}
}
