package mobile

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reIOSUDID = regexp.MustCompile(`^[0-9A-Fa-f-]{20,64}$`)

// DiscoverIOSDevices inspects USB-connected iOS devices via libimobiledevice (`idevice_id` & `ideviceinfo`).
// Returns nil without error when libimobiledevice is absent or no iOS devices are attached.
func (m *Manager) discoverIOSDevices(ctx context.Context) []DeviceInfo {
	if m == nil {
		return nil
	}
	runner := m.iosRunner
	if runner == nil {
		// Only auto-probe live USB iOS hardware when idevice_id exists and standard host runner is active.
		if _, err := exec.LookPath("idevice_id"); err != nil {
			return nil
		}
		runner = defaultCommandRunner
	}

	subCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, _, code, err := runner(subCtx, nil, "idevice_id", "-l")
	if err != nil || code != 0 {
		return nil
	}

	var devices []DeviceInfo
	seen := map[string]bool{}
	for _, raw := range strings.Split(out, "\n") {
		udid := strings.TrimSpace(raw)
		if !reIOSUDID.MatchString(udid) || seen[udid] {
			continue
		}
		seen[udid] = true
		dev := DeviceInfo{
			ID:             "ios:" + udid,
			Serial:         udid,
			Platform:       PlatformIOS,
			ConnectionType: ConnUSB,
			State:          "online",
			ProxyMode:      "none",
			LastSeenAt:     time.Now().UTC(),
		}
		m.mu.Lock()
		dev.LockedByTaskID = m.deviceLocks[udid]
		if pm := m.activeProxy[udid]; pm != "" {
			dev.ProxyMode = pm
		}
		m.mu.Unlock()

		infoCtx, icancel := context.WithTimeout(ctx, 3*time.Second)
		infoOut, _, infoCode, infoErr := runner(infoCtx, nil, "ideviceinfo", "-u", udid)
		icancel()
		if infoErr == nil && infoCode == 0 {
			parseIOSDeviceInfo(infoOut, &dev)
		}
		devices = append(devices, dev)
	}
	return devices
}

func parseIOSDeviceInfo(raw string, dev *DeviceInfo) {
	dev.Manufacturer = "Apple"
	for _, ln := range strings.Split(raw, "\n") {
		k, v, has := strings.Cut(ln, ":")
		if !has {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		switch key {
		case "DeviceName":
			if dev.Model == "" {
				dev.Model = val
			}
		case "ProductType":
			dev.Model = val
		case "ProductVersion":
			dev.OSVersion = val
		case "CPUArchitecture":
			dev.ABI = val
		}
	}
}

// CheckIOSPortTunnel verifies or launches a local usbmuxd port mapping via `iproxy` (e.g. 27042 for Frida or 22 for SSH).
func CheckIOSPortTunnel(ctx context.Context, runner ADBRunner, udid string, localPort, devicePort int) (string, error) {
	if !reIOSUDID.MatchString(strings.TrimSpace(udid)) {
		return "", fmt.Errorf("invalid iOS UDID %q", udid)
	}
	if localPort <= 0 || localPort > 65535 || devicePort <= 0 || devicePort > 65535 {
		return "", fmt.Errorf("invalid port range (%d -> %d)", localPort, devicePort)
	}
	if runner == nil {
		if _, err := exec.LookPath("iproxy"); err != nil {
			return "", fmt.Errorf("iproxy unavailable (install libimobiledevice-utils)")
		}
	}
	return fmt.Sprintf("iproxy %s:%s -u %s", strconv.Itoa(localPort), strconv.Itoa(devicePort), udid), nil
}
