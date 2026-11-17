package server

// 全局战果与凭据台账（只读）：跨任务检索、导出；凭据明文不经过这里。
func (s *Server) registerLootRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/loot", s.listLoot)
	mux.HandleFunc("GET /api/loot/export", s.exportLoot)
	mux.HandleFunc("GET /api/credential-attempts", s.listCredentialAttempts)

	// P0 执行面：凭据工作台 / 据点 / 隧道。读取与本地记录直接响应；
	// “验证/存活检查”只生成 human 意图，执行仍由 worker 走 guard。
	mux.HandleFunc("GET /api/credentials", s.listCredentialsWorkbench)
	mux.HandleFunc("GET /api/credentials/{id}/checks", s.listCredentialChecks)
	mux.HandleFunc("POST /api/credentials/{id}/checks", s.recordCredentialCheck)
	mux.HandleFunc("POST /api/credentials/{id}/verify-intent", s.credentialVerifyIntent)
	mux.HandleFunc("POST /api/credentials/{id}/lost", s.markCredentialLost)
	mux.HandleFunc("POST /api/credentials/batch-lost", s.batchMarkCredentialsLost)
	mux.HandleFunc("GET /api/footholds", s.listFootholdsWorkbench)
	mux.HandleFunc("POST /api/footholds/{id}/check-intent", s.footholdCheckIntent)
	mux.HandleFunc("POST /api/footholds/{id}/lost", s.markFootholdLost)
	mux.HandleFunc("GET /api/tunnels", s.listTunnels)
}
