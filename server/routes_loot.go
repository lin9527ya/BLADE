package server

// 全局战果与凭据台账（只读）：跨任务检索、导出；凭据明文不经过这里。
func (s *Server) registerLootRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/loot", s.listLoot)
	mux.HandleFunc("GET /api/loot/export", s.exportLoot)
	mux.HandleFunc("GET /api/credential-attempts", s.listCredentialAttempts)
}
