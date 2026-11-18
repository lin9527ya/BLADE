package db

import (
	"fmt"
	"strings"
)

// ValidateDetectionResult 校验一次检测验证的核心字段，返回归一化后的 ATT&CK 技术 ID。
// agent 的 report_detection_check 与服务端人工录入共用这一处规则，避免两套口径漂移。
//
// 三态必须分清：triggered=确实响了（有日志/告警为证）、not_triggered=确实没响
// （查过、该时段确实没有记录）、unknown=无法判定——**unknown 不是「没响」**。
func ValidateDetectionResult(result, technique, evidence, confidence string) (string, error) {
	result = strings.TrimSpace(result)
	switch result {
	case StateDetectionTriggered, StateDetectionNotTriggered, StateDetectionUnknown:
	default:
		return "", fmt.Errorf("result 必须是 triggered / not_triggered / unknown（三态要分清：unknown 不是「没响」），收到 %q", result)
	}
	techs := NormalizeTechniques([]string{technique})
	if len(techs) != 1 {
		return "", fmt.Errorf("technique 必须是单个 ATT&CK 技术 ID（T#### 或 T####.###），收到 %q", technique)
	}
	if strings.TrimSpace(evidence) == "" {
		return "", fmt.Errorf("evidence 不能为空：你查了哪里、看到了什么——「没看到」本身也要有据（查的哪个源、哪个时段）")
	}
	confidence = strings.TrimSpace(confidence)
	if confidence == "" {
		return "", fmt.Errorf("confidence 不能为空：high 直接看到日志 / medium 间接推断 / low 仅凭现象")
	}
	if !ValidConfidence(confidence) {
		return "", fmt.Errorf("confidence 取值必须是 high|medium|low")
	}
	return techs[0], nil
}
