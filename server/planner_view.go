package server

// plannerBoundedView 决定 planner 每轮预取与 graph_overview 默认返回的是
// 有界黑板视图（true，默认；省 token）还是完整态势（false）。
//
// 它独立于探索约束分组：有界视图是"给模型看多少"，不是"允许模型做什么"。
// 想让发散完全由模型决定、不受裁剪影响时，把它关掉；代价是每轮上下文变大。
func (s *Server) plannerBoundedView() bool {
	if s.m == nil || s.m.pg == nil {
		return true
	}
	return s.m.pg.GetBool(settingPlannerBoundedView, true)
}
