package mobile

import "time"

// Platform identifies the mobile operating system of a package or device.
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
)

// ConnectionType describes how BLADE talks to an attached mobile device.
type ConnectionType string

const (
	ConnUSB  ConnectionType = "usb"
	ConnWiFi ConnectionType = "wifi"
)

// PermissionSummary describes a permission declared in an Android manifest.
type PermissionSummary struct {
	Name      string `json:"name"`
	Dangerous bool   `json:"dangerous,omitempty"`
}

// IntentFilter describes an intent filter extracted from an Android component.
type IntentFilter struct {
	Actions    []string `json:"actions,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Schemes    []string `json:"schemes,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
	PathPrefix string   `json:"path_prefix,omitempty"`
}

// ComponentSummary represents an Android Activity, Service, Receiver, or Provider.
type ComponentSummary struct {
	Type        string         `json:"type"` // activity | service | receiver | provider
	Name        string         `json:"name"`
	Exported    bool           `json:"exported"`
	Implicit    bool           `json:"implicit_exported,omitempty"`
	Permission  string         `json:"permission,omitempty"`
	Authorities string         `json:"authorities,omitempty"`
	Filters     []IntentFilter `json:"filters,omitempty"`
}

// ATSSummary summarizes iOS App Transport Security configuration.
type ATSSummary struct {
	AllowsArbitraryLoads bool     `json:"allows_arbitrary_loads"`
	ExceptionDomains     []string `json:"exception_domains,omitempty"`
}

// PackageMetadata holds static inspection metadata extracted from APK/AAB/XAPK/IPA files.
type PackageMetadata struct {
	SampleName          string              `json:"sample_name"`
	SamplePath          string              `json:"sample_path,omitempty"`
	SHA256              string              `json:"sha256"`
	SizeBytes           int64               `json:"size_bytes"`
	Platform            Platform            `json:"platform"`
	FileType            string              `json:"file_type"` // apk | aab | xapk | ipa
	PackageName         string              `json:"package_name"`
	AppName             string              `json:"app_name,omitempty"`
	VersionName         string              `json:"version_name,omitempty"`
	VersionCode         string              `json:"version_code,omitempty"`
	MinSDK              int                 `json:"min_sdk,omitempty"`
	TargetSDK           int                 `json:"target_sdk,omitempty"`
	Debuggable          bool                `json:"debuggable"`
	AllowBackup         bool                `json:"allow_backup"`
	CleartextTraffic    *bool               `json:"cleartext_traffic,omitempty"`
	NetworkSecurityCfg  string              `json:"network_security_config,omitempty"`
	FairPlayEncrypted   *bool               `json:"fairplay_encrypted,omitempty"` // iOS Mach-O cryptid > 0
	ATS                 *ATSSummary         `json:"ats,omitempty"`
	Frameworks          []string            `json:"frameworks,omitempty"`
	Packers             []string            `json:"packers,omitempty"`
	NativeABIs          []string            `json:"native_abis,omitempty"`
	Permissions         []PermissionSummary `json:"permissions,omitempty"`
	ExportedComponents  []ComponentSummary  `json:"exported_components,omitempty"`
	URLSchemes          []string            `json:"url_schemes,omitempty"`
	CandidateDomains    []string            `json:"candidate_domains,omitempty"`
	CandidateURLs       []string            `json:"candidate_urls,omitempty"`
	ThirdPartySDKHosts  []string            `json:"third_party_sdk_hosts,omitempty"`
	HardcodedHints      []string            `json:"hardcoded_hints,omitempty"`
	Warnings            []string            `json:"warnings,omitempty"`
	InspectedAtUnix     int64               `json:"inspected_at_unix"`
	SplitAPKNames       []string            `json:"split_apk_names,omitempty"`
	NetworkSecTrustUser bool                `json:"network_sec_trust_user,omitempty"`
}

