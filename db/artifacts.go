package db

import (
	"encoding/json"
	"strings"
)

// 遗留物（P43）：我们**留在目标上**的东西。
//
// 它跟其它战果是反的——据点与凭据记的是"我们拿到了什么"，遗留物记的是"我们给客户留下了什么"。
// 没清掉的 webshell 就是留在客户系统上的后门；而在此之前的平台里，没有任何地方记过这件事，
// 交付时也说不清我们动过什么。这一层补的就是这本账。
//
// 它**不限制任何行为**，只要求落了东西说一声——与"不压掉模型的发散性"这条约束零冲突。

// 遗留物分类。取值是**封闭词表**，由本包归一（不靠 CHECK 约束：与 findings.status 同理，
// 归一放在代码里改起来不用动 schema）。归一到 other 而不是拒收——分类错不该让一条真实的
// 遗留物进不了台账，那恰恰是这本账最不能出的错。
const (
	ArtifactWebshell  = "webshell"       // 落地的 webshell / 一句话木马
	ArtifactUpload    = "upload"         // 上传的工具、脚本、文件（含临时目录里的）
	ArtifactAccount   = "account"        // 新建/改动过的账号
	ArtifactScheduled = "scheduled_task" // 计划任务 / cron / systemd timer
	ArtifactConfig    = "config_change"  // 改过的配置（含服务、注册表、启动项）
	ArtifactService   = "service"        // 安装/启动过的服务
	ArtifactOther     = "other"
)

// artifactKinds 是分类词表。NormalizeArtifactKind 用它归一。
var artifactKinds = map[string]string{
	ArtifactWebshell:  ArtifactWebshell,
	ArtifactUpload:    ArtifactUpload,
	ArtifactAccount:   ArtifactAccount,
	ArtifactScheduled: ArtifactScheduled,
	ArtifactConfig:    ArtifactConfig,
	ArtifactService:   ArtifactService,
	ArtifactOther:     ArtifactOther,
	// 常见别名：让模型写 web_shell / webshell / 后门 时都能落进同一格，
	// 而不是各自变成一个自由文本分类（那会让报告没法归类）。
	"web_shell":   ArtifactWebshell,
	"backdoor":    ArtifactWebshell,
	"file":        ArtifactUpload,
	"user":        ArtifactAccount,
	"cron":        ArtifactScheduled,
	"task":        ArtifactScheduled,
	"registry":    ArtifactConfig,
	"persistence": ArtifactConfig,
	"daemon":      ArtifactService,
}

// NormalizeArtifactKind 把自由输入归一到分类词表；不认识的一律落 other（不拒收）。
func NormalizeArtifactKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if v, ok := artifactKinds[k]; ok {
		return v
	}
	return ArtifactOther
}

// ValidArtifactState 校验状态取值。**不做归一**：状态不是分类，猜错会让"没清"显示成"清了"，
// 那是这本账唯一不能犯的错——所以非法值直接拒收，由调用方要求模型改。
func ValidArtifactState(s string) bool {
	switch s {
	case StateArtifactActive, StateArtifactRemoved, StateArtifactUnknown:
		return true
	}
	return false
}

// Artifact 是遗留物 payload 的读模型（报告、概览、界面共用一份解析）。
type Artifact struct {
	Summary    string `json:"summary"`
	Detail     string `json:"detail"`
	Evidence   string `json:"evidence"`
	Confidence string `json:"confidence"`
	// Kind 是分类（webshell / upload / account / …）。
	Kind string `json:"kind,omitempty"`
	// Host 是落在哪台目标上。
	Host string `json:"host,omitempty"`
	// Locator 是具体位置：文件路径、账号名、任务名、注册表键。**报告要靠它才找得到东西。**
	// 文件类必须是**目标上的绝对路径**（清理靠它），而不是 URL。
	Locator string `json:"locator,omitempty"`
	// URL 是访问入口：webshell/上传文件的访问地址，或可执行方式。与 Locator 配套——
	// 一个回答"在目标哪里"，一个回答"怎么用到它"，报告两样都要。
	URL string `json:"url,omitempty"`
	// Cleanup 是怎么清（命令或步骤）。写下来，清理的人不用重新推一遍。
	Cleanup string `json:"cleanup,omitempty"`
	// RemovedEvidence 是清理时的那一行输出——`removed` 状态必须有它。
	RemovedEvidence string `json:"removed_evidence,omitempty"`
}

// ParseArtifact 解析一条遗留物节点的 payload。畸形 payload 一律当空字段，不报错
// （读路径不该因为一条脏数据整段失败，同 ParseCredential）。
func ParseArtifact(n *Node) Artifact {
	var a Artifact
	if n == nil || len(n.Payload) == 0 {
		return a
	}
	_ = json.Unmarshal(n.Payload, &a)
	return a
}

// Label 是一行可读标签：分类 + 位置（+ 主机）。
func (a Artifact) Label() string {
	parts := []string{}
	if k := ArtifactKindLabel(a.Kind); k != "" {
		parts = append(parts, k)
	}
	if a.Locator != "" {
		parts = append(parts, a.Locator)
	}
	if a.Host != "" {
		parts = append(parts, "于 "+a.Host)
	}
	if len(parts) == 0 {
		return a.Summary
	}
	return strings.Join(parts, " ")
}

// ArtifactKindLabel 把分类翻成报告/界面里的中文。未知值原样返回（不猜）。
func ArtifactKindLabel(k string) string {
	switch k {
	case ArtifactWebshell:
		return "Webshell"
	case ArtifactUpload:
		return "上传文件"
	case ArtifactAccount:
		return "账号"
	case ArtifactScheduled:
		return "计划任务"
	case ArtifactConfig:
		return "配置改动"
	case ArtifactService:
		return "服务"
	case ArtifactOther:
		return "其他"
	}
	return k
}

// ArtifactStateLabel 渲染遗留物状态。三态**各自成词**——尤其 unknown 不能并进 active，
// 那会把"我们没去确认"说成"我们确认它还留着"（同检测三态的纪律，方向相反但道理一样）。
func ArtifactStateLabel(s string) string {
	switch s {
	case StateArtifactActive:
		return "仍在目标上（未清理）"
	case StateArtifactRemoved:
		return "已清理"
	case StateArtifactUnknown:
		return "未确认（不知道还在不在）"
	}
	return s
}

// ArtifactStateRank 给状态一个可排序的档位：**未清理的排最前**——交付物里最要紧的
// 就是"还有东西留在客户系统上"这一条。
func ArtifactStateRank(s string) int {
	switch s {
	case StateArtifactActive:
		return 0
	case StateArtifactUnknown:
		return 1
	case StateArtifactRemoved:
		return 2
	}
	return 3
}
