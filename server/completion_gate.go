package server

import "strings"

// completionGateMode 是收官审计的语气：
//
//	block —— 目标达成但有未豁免缺口时拒收 prove_goal（默认；"完成"必须包含"探索充分"）
//	notice —— 放行，把缺口写进 prove_goal 的回执，由模型决定是否继续
//	off —— 完全关闭（旧行为）
//
// 每轮现读：改设置对下一轮规划生效，不需要重建 agent。
func (s *Server) completionGateMode() string {
	if s.m == nil || s.m.pg == nil {
		return "block"
	}
	v, ok, err := s.m.pg.GetSetting(settingCompletionGate)
	if err != nil || !ok {
		return "block"
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "block", "notice", "off":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "block"
	}
}
