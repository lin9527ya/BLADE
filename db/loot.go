package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// 凭据的可利用面（P36）：把 credential 节点从「在手/掉了」做成**能据此行动**的东西。
//
// 此前一条凭据只知道"我握着 wp_user 的口令"，不知道**它能登哪、验证过没有**——规划层于是只能
// 派一条"拿它去试试"的意图，试完的结论又只能压进 fact 文本。这一层补三样：
//
//	能登哪：targets[]（每一条目标带协议/端口，以及**在这条目标上**是否实测登入成功）
//	来源与置信：evidence / confidence（已有）+ 每条目标的验证证据
//	脱敏：对外（报告/概览/界面）只印指纹，明文只在受控入口按需取，且那一次留痕
//
// 为什么不给节点加一个顶层 verified：验证是**对某条目标**的结论，不是对凭据本体的结论——
// 同一条口令在 A 机器能登、在 B 机器不能，是很常见的事。顶层那个布尔迟早会和 targets 打架。

// CredentialTarget 是一条「这条凭据能登哪」的记录。
type CredentialTarget struct {
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	Service  string `json:"service,omitempty"`  // 什么服务：ssh / rdp / smb / winrm / mysql / http…
	Protocol string `json:"protocol,omitempty"` // 怎么连（可与 service 不同：smb 走 445/tcp）
	// Verified 是**在这条目标上**实测登入成功过。它比 confidence 硬：confidence 说的是
	// "我多确定读到的是真凭据"，verified 说的是"我用它真的进去了"。
	Verified bool   `json:"verified,omitempty"`
	Evidence string `json:"evidence,omitempty"` // 验证的那一行输出（verified=true 时必填）
	Note     string `json:"note,omitempty"`
}

// CredentialView 是凭据 payload 的读模型（报告、概览、受控入口共用一份解析）。
type CredentialView struct {
	Summary    string             `json:"summary"`
	Detail     string             `json:"detail"`
	Evidence   string             `json:"evidence"`
	Confidence string             `json:"confidence"`
	Kind       string             `json:"kind"`
	Username   string             `json:"username"`
	Secret     string             `json:"secret"`
	Targets    []CredentialTarget `json:"targets,omitempty"`
}

// ParseCredential 解析一条凭据节点的 payload。畸形 payload 一律当作空字段，不报错——
// 读路径不该因为一条脏数据整段失败（同 NodeTechniques）。
func ParseCredential(n *Node) CredentialView {
	var v CredentialView
	if n == nil || len(n.Payload) == 0 {
		return v
	}
	_ = json.Unmarshal(n.Payload, &v)
	return v
}

// Fingerprint 是这条凭据的短指纹（脱敏展示用）。
func (v CredentialView) Fingerprint() string {
	return CredentialFingerprint(v.Kind, v.Username, v.Secret)
}

// VerifiedTargets 列出实测登入成功过的目标。
func (v CredentialView) VerifiedTargets() []CredentialTarget {
	var out []CredentialTarget
	for _, t := range v.Targets {
		if t.Verified {
			out = append(out, t)
		}
	}
	return out
}

// TargetLabel 渲染一条目标（报告与界面共用同一套写法）。
func (t CredentialTarget) Label() string {
	host := strings.TrimSpace(t.Host)
	if t.Port > 0 {
		host += ":" + itoa(int64(t.Port))
	}
	svc := strings.TrimSpace(t.Service)
	if svc == "" {
		return host
	}
	return host + "（" + svc + "）"
}

// CredentialFingerprint 给凭据算一个可对外印的短指纹：sha256(kind|username|secret) 前 8 位十六进制。
//
// 它要解决的是一个具体的两难：报告里不能印明文，但"这条凭据"必须在**多份报告、界面、对话**
// 之间可指认——否则「wp_user 那条」在有两套 wp_user 口令时说不清是哪一条。指纹是单向的
// （8 位十六进制不可反推），但对同一条凭据稳定，于是它既能指认、又不外流。
//
// 空 secret 返回空串：没有本体的记录（比如只登记了"存在这么一条"）不该有一个看起来像指纹的东西。
func CredentialFingerprint(kind, username, secret string) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(kind)) + "\x00" +
		strings.TrimSpace(username) + "\x00" + secret))
	return hex.EncodeToString(h[:])[:8]
}

