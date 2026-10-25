package server

// 任务面：任务 CRUD、战役、分类/模板、任务归档、任务级资产拦截规则、
// 公司级知识（跨任务只读先验）、计划任务。
//
// 路由注册按域拆到本文件，由 registerRoutes 串起来（见 server.go）。
// 拆分的回归网是 server/routes_snapshot_test.go：路由集合必须逐条不变。
func (s *Server) registerTaskRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("GET /api/tasks/export.csv", s.exportTasksCSV) // 任务列表 CSV（与列表同一份 DTO）
	mux.HandleFunc("POST /api/tasks", s.createTask)
	// 战役：任务之上的一层（归属 + 聚合）。归属走声明式的 PUT —— 一次给出应当包含的全部任务。
	mux.HandleFunc("GET /api/campaigns", s.listCampaigns)
	mux.HandleFunc("POST /api/campaigns", s.createCampaign)
	mux.HandleFunc("GET /api/campaigns/{id}", s.getCampaign)
	mux.HandleFunc("PATCH /api/campaigns/{id}", s.patchCampaign)
	mux.HandleFunc("DELETE /api/campaigns/{id}", s.deleteCampaign)
	mux.HandleFunc("PUT /api/campaigns/{id}/tasks", s.putCampaignTasks)
	mux.HandleFunc("GET /api/task-categories", s.pgListTaskCategories)
	mux.HandleFunc("POST /api/task-categories", s.pgCreateTaskCategory)
	mux.HandleFunc("PATCH /api/task-categories/{id}", s.pgRenameTaskCategory)
	mux.HandleFunc("DELETE /api/task-categories/{id}", s.pgDeleteTaskCategory)
	mux.HandleFunc("POST /api/tasks/category/batch", s.updateTasksCategoryBatch)
	mux.HandleFunc("GET /api/task-templates", s.pgListTaskTemplates)
	mux.HandleFunc("POST /api/task-templates", s.pgCreateTaskTemplate)
	mux.HandleFunc("PATCH /api/task-templates/{id}", s.pgUpdateTaskTemplate)
	mux.HandleFunc("DELETE /api/task-templates/{id}", s.pgDeleteTaskTemplate)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.updateTaskMetadata)
	mux.HandleFunc("PATCH /api/tasks/{id}/category", s.updateTaskCategory)
	// 任务级资产拦截/允许规则
	mux.HandleFunc("GET /api/tasks/{id}/intercept-rules", s.taskInterceptListRules)
	mux.HandleFunc("POST /api/tasks/{id}/intercept-rules", s.taskInterceptCreateRule)
	mux.HandleFunc("PUT /api/tasks/{id}/intercept-rules/{rid}", s.taskInterceptUpdateRule)
	mux.HandleFunc("DELETE /api/tasks/{id}/intercept-rules/{rid}", s.taskInterceptDeleteRule)
	mux.HandleFunc("POST /api/tasks/{id}/intercept-rules/{rid}/toggle", s.taskInterceptToggleRule)
	mux.HandleFunc("POST /api/tasks/control/batch", s.controlTasksBatch)
	mux.HandleFunc("GET /api/task-archives", s.listTaskArchives)
	mux.HandleFunc("GET /api/task-archives/{id}", s.getTaskArchive)
	// P23 公司级知识:跨任务只读背景(Agent 只能沉淀,改/废由人做)
	mux.HandleFunc("GET /api/companies/{id}/knowledge", s.listCompanyKnowledge)
	mux.HandleFunc("PATCH /api/companies/{id}/knowledge/{kid}", s.patchCompanyKnowledge)
	mux.HandleFunc("POST /api/findings/{id}/promote-knowledge", s.promoteFindingKnowledge)
	// P22 计划任务:结构化频率到点自动建任务(不引 cron 依赖;不追补;CAS 防重复)
	mux.HandleFunc("GET /api/task-schedules", s.listTaskSchedules)
	mux.HandleFunc("POST /api/task-schedules", s.createTaskSchedule)
	mux.HandleFunc("PATCH /api/task-schedules/{id}", s.patchTaskSchedule)
	mux.HandleFunc("DELETE /api/task-schedules/{id}", s.deleteTaskSchedule)
	mux.HandleFunc("POST /api/task-schedules/{id}/run", s.runTaskScheduleNow)
	mux.HandleFunc("POST /api/tasks/{id}/archive", s.queueTaskArchive)
	mux.HandleFunc("POST /api/tasks/archive/batch", s.queueTaskArchivesBatch)
	mux.HandleFunc("POST /api/task-archives/{id}/restore", s.queueTaskArchiveRestore)
	mux.HandleFunc("POST /api/task-archives/restore/batch", s.restoreTaskArchivesBatch)
	mux.HandleFunc("DELETE /api/task-archives/{id}", s.queueTaskArchiveDelete)
	mux.HandleFunc("POST /api/task-archives/delete/batch", s.deleteTaskArchivesBatch)
	mux.HandleFunc("GET /api/tasks/{id}/coverage", s.taskCoverage)
	mux.HandleFunc("GET /api/tasks/{id}/coverage-graph", s.taskCoverageGraph)
	mux.HandleFunc("GET /api/tasks/{id}/asset-refs", s.taskAssetRefs)
	mux.HandleFunc("POST /api/tasks/{id}/assets", s.attachTaskAssets)
	mux.HandleFunc("DELETE /api/tasks/{id}/assets/{assetID}", s.detachTaskAsset)
	mux.HandleFunc("GET /api/tasks/{id}/intent-assets", s.taskIntentAssets)
}
