package db

import (
	"encoding/json"
	"strings"
)

// 目标侧痕迹（footprint）：测试动作**可能**在目标上留下的可检出信号。
//
// 它跟遗留物（artifact）是两个方向：
//   - artifact 记“我们确实留下了什么具体对象”（文件/账号/任务），清理它等于删除对象；
//   - footprint 记“目标侧可能看见什么信号”（登录失败、EDR 遥测、访问日志、扫描痕迹），
//     有些可以定向清理，有些只能接受风险。清单来自实际执行过的命令 + opsec 知识库，
//     也可由 worker 实测补录。
//
// 状态词严格区分：cleaned 是“清过且有证据”，accepted 是“决定不清理/清不了，接受风险”。
// 两者都并进“已处理”会让报告读不出暴露面。

const (
	FootprintSourcePredicted = "predicted" // 命令历史 + opsec 知识库自动归纳
	FootprintSourceObserved  = "observed"  // worker 实测/观察后补录
)

const (
	FootprintCatLog     = "log"     // 目标系统日志
	FootprintCatEDR     = "edr"     // EDR / HIDS / AV
	FootprintCatNetwork = "network" // 边界设备 / IDS / 代理
	FootprintCatApp     = "app"     // 应用 / 中间件 / 数据库日志
	FootprintCatLocal   = "local"   // 目标本机的 shell 历史 / 临时文件
	FootprintCatOther   = "other"
)

const (
	FootprintExposureLow    = "low"
	FootprintExposureMedium = "medium"
	FootprintExposureHigh   = "high"
)

var footprintCategories = map[string]string{
	FootprintCatLog:     FootprintCatLog,
	FootprintCatEDR:     FootprintCatEDR,
	FootprintCatNetwork: FootprintCatNetwork,
	FootprintCatApp:     FootprintCatApp,
	FootprintCatLocal:   FootprintCatLocal,
	FootprintCatOther:   FootprintCatOther,
	// 常见别名，避免模型写“日志/audit/access log”时各落一格。
	"logs":        FootprintCatLog,
	"audit":       FootprintCatLog,
	"system":      FootprintCatLog,
	"security":    FootprintCatEDR,
	"av":          FootprintCatEDR,
	"hids":        FootprintCatEDR,
	"ids":         FootprintCatNetwork,
	"proxy":       FootprintCatNetwork,
	"application": FootprintCatApp,
	"access_log":  FootprintCatApp,
	"history":     FootprintCatLocal,
	"temp":        FootprintCatLocal,
}

func NormalizeFootprintCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if v, ok := footprintCategories[c]; ok {
		return v
	}
	return FootprintCatOther
}

func NormalizeFootprintExposure(e string) string {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case FootprintExposureLow, "低":
		return FootprintExposureLow
	case FootprintExposureMedium, "中":
		return FootprintExposureMedium
	case FootprintExposureHigh, "高":
		return FootprintExposureHigh
	}
	return FootprintExposureMedium
}

func ValidFootprintState(s string) bool {
	switch s {
	case StateFootprintOpen, StateFootprintUnknown, StateFootprintAccepted, StateFootprintCleaned, StateFootprintNotFound:
		return true
	}
	return false
}

// Footprint 是 footprint payload 的读模型（报告、概览、界面共用一份解析）。
type Footprint struct {
	Summary    string   `json:"summary"`
	Detail     string   `json:"detail,omitempty"`
	Evidence   string   `json:"evidence,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
	Source     string   `json:"source,omitempty"`
	Category   string   `json:"category,omitempty"`
	Where      []string `json:"where,omitempty"`
	Signals    []string `json:"signals,omitempty"`
	Exposure   string   `json:"exposure,omitempty"`
	Host       string   `json:"host,omitempty"`
	Locator    string   `json:"locator,omitempty"`
	Cleanup    string   `json:"cleanup,omitempty"`
	// OpsecNoteID 是自动归纳的去重键；非空表示来自知识库，不是 worker 实测。
	OpsecNoteID    string `json:"opsec_note_id,omitempty"`
	Occurrences    int    `json:"occurrences,omitempty"`
	LastActivityID int64  `json:"last_activity_id,omitempty"`
	// CleanupIntentID 记录最近一次“交给 Agent 清理”的意图，防止重复点击。
	CleanupIntentID int64 `json:"cleanup_intent_id,omitempty"`
	// AssetIDs 是自动归纳写入的资产锚点副本；清理意图把它带进 payload，
	// 让 worker 知道该去哪台目标，而不必在探索图上重新检索。
	AssetIDs         []int64 `json:"asset_ids,omitempty"`
	RemovedEvidence  string  `json:"removed_evidence,omitempty"`
	RemovedNote      string  `json:"removed_note,omitempty"`
	AcceptedReason   string  `json:"accepted_reason,omitempty"`
	NotFoundEvidence string  `json:"not_found_evidence,omitempty"`
	NotFoundNote     string  `json:"not_found_note,omitempty"`
}

func ParseFootprint(n *Node) Footprint {
	var f Footprint
	if n == nil || len(n.Payload) == 0 {
		return f
	}
	_ = json.Unmarshal(n.Payload, &f)
	// 读侧归一化：payload 可能来自旧版本、手工写入或外部导入，不能假设它已经
	// 用过 Normalize；否则报告会把 "audit"/"高" 这类别名原样印出来。
	f.Category = NormalizeFootprintCategory(f.Category)
	if strings.TrimSpace(f.Exposure) != "" {
		f.Exposure = NormalizeFootprintExposure(f.Exposure)
	}
	return f
}

func (f Footprint) Label() string {
	parts := []string{}
	if l := FootprintCategoryLabel(f.Category); l != "" {
		parts = append(parts, l)
	}
	if f.Host != "" {
		parts = append(parts, "于 "+f.Host)
	}
	if len(parts) == 0 {
		return f.Summary
	}
	return strings.Join(parts, " ")
}

func FootprintCategoryLabel(c string) string {
	switch c {
	case FootprintCatLog:
		return "目标系统日志"
	case FootprintCatEDR:
		return "安全设备/EDR"
	case FootprintCatNetwork:
		return "网络侧"
	case FootprintCatApp:
		return "应用日志"
	case FootprintCatLocal:
		return "本机痕迹"
	case FootprintCatOther:
		return "其他"
	}
	return c
}

func FootprintStateLabel(s string) string {
	switch s {
	case StateFootprintOpen:
		return "未处置"
	case StateFootprintUnknown:
		return "未确认"
	case StateFootprintAccepted:
		return "接受风险"
	case StateFootprintCleaned:
		return "已清理"
	case StateFootprintNotFound:
		return "未发现"
	}
	return s
}

// FootprintStateRank 报告排序：未处置 > 未确认 > 接受风险 > 已清理。
func FootprintStateRank(s string) int {
	switch s {
	case StateFootprintOpen:
		return 0
	case StateFootprintUnknown:
		return 1
	case StateFootprintAccepted:
		return 2
	case StateFootprintCleaned:
		return 3
	case StateFootprintNotFound:
		return 4
	}
	return 4
}

func FootprintSourceLabel(s string) string {
	switch s {
	case FootprintSourcePredicted:
		return "自动归纳"
	case FootprintSourceObserved:
		return "Agent 上报"
	}
	return s
}

func FootprintExposureLabel(e string) string {
	if strings.TrimSpace(e) == "" {
		return ""
	}
	switch NormalizeFootprintExposure(e) {
	case FootprintExposureLow:
		return "低"
	case FootprintExposureMedium:
		return "中"
	case FootprintExposureHigh:
		return "高"
	}
	return e
}