// MergeCredentialTarget 把一条新目标并进已有列表：同 (host, port, service) 视为同一条，
// 新信息覆盖旧的（验证证据、备注），**已验证过就不会被未验证的记录抹掉**——
// "我上次真的进去了"是既成事实，不该因为这次没试成而消失。
func MergeCredentialTarget(list []CredentialTarget, add CredentialTarget) []CredentialTarget {
	add.Host = strings.TrimSpace(add.Host)
	if add.Host == "" {
		return list
	}
	for i, old := range list {
		if !sameTarget(old, add) {
			continue
		}
		merged := old
		if add.Service != "" {
			merged.Service = add.Service
		}
		if add.Protocol != "" {
			merged.Protocol = add.Protocol
		}
		if add.Note != "" {
			merged.Note = add.Note
		}
		if add.Verified {
			merged.Verified = true
			if add.Evidence != "" {
				merged.Evidence = add.Evidence
			}
		}
		list[i] = merged
		return list
	}
	return append(list, add)
}

func sameTarget(a, b CredentialTarget) bool {
	return strings.EqualFold(strings.TrimSpace(a.Host), strings.TrimSpace(b.Host)) &&
		a.Port == b.Port &&
		strings.EqualFold(strings.TrimSpace(a.Service), strings.TrimSpace(b.Service))
}

// SortCredentialTargets 让输出稳定（报告与界面每次渲染顺序一致）：已验证的在前，然后按 host。
func SortCredentialTargets(list []CredentialTarget) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Verified != list[j].Verified {
			return list[i].Verified
		}
		if !strings.EqualFold(list[i].Host, list[j].Host) {
			return strings.ToLower(list[i].Host) < strings.ToLower(list[j].Host)
		}
		return list[i].Port < list[j].Port
	})
}

// FootholdView 是据点 payload 的读模型。
//
// 据点此前只有 access/privilege/detail 三段自由文本：平台知道"你在 web01 上拿到了 root"，
// 但不知道**怎么再进去**——于是任何后续动作都只能让模型自己重连，平台既帮不上忙，
// 也无法对"连到哪"做授权范围校验。结构化连法把这件事变成可执行、可审计的：
//
//	protocol      ssh | webshell（闭集，见 ValidFootholdProtocol）
//	host/port     连哪（host 必填，也是 RoE 校验的对象）
//	credential_id ssh 用哪条凭据节点的口令/私钥
//	url/param     webshell 打哪个入口、命令放在哪个参数（默认 cmd）
//
// 明文永远不在这里：ssh 的凭据由 credential_id 指向凭据节点，执行时临时落盘、读完即删。
type FootholdView struct {
	Summary    string `json:"summary"`
	Detail     string `json:"detail"`
	Evidence   string `json:"evidence"`
	Confidence string `json:"confidence"`
	Access     string `json:"access"`
	Privilege  string `json:"privilege"`

	Protocol     string `json:"protocol,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	URL          string `json:"url,omitempty"`
	Param        string `json:"param,omitempty"`
	CredentialID int64  `json:"credential_id,omitempty"`
}

// ParseFoothold 解析据点节点的 payload（畸形一律当空字段，同 ParseCredential）。
func ParseFoothold(n *Node) FootholdView {
	var v FootholdView
	if n == nil || len(n.Payload) == 0 {
		return v
	}
	_ = json.Unmarshal(n.Payload, &v)
	return v
}

// FootholdProtocols 是**平台能代为执行**的协议闭集。写别的值不算错，但不能执行——
// 所以 report_foothold 会当场拒收并列出可用值，而不是留一个只能看不能用的字段。
var FootholdProtocols = []string{"ssh", "webshell"}

// ValidFootholdProtocol 报告协议是否可执行。
func ValidFootholdProtocol(p string) bool {
	for _, v := range FootholdProtocols {
		if p == v {
			return true
		}
	}
	return false
}
