package server

// 领域数据面：资产与 CSV 导入、AD 攻击路径导入、云上枚举导入、
// 授权范围（RoE）与停机开关、凭据可利用面、可检出性知识库、作业时间线。
//
// 路由注册按域拆到本文件，由 registerRoutes 串起来（见 server.go）。
// 拆分的回归网是 server/routes_snapshot_test.go：路由集合必须逐条不变。
func (s *Server) registerAssetRoutes(mux routeRegistrar) {
	// asset system
	mux.HandleFunc("GET /api/assets", s.listAssets)
	mux.HandleFunc("GET /api/assets/counts", s.assetCounts)
	mux.HandleFunc("POST /api/assets", s.insertAssets)
	mux.HandleFunc("DELETE /api/assets", s.deleteAssets)
	// CSV 目标清单导入：预览只读、应用才写（同一份解析器，见 server/asset_import.go）。
	mux.HandleFunc("POST /api/assets/import/preview", s.assetImportPreview)
	mux.HandleFunc("POST /api/assets/import", s.assetImportApply)
	// AD 攻击路径（P32）：采集结果导入任务图 + 从图里读域内路径。
	// 导入是**数据摄入**、不产生对目标的动作，因此与资产导入同理不过 RoE/命令门；
	// 但"据此去打"走的仍是既有的意图/命令门，这一点别混。
	mux.HandleFunc("POST /api/ad/import/preview", s.adImportPreview)
	mux.HandleFunc("POST /api/ad/import", s.adImportApply)
	mux.HandleFunc("GET /api/tasks/{id}/ad-paths", s.taskADPaths)
	// P35 停机开关与作业时间窗的执行侧：状态查询 + 两个动作（停机/解除、继续全部）。
	// 停机是**立即生效**的（不等 scheduler tick）：现场叫停时那 5 秒不该等。
	mux.HandleFunc("GET /api/scope/enforcement", s.scopeEnforcementStatus)
	mux.HandleFunc("POST /api/scope/kill-switch", s.setKillSwitch)
	mux.HandleFunc("POST /api/scope/resume-all", s.resumeAllTasks)
	// P36：凭据明文的受控入口（有副作用——写留痕，所以是 POST 不是 GET）。
	mux.HandleFunc("POST /api/credentials/{id}/reveal", s.revealCredential)
	// P38：可检出性知识库一览（只读——让人看得见这份清单的边界在哪）。
	mux.HandleFunc("GET /api/opsec/knowledge", s.opsecKnowledge)
	// P39 云上枚举结果导入（G 项）：与 AD 导入同一套契约、同一张图。
	mux.HandleFunc("POST /api/cloud/import/preview", s.cloudImportPreview)
	mux.HandleFunc("POST /api/cloud/import", s.cloudImportApply)
	// P37 作业时间线：页面读的与导出的**同一份装配**（客户拿到的那份不该和界面对不上）。
	mux.HandleFunc("GET /api/tasks/{id}/timeline", s.taskTimeline)
	mux.HandleFunc("GET /api/tasks/{id}/timeline.md", s.taskTimelineExport)
}
