package server

// 任务详情面：工作空间文件管理、约束与探索策略（P18/P20）、
// 任务/意图控制、单条意图重跑、active 标记。
//
// 路由注册按域拆到本文件，由 registerRoutes 串起来（见 server.go）。
// 拆分的回归网是 server/routes_snapshot_test.go：路由集合必须逐条不变。
func (s *Server) registerWorkspaceRoutes(mux routeRegistrar) {
	// 工作空间文件管理器（针对 workDir）
	mux.HandleFunc("GET /api/workspace/list", s.wsList)
	mux.HandleFunc("GET /api/workspace/read", s.wsRead)
	mux.HandleFunc("POST /api/workspace/write", s.wsWrite)
	mux.HandleFunc("POST /api/workspace/mkdir", s.wsMkdir)
	mux.HandleFunc("DELETE /api/workspace/delete", s.wsDelete)
	mux.HandleFunc("GET /api/workspace/download", s.wsDownload)
	mux.HandleFunc("POST /api/workspace/upload", s.wsUpload)
	mux.HandleFunc("GET /api/tasks/{id}/scope", s.taskScopeList)
	mux.HandleFunc("POST /api/tasks/{id}/scope", s.taskScopeAdd)
	mux.HandleFunc("DELETE /api/tasks/{id}/scope/{sid}", s.taskScopeDelete)
	mux.HandleFunc("GET /api/tasks/{id}/goals", s.listGoals)                           // 目标管理:列出本任务全部目标
	mux.HandleFunc("POST /api/tasks/{id}/goals", s.addGoal)                            // 目标管理:人工新增目标(复活任务)
	mux.HandleFunc("PATCH /api/tasks/{id}/goals/{gid}", s.editGoal)                    // 目标管理:修改目标(复活任务)
	mux.HandleFunc("DELETE /api/tasks/{id}/goals/{gid}", s.deleteGoal)                 // 目标管理:硬删除目标(不复活)
	mux.HandleFunc("GET /api/tasks/{id}/hints", s.listHints)                           // 战略提示:列出待采纳/已采纳
	mux.HandleFunc("POST /api/tasks/{id}/hints", s.addHint)                            // 战略提示:人工新增(唤醒 planner,不复活任务)
	mux.HandleFunc("PATCH /api/tasks/{id}/hints/{hid}", s.updateHint)                  // 战略提示:改文本 / 撤销采纳
	mux.HandleFunc("POST /api/tasks/{id}/hints/{hid}/remind", s.remindHint)            // 战略提示:再次提醒 planner
	mux.HandleFunc("DELETE /api/tasks/{id}/hints/{hid}", s.deleteHint)                 // 战略提示:人工删除
	mux.HandleFunc("GET /api/tasks/{id}/artifacts", s.listArtifacts)                   // 遗留物:列出本任务台账
	mux.HandleFunc("POST /api/tasks/{id}/artifacts/{aid}/cleared", s.clearArtifact)    // 遗留物:人工销账(必填清理证据)
	mux.HandleFunc("POST /api/tasks/{id}/artifacts/accept-risk", s.acceptArtifactRisk) // 遗留物:接受风险(必填理由,任务级)
	mux.HandleFunc("GET /api/tasks/{id}/constraints", s.listConstraints)               // 约束管理:列出本任务操作约束
	mux.HandleFunc("POST /api/tasks/{id}/constraints", s.addConstraint)                // 约束管理:新增约束(不通知 planner)
	mux.HandleFunc("PATCH /api/tasks/{id}/constraints/{cid}", s.editConstraint)        // 约束管理:修改约束
	mux.HandleFunc("DELETE /api/tasks/{id}/constraints/{cid}", s.deleteConstraint)     // 约束管理:删除约束
	// P18 约束可执行化：把约束编译成任务级拦截规则 / 授权范围（compile 只读预览，apply 落库）。
	mux.HandleFunc("GET /api/tasks/{id}/policy", s.getTaskPolicy)              // P20 探索策略:读生效策略+覆盖层+额度+近期被拒
	mux.HandleFunc("PATCH /api/tasks/{id}/policy", s.patchTaskPolicy)          // P20 探索策略:保存(下一轮规划生效,不回溯已有意图)
	mux.HandleFunc("DELETE /api/tasks/{id}/policy", s.patchTaskPolicy)         // P20 探索策略:清空覆盖(回到无策略)
	mux.HandleFunc("POST /api/tasks/{id}/policy/preview", s.previewTaskPolicy) // P20 探索策略:编译预览(只读,拿本任务真实调用当样本)
	mux.HandleFunc("POST /api/tasks/{id}/policy/prune", s.pruneTaskPolicy)     // P20 探索策略:清理被新策略禁止的未领取意图
	mux.HandleFunc("POST /api/tasks/{id}/constraints/{cid}/compile", s.compileConstraint)
	mux.HandleFunc("POST /api/tasks/{id}/constraints/{cid}/apply", s.applyConstraint)
	mux.HandleFunc("POST /api/tasks/{id}/control", s.control)
	mux.HandleFunc("PUT /api/tasks/{id}/llm", s.updateTaskLLMProfiles)
	mux.HandleFunc("GET /api/tasks/{id}/llm/resolution", s.taskLLMResolutionHandler)
	mux.HandleFunc("POST /api/tasks/{id}/intents/{iid}/control", s.controlIntent)
	mux.HandleFunc("POST /api/tasks/{id}/intents/{iid}/messages", s.sendWorkerMessage)
	mux.HandleFunc("POST /api/tasks/{id}/intents/{iid}/rerun", s.rerunIntent)    // 重跑单条 blocked/exhausted/stopped 意图
	mux.HandleFunc("POST /api/tasks/{id}/intents/rerun-blocked", s.rerunBlocked) // 批量重跑本任务全部 blocked 意图
	mux.HandleFunc("GET /api/executions/{eid}", s.getExecution)                  // 长任务执行台账详情（时间线跳转）
	mux.HandleFunc("POST /api/active", s.setActive)
}