// DeviceInfo describes an attached or saved mobile test device.
type DeviceInfo struct {
	ID             string         `json:"id"` // stable id: adb:<serial>
	Serial         string         `json:"serial"`
	Platform       Platform       `json:"platform"`
	ConnectionType ConnectionType `json:"connection_type"`
	Endpoint       string         `json:"endpoint,omitempty"` // ip:port for wifi
	Model          string         `json:"model,omitempty"`
	Manufacturer   string         `json:"manufacturer,omitempty"`
	OSVersion      string         `json:"os_version,omitempty"`
	APILevel       int            `json:"api_level,omitempty"`
	ABI            string         `json:"abi,omitempty"`
	State          string         `json:"state"` // online | offline | unauthorized | disconnected
	Rooted         bool           `json:"rooted"`
	RootMethod     string         `json:"root_method,omitempty"`
	SystemCAStatus string         `json:"system_ca_status,omitempty"` // none | user | system_apex | system_legacy
	FridaServer    string         `json:"frida_server,omitempty"`     // missing | stopped | running
	FridaPID       int            `json:"frida_pid,omitempty"`
	ProxyMode      string         `json:"proxy_mode,omitempty"` // none | usb_reverse | lan
	ProxyAddr      string         `json:"proxy_addr,omitempty"`
	ForegroundPkg  string         `json:"foreground_pkg,omitempty"`
	LockedByTaskID int64          `json:"locked_by_task_id,omitempty"`
	LastSeenAt     time.Time      `json:"last_seen_at"`
}

// UIElement represents an interactive or readable element parsed from Android UI hierarchy XML.
type UIElement struct {
	Index       int    `json:"index"`
	Class       string `json:"class"`
	Text        string `json:"text,omitempty"`
	ContentDesc string `json:"content_desc,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
	Clickable   bool   `json:"clickable,omitempty"`
	Enabled     bool   `json:"enabled"`
	Editable    bool   `json:"editable,omitempty"`
	Password    bool   `json:"password,omitempty"`
	Bounds      string `json:"bounds"`
	CenterX     int    `json:"center_x"`
	CenterY     int    `json:"center_y"`
}

// UIDumpResult contains a compact parsed UI tree for an Android screen.
type UIDumpResult struct {
	Serial        string      `json:"serial"`
	ForegroundPkg string      `json:"foreground_pkg,omitempty"`
	ElementCount  int         `json:"element_count"`
	Elements      []UIElement `json:"elements"`
	RawTruncated  bool        `json:"raw_truncated,omitempty"`
}

// UIActionRequest triggers a UI automation event on a bound Android device.
type UIActionRequest struct {
	Action   string `json:"action"` // tap | input | swipe | back | home | keyevent | launch | deeplink | force_stop
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
	X2       int    `json:"x2,omitempty"`
	Y2       int    `json:"y2,omitempty"`
	Text     string `json:"text,omitempty"`
	KeyCode  int    `json:"key_code,omitempty"`
	Package  string `json:"package,omitempty"`
	Activity string `json:"activity,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// FridaScriptSpec describes a built-in standardized Frida assessment hook module.
type FridaScriptSpec struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"` // ssl | bypass | crypto | network | webview
	Description string   `json:"description"`
	Frameworks  []string `json:"frameworks,omitempty"`
	Source      string   `json:"source"`
}

// FridaSession records an active or historical Frida hook session managed by BLADE.
type FridaSession struct {
	ID        string    `json:"id"`
	TaskID    int64     `json:"task_id,omitempty"`
	Serial    string    `json:"serial"`
	Package   string    `json:"package"`
	Mode      string    `json:"mode"` // spawn | attach
	Modules   []string  `json:"modules"`
	Running   bool      `json:"running"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	ExitError string    `json:"exit_error,omitempty"`
	LogCount  int       `json:"log_count"`
	LastLogs  []string  `json:"last_logs,omitempty"`
}

// IPCProbeRequest tests an exported Android component or deep link.
type IPCProbeRequest struct {
	Kind      string            `json:"kind"` // activity | deeplink | broadcast | provider
	Package   string            `json:"package"`
	Component string            `json:"component,omitempty"`
	URI       string            `json:"uri,omitempty"`
	Action    string            `json:"action,omitempty"`
	Extras    map[string]string `json:"extras,omitempty"`
}

// IPCProbeResult returns the structured outcome of an IPC probe command.
type IPCProbeResult struct {
	Kind     string `json:"kind"`
	Command  string `json:"command"`
	Success  bool   `json:"success"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	CrashLog string `json:"crash_log,omitempty"`
}

// SandboxFileEntry describes a file or directory inside a rooted Android app sandbox.
type SandboxFileEntry struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size,omitempty"`
	Preview string `json:"preview,omitempty"`
}
