package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lin9527ya/blade/mobile"
)

// MobileDeviceRecord represents a persisted mobile device entry in PostgreSQL.
type MobileDeviceRecord struct {
	ID             string    `json:"id"`
	Serial         string    `json:"serial"`
	Platform       string    `json:"platform"`
	ConnectionType string    `json:"connection_type"`
	Endpoint       string    `json:"endpoint,omitempty"`
	Model          string    `json:"model,omitempty"`
	OSVersion      string    `json:"os_version,omitempty"`
	APILevel       int       `json:"api_level,omitempty"`
	ABI            string    `json:"abi,omitempty"`
	Rooted         bool      `json:"rooted"`
	RootMethod     string    `json:"root_method,omitempty"`
	SystemCAStatus string    `json:"system_ca_status,omitempty"`
	FridaServer    string    `json:"frida_server,omitempty"`
	ProxyMode      string    `json:"proxy_mode,omitempty"`
	BoundTaskID    *int64    `json:"bound_task_id,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

// TaskMobileConfig summarizes the mobile sample and device attachment for a task.
type TaskMobileConfig struct {
	TaskID          int64           `json:"task_id"`
	AppMode         string          `json:"app_mode"`
	AppPlatform     string          `json:"app_platform,omitempty"`
	AppPackageName  string          `json:"app_package_name,omitempty"`
	AppSamplePaths  []string        `json:"app_sample_paths,omitempty"`
	AppManifestJSON json.RawMessage `json:"app_manifest_json,omitempty"`
	MobileDeviceID  string          `json:"mobile_device_id,omitempty"`
	MobileProxyMode string          `json:"mobile_proxy_mode,omitempty"`
}

// UpsertMobileDevice persists or updates discovered ADB/mobile device metadata.
func (d *DB) UpsertMobileDevice(dev mobile.DeviceInfo) error {
	if strings.TrimSpace(dev.Serial) == "" {
		return nil
	}
	id := dev.ID
	if id == "" {
		id = "adb:" + dev.Serial
	}
	platform := string(dev.Platform)
	if platform == "" {
		platform = "android"
	}
	conn := string(dev.ConnectionType)
	if conn == "" {
		conn = "usb"
	}
	_, err := d.Exec(`
INSERT INTO mobile_devices(
    id, serial, platform, connection_type, endpoint, model, os_version, api_level, abi,
    rooted, root_method, system_ca_status, frida_server, proxy_mode, last_seen_at
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())
ON CONFLICT (serial) DO UPDATE SET
    platform         = EXCLUDED.platform,
    connection_type  = EXCLUDED.connection_type,
    endpoint         = COALESCE(NULLIF(EXCLUDED.endpoint,''), mobile_devices.endpoint),
    model            = COALESCE(NULLIF(EXCLUDED.model,''), mobile_devices.model),
    os_version       = COALESCE(NULLIF(EXCLUDED.os_version,''), mobile_devices.os_version),
    api_level        = CASE WHEN EXCLUDED.api_level > 0 THEN EXCLUDED.api_level ELSE mobile_devices.api_level END,
    abi              = COALESCE(NULLIF(EXCLUDED.abi,''), mobile_devices.abi),
    rooted           = EXCLUDED.rooted,
    root_method      = EXCLUDED.root_method,
    system_ca_status = EXCLUDED.system_ca_status,
    frida_server     = EXCLUDED.frida_server,
    proxy_mode       = EXCLUDED.proxy_mode,
    last_seen_at     = now()`,
		id, dev.Serial, platform, conn, dev.Endpoint, dev.Model, dev.OSVersion, dev.APILevel, dev.ABI,
		dev.Rooted, dev.RootMethod, dev.SystemCAStatus, dev.FridaServer, dev.ProxyMode)
	return err
}

// ListMobileDevices returns all known devices ordered by latest activity.
func (d *DB) ListMobileDevices() ([]MobileDeviceRecord, error) {
	rows, err := d.Query(`
SELECT id, serial, platform, connection_type, endpoint, model, os_version, api_level, abi,
       rooted, root_method, system_ca_status, frida_server, proxy_mode, bound_task_id, last_seen_at
FROM mobile_devices
ORDER BY last_seen_at DESC, serial ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MobileDeviceRecord
	for rows.Next() {
		var r MobileDeviceRecord
		var bound sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Serial, &r.Platform, &r.ConnectionType, &r.Endpoint, &r.Model,
			&r.OSVersion, &r.APILevel, &r.ABI, &r.Rooted, &r.RootMethod, &r.SystemCAStatus,
			&r.FridaServer, &r.ProxyMode, &bound, &r.LastSeenAt); err != nil {
			return nil, err
		}
		if bound.Valid {
			v := bound.Int64
			r.BoundTaskID = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteMobileDevice removes a saved device row when not bound or after unbinding.
func (d *DB) DeleteMobileDevice(idOrSerial string) error {
	s := strings.TrimPrefix(strings.TrimSpace(idOrSerial), "adb:")
	if s == "" {
		return nil
	}
	_, err := d.Exec(`UPDATE tasks SET mobile_device_id='' WHERE mobile_device_id=$1`, s)
	if err != nil {
		return err
	}
	_, err = d.Exec(`DELETE FROM mobile_devices WHERE serial=$1 OR id=$2`, s, idOrSerial)
	return err
}

// BindTaskMobileDevice exclusively assigns deviceSerial to taskID.
func (d *DB) BindTaskMobileDevice(taskID int64, serial, proxyMode string) error {
	if taskID <= 0 {
		return fmt.Errorf("invalid task id %d", taskID)
	}
	serial = strings.TrimPrefix(strings.TrimSpace(serial), "adb:")
	if serial == "" {
		return fmt.Errorf("device serial required")
	}
	proxyMode = normalizeMobileProxyMode(proxyMode)

	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Ensure device record exists (e.g. direct ADB attach before first poll).
	if _, err := tx.Exec(`
INSERT INTO mobile_devices(id, serial, platform, connection_type, last_seen_at)
VALUES ($1, $2, 'android', CASE WHEN $2 LIKE '%:%' THEN 'wifi' ELSE 'usb' END, now())
ON CONFLICT (serial) DO NOTHING`, "adb:"+serial, serial); err != nil {
		return err
	}

	var existingOwner sql.NullInt64
	if err := tx.QueryRow(`SELECT bound_task_id FROM mobile_devices WHERE serial=$1 FOR UPDATE`, serial).Scan(&existingOwner); err != nil && err != sql.ErrNoRows {
		return err
	}
	if existingOwner.Valid && existingOwner.Int64 != taskID {
		var alive bool
		_ = tx.QueryRow(`SELECT status NOT IN `+TerminalStatusSQL()+` AND deleted_at IS NULL FROM tasks WHERE id=$1`, existingOwner.Int64).Scan(&alive)
		if alive {
			return fmt.Errorf("设备 %s 已被任务 #%d 占用", serial, existingOwner.Int64)
		}
	}

	// Clear any prior binding held by this task or expired binding on this serial.
	if _, err := tx.Exec(`UPDATE mobile_devices SET bound_task_id=NULL WHERE bound_task_id=$1 AND serial<>$2`, taskID, serial); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE mobile_devices SET bound_task_id=$1, updated_at=now() WHERE serial=$2`, taskID, serial); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE tasks SET mobile_device_id=$2, mobile_proxy_mode=$3 WHERE id=$1`, taskID, serial, proxyMode); err != nil {
		return err
	}
	return tx.Commit()
}

// UnbindTaskMobileDevice detaches any mobile device from taskID and returns the detached serial.
func (d *DB) UnbindTaskMobileDevice(taskID int64) (string, error) {
	if taskID <= 0 {
		return "", nil
	}
	tx, err := d.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var serial string
	_ = tx.QueryRow(`SELECT COALESCE(mobile_device_id,'') FROM tasks WHERE id=$1`, taskID).Scan(&serial)
	if _, err := tx.Exec(`UPDATE tasks SET mobile_device_id='' WHERE id=$1`, taskID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE mobile_devices SET bound_task_id=NULL WHERE bound_task_id=$1`, taskID); err != nil {
		return "", err
	}
	return serial, tx.Commit()
}

// UpdateTaskAppMetadata saves the adopted sample paths and inspected static manifest for a task.
func (d *DB) UpdateTaskAppMetadata(taskID int64, samplePaths []string, manifestJSON json.RawMessage, platform, pkgName string) error {
	if len(manifestJSON) == 0 {
		manifestJSON = json.RawMessage(`{}`)
	}
	pathsRaw, err := json.Marshal(samplePaths)
	if err != nil {
		pathsRaw = []byte("[]")
	}
	_, err = d.Exec(`
UPDATE tasks
SET app_mode='mobile',
    app_platform=COALESCE(NULLIF($2,''), app_platform),
    app_package_name=COALESCE(NULLIF($3,''), app_package_name),
    app_sample_paths=$4::jsonb,
    app_manifest_json=$5::jsonb
WHERE id=$1 AND deleted_at IS NULL`,
		taskID, strings.TrimSpace(platform), strings.TrimSpace(pkgName), string(pathsRaw), string(manifestJSON))
	return err
}

// TaskMobileInfo returns mobile sample & bound device config for a task. Errors/not-found return zero config.
func (d *DB) TaskMobileInfo(taskID int64) (TaskMobileConfig, error) {
	cfg := TaskMobileConfig{TaskID: taskID, AppMode: "standard", MobileProxyMode: "usb_reverse"}
	if d == nil || taskID <= 0 {
		return cfg, nil
	}
	var pathsRaw, manRaw []byte
	err := d.QueryRow(`
SELECT COALESCE(app_mode,'standard'), COALESCE(app_platform,''), COALESCE(app_package_name,''),
       COALESCE(app_sample_paths,'[]'::jsonb), COALESCE(app_manifest_json,'{}'::jsonb),
       COALESCE(mobile_device_id,''), COALESCE(mobile_proxy_mode,'usb_reverse')
FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(
		&cfg.AppMode, &cfg.AppPlatform, &cfg.AppPackageName, &pathsRaw, &manRaw, &cfg.MobileDeviceID, &cfg.MobileProxyMode,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return cfg, nil
		}
		return cfg, err
	}
	if len(pathsRaw) > 0 {
		_ = json.Unmarshal(pathsRaw, &cfg.AppSamplePaths)
	}
	if len(manRaw) > 0 {
		cfg.AppManifestJSON = json.RawMessage(manRaw)
	}
	return cfg, nil
}

// TaskHasMobileDevice reports whether a task currently binds an active mobile device.
func (d *DB) TaskHasMobileDevice(taskID int64) bool {
	if d == nil || taskID <= 0 {
		return false
	}
	var dev string
	if err := d.QueryRow(`SELECT COALESCE(mobile_device_id,'') FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&dev); err != nil {
		return false
	}
	return strings.TrimSpace(dev) != ""
}

// TaskHasMobileApp reports whether a task targets a mobile APP (app_mode=mobile, uploaded samples,
// resolved package name, or bound mobile device).
func (d *DB) TaskHasMobileApp(taskID int64) bool {
	if d == nil || taskID <= 0 {
		return false
	}
	var has bool
	err := d.QueryRow(`
SELECT (
	COALESCE(app_mode,'standard') = 'mobile'
	OR COALESCE(app_package_name,'') <> ''
	OR jsonb_array_length(COALESCE(app_sample_paths,'[]'::jsonb)) > 0
	OR COALESCE(mobile_device_id,'') <> ''
)
FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&has)
	return err == nil && has
}

func normalizeMobileProxyMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "lan", "none", "usb_reverse":
		return strings.ToLower(strings.TrimSpace(m))
	default:
		return "usb_reverse"
	}
}
