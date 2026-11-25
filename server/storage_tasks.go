package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

const maxStorageTaskCleanBatch = 100

type storageTaskItem struct {
	TaskID             string    `json:"task_id"`
	Name               string    `json:"name"`
	Status             string    `json:"status"`
	ExplorationID      int64     `json:"exploration_id"`
	Orphan             bool      `json:"orphan"`
	ActiveRunning      bool      `json:"active_running"`
	FilesBytes         int64     `json:"files_bytes"`
	FilesUnits         int       `json:"files_units"`
	TranscriptsBytes   int64     `json:"transcripts_bytes"`
	TranscriptsCount   int       `json:"transcripts_count"`
	ActivityRows       int64     `json:"activity_rows"`
	ActivityBytes      int64     `json:"activity_bytes"`
	SideQuestionRows   int64     `json:"side_question_rows"`
	SideQuestionBytes  int64     `json:"side_question_bytes"`
	LLMRecordRows      int64     `json:"llm_record_rows"`
	LLMRecordBytes     int64     `json:"llm_record_bytes"`
	LLMRecordBodyRows  int64     `json:"llm_record_body_rows"`
	LLMRecordBodyBytes int64     `json:"llm_record_body_bytes"`
	LLMUsageRows       int64     `json:"llm_usage_rows"`
	LLMUsageBytes      int64     `json:"llm_usage_bytes"`
	ToolExecutionRows  int64     `json:"tool_execution_rows"`
	ToolExecutionBytes int64     `json:"tool_execution_bytes"`
	BlackboardLogRows  int64     `json:"blackboard_log_rows"`
	BlackboardLogBytes int64     `json:"blackboard_log_bytes"`
	TotalBytes         int64     `json:"total_bytes"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type storageTasksReport struct {
	ScannedAt   time.Time         `json:"scanned_at"`
	TotalBytes  int64             `json:"total_bytes"`
	OrphanBytes int64             `json:"orphan_bytes"`
	Tasks       []storageTaskItem `json:"tasks"`
}

func (s *Server) taskIsActivelyRunning(id string, fallbackDBStatus string) bool {
	if t, ok := s.m.Task(id); ok && t != nil {
		resolved := s.resolvedTaskStatus(t)
		if resolved == db.TaskStatusRunning || resolved == db.TaskStatusQueued {
			return true
		}
		if s.engine != nil && s.engine.Started(id) && !s.engine.IsPaused(id) {
			return true
		}
		return false
	}
	return fallbackDBStatus == db.TaskStatusRunning || fallbackDBStatus == db.TaskStatusQueued
}

func countDirTopEntries(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}

func (s *Server) buildStorageTasksReport(ctx context.Context) (*storageTasksReport, error) {
	tasks, err := s.m.pg.ListTasks()
	if err != nil {
		return nil, fmt.Errorf("读取任务列表失败: %w", err)
	}
	itemsMap := make(map[string]*storageTaskItem, len(tasks))
	taskIDs := make([]int64, 0, len(tasks))
	expByTask := make(map[int64]int64, len(tasks))
	taskByExp := make(map[int64]string, len(tasks))

	for _, t := range tasks {
		if t == nil {
			continue
		}
		idStr := strconv.FormatInt(t.ID, 10)
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = strings.TrimSpace(t.Description)
		}
		if name == "" {
			name = fmt.Sprintf("任务 #%d", t.ID)
		}
		status := t.Status
		if mt, ok := s.m.Task(idStr); ok && mt != nil {
			status = s.resolvedTaskStatus(mt)
		}
		itemsMap[idStr] = &storageTaskItem{
			TaskID:        idStr,
			Name:          name,
			Status:        status,
			ExplorationID: t.ExplorationID,
			ActiveRunning: s.taskIsActivelyRunning(idStr, t.Status),
			UpdatedAt:     t.CreatedAt,
		}
		taskIDs = append(taskIDs, t.ID)
		if t.ExplorationID > 0 {
			expByTask[t.ID] = t.ExplorationID
			taskByExp[t.ExplorationID] = idStr
		}
	}

	// Scan data/tasks/<id>
	tasksRoot := filepath.Join(s.m.dir, "tasks")
	if entries, rerr := os.ReadDir(tasksRoot); rerr == nil {
		for _, e := range entries {
			name := e.Name()
			p := filepath.Join(tasksRoot, name)
			var sz int64
			var mod time.Time
			var serr error
			if e.IsDir() {
				sz, mod, serr = scanStorageUnit(p)
			} else if info, ierr := e.Info(); ierr == nil {
				sz, mod = info.Size(), info.ModTime()
			} else {
				serr = ierr
			}
			if serr != nil {
				continue
			}
			item, exists := itemsMap[name]
			if !exists {
				item = &storageTaskItem{
					TaskID:    name,
					Name:      "已删除任务残留 #" + name,
					Status:    "orphan",
					Orphan:    true,
					UpdatedAt: mod,
				}
				if nid, perr := strconv.ParseInt(name, 10, 64); perr == nil && nid > 0 {
					taskIDs = append(taskIDs, nid)
				}
				itemsMap[name] = item
			}
			item.FilesBytes += sz
			if e.IsDir() {
				item.FilesUnits += countDirTopEntries(p)
			} else {
				item.FilesUnits++
			}
			if mod.After(item.UpdatedAt) {
				item.UpdatedAt = mod
			}
		}
	}

	// Scan data/transcripts/exp<eid>-*
	transcriptsRoot := filepath.Join(s.m.dir, "transcripts")
	if entries, rerr := os.ReadDir(transcriptsRoot); rerr == nil {
		for _, e := range entries {
			eid, ok := parseTranscriptExpID(e.Name())
			if !ok {
				continue
			}
			p := filepath.Join(transcriptsRoot, e.Name())
			var sz int64
			var mod time.Time
			var serr error
			if e.IsDir() {
				sz, mod, serr = scanStorageUnit(p)
			} else if info, ierr := e.Info(); ierr == nil {
				sz, mod = info.Size(), info.ModTime()
			} else {
				serr = ierr
			}
			if serr != nil {
				continue
			}
			key, known := taskByExp[eid]
			if !known {
				key = fmt.Sprintf("exp-%d", eid)
			}
			item, exists := itemsMap[key]
			if !exists {
				item = &storageTaskItem{
					TaskID:        key,
					Name:          fmt.Sprintf("孤儿转写残留 (exp %d)", eid),
					Status:        "orphan",
					ExplorationID: eid,
					Orphan:        true,
					UpdatedAt:     mod,
				}
				itemsMap[key] = item
			}
			item.TranscriptsBytes += sz
			item.TranscriptsCount++
			if mod.After(item.UpdatedAt) {
				item.UpdatedAt = mod
			}
		}
	}

	dbStats, err := s.m.pg.TaskStorageStats(ctx, taskIDs, expByTask)
	if err != nil {
		return nil, err
	}
	rep := &storageTasksReport{
		ScannedAt: time.Now(),
		Tasks:     make([]storageTaskItem, 0, len(itemsMap)),
	}
	for _, item := range itemsMap {
		if nid, perr := strconv.ParseInt(item.TaskID, 10, 64); perr == nil && nid > 0 {
			if st, ok := dbStats[nid]; ok {
				item.ActivityRows = st.ActivityRows
				item.ActivityBytes = st.ActivityBytes
				item.SideQuestionRows = st.SideQuestionRows
				item.SideQuestionBytes = st.SideQuestionBytes
				item.LLMRecordRows = st.LLMRecordRows
				item.LLMRecordBytes = st.LLMRecordBytes
				item.LLMRecordBodyRows = st.LLMRecordBodyRows
				item.LLMRecordBodyBytes = st.LLMRecordBodyBytes
				item.LLMUsageRows = st.LLMUsageRows
				item.LLMUsageBytes = st.LLMUsageBytes
				item.ToolExecutionRows = st.ToolExecutionRows
				item.ToolExecutionBytes = st.ToolExecutionBytes
				item.BlackboardLogRows = st.BlackboardLogRows
				item.BlackboardLogBytes = st.BlackboardLogBytes
			}
		}
		item.TotalBytes = item.FilesBytes + item.TranscriptsBytes +
			item.ActivityBytes + item.SideQuestionBytes +
			item.LLMRecordBytes + item.LLMUsageBytes +
			item.ToolExecutionBytes + item.BlackboardLogBytes
		rep.TotalBytes += item.TotalBytes
		if item.Orphan {
			rep.OrphanBytes += item.TotalBytes
		}
		rep.Tasks = append(rep.Tasks, *item)
	}
	sort.Slice(rep.Tasks, func(i, j int) bool {
		if rep.Tasks[i].TotalBytes != rep.Tasks[j].TotalBytes {
			return rep.Tasks[i].TotalBytes > rep.Tasks[j].TotalBytes
		}
		return rep.Tasks[i].TaskID < rep.Tasks[j].TaskID
	})
	return rep, nil
}

func (s *Server) getStorageTasks(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	rep, err := s.buildStorageTasksReport(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"report": rep})
}

func parseStorageCleanScopes(raw []string) (files, transcripts bool, dbScopes db.TaskCleanScopes, err error) {
	if len(raw) == 0 {
		return false, false, dbScopes, errors.New("scopes 不能为空")
	}
	for _, s := range raw {
		switch strings.TrimSpace(s) {
		case "files":
			files = true
		case "transcripts":
			transcripts = true
		case "activity":
			dbScopes.Activity = true
		case "side_questions":
			dbScopes.SideQuestions = true
		case "llm_record_bodies":
			dbScopes.LLMRecordBodies = true
		case "llm_records":
			dbScopes.LLMRecords = true
		case "llm_usage":
			dbScopes.LLMUsage = true
		case "tool_executions":
			dbScopes.ToolExecutions = true
		case "blackboard_logs":
			dbScopes.BlackboardLogs = true
		default:
			return false, false, dbScopes, fmt.Errorf("未知清理范围 %q", s)
		}
	}
	return files, transcripts, dbScopes, nil
}

func safeStorageTaskToken(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}

type storageTaskCleanEntry struct {
	TaskID        string `json:"task_id"`
	FreedBytes    int64  `json:"freed_bytes"`
	FilesRemoved  bool   `json:"files_removed,omitempty"`
	Transcripts   int    `json:"transcripts_removed,omitempty"`
	DBRowsDeleted int64  `json:"db_rows_deleted,omitempty"`
}

func (s *Server) cleanStorageTasks(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		TaskIDs []string `json:"task_ids"`
		Scopes  []string `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.TaskIDs) == 0 || len(req.TaskIDs) > maxStorageTaskCleanBatch {
		writeErr(w, 400, fmt.Sprintf("task_ids 数量需为 1-%d", maxStorageTaskCleanBatch))
		return
	}
	cleanFiles, cleanTranscripts, dbScopes, err := parseStorageCleanScopes(req.Scopes)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	preReport, err := s.buildStorageTasksReport(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	byID := make(map[string]storageTaskItem, len(preReport.Tasks))
	for _, item := range preReport.Tasks {
		byID[item.TaskID] = item
	}

	seen := make(map[string]bool, len(req.TaskIDs))
	cleaned := make([]storageTaskCleanEntry, 0, len(req.TaskIDs))
	failures := make([]string, 0)
	var totalFreed int64

	tasksRoot := filepath.Join(s.m.dir, "tasks")
	transcriptsRoot := filepath.Join(s.m.dir, "transcripts")

	for _, rawID := range req.TaskIDs {
		id := strings.TrimSpace(rawID)
		if !safeStorageTaskToken(id) {
			failures = append(failures, fmt.Sprintf("%q：非法任务标识", rawID))
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true

		item, exists := byID[id]
		if !exists {
			failures = append(failures, fmt.Sprintf("#%s：任务及残留存储不存在", id))
			continue
		}
		if s.taskIsActivelyRunning(id, item.Status) {
			failures = append(failures, fmt.Sprintf("#%s（%s）：正在运行/排队中，请先暂停再执行按任务清理", id, item.Name))
			continue
		}

		entry := storageTaskCleanEntry{TaskID: id}
		if cleanFiles && !strings.HasPrefix(id, "exp-") {
			target := filepath.Join(tasksRoot, id)
			if pathWithin(tasksRoot, target) {
				if _, lerr := os.Lstat(target); lerr == nil {
					if rerr := os.RemoveAll(target); rerr != nil {
						failures = append(failures, fmt.Sprintf("#%s 产物目录：%v", id, rerr))
					} else {
						entry.FilesRemoved = true
						entry.FreedBytes += item.FilesBytes
					}
				}
			}
		}

		if cleanTranscripts && item.ExplorationID > 0 {
			prefix := fmt.Sprintf("exp%d-", item.ExplorationID)
			if entries, rerr := os.ReadDir(transcriptsRoot); rerr == nil {
				removedCount := 0
				for _, e := range entries {
					if !strings.HasPrefix(e.Name(), prefix) {
						continue
					}
					p := filepath.Join(transcriptsRoot, e.Name())
					if !pathWithin(transcriptsRoot, p) {
						continue
					}
					if rerr := os.RemoveAll(p); rerr != nil {
						failures = append(failures, fmt.Sprintf("#%s 转写 %s：%v", id, e.Name(), rerr))
						continue
					}
					removedCount++
				}
				if removedCount > 0 {
					entry.Transcripts = removedCount
					entry.FreedBytes += item.TranscriptsBytes
				}
			}
		}

		if numID, perr := strconv.ParseInt(id, 10, 64); perr == nil && numID > 0 {
			dbRes, derr := s.m.pg.PurgeTaskScopedData(r.Context(), numID, item.ExplorationID, dbScopes)
			if derr != nil {
				failures = append(failures, fmt.Sprintf("#%s 数据库清理：%v", id, derr))
			} else {
				rowsDeleted := dbRes.ActivityDeleted + dbRes.SideQuestionsDeleted +
					dbRes.LLMRecordBodiesCleared + dbRes.LLMRecordsDeleted +
					dbRes.LLMUsageDeleted + dbRes.ToolExecutionsDeleted + dbRes.BlackboardLogsDeleted
				entry.DBRowsDeleted = rowsDeleted
				if dbScopes.Activity && dbRes.ActivityDeleted > 0 {
					entry.FreedBytes += item.ActivityBytes
				}
				if dbScopes.SideQuestions && dbRes.SideQuestionsDeleted > 0 {
					entry.FreedBytes += item.SideQuestionBytes
				}
				if dbScopes.LLMRecords && dbRes.LLMRecordsDeleted > 0 {
					entry.FreedBytes += item.LLMRecordBytes
				} else if dbScopes.LLMRecordBodies && dbRes.LLMRecordBodiesCleared > 0 {
					entry.FreedBytes += item.LLMRecordBodyBytes
				}
				if dbScopes.LLMUsage && dbRes.LLMUsageDeleted > 0 {
					entry.FreedBytes += item.LLMUsageBytes
				}
				if dbScopes.ToolExecutions && dbRes.ToolExecutionsDeleted > 0 {
					entry.FreedBytes += item.ToolExecutionBytes
				}
				if dbScopes.BlackboardLogs && dbRes.BlackboardLogsDeleted > 0 {
					entry.FreedBytes += item.BlackboardLogBytes
				}
			}
		}

		totalFreed += entry.FreedBytes
		cleaned = append(cleaned, entry)
	}

	slogFor("storage").Info("按任务精确清理完成",
		"tasks", len(cleaned), "freed_bytes", totalFreed, "failed", len(failures))

	afterTasks, _ := s.buildStorageTasksReport(r.Context())
	afterGlobal, _ := s.storageReport(r.Context())
	writeJSON(w, 200, map[string]any{
		"cleaned":       cleaned,
		"freed_bytes":   totalFreed,
		"failures":      failures,
		"tasks_report":  afterTasks,
		"global_report": afterGlobal,
	})
}

func (s *Server) vacuumStorage(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		Full   bool     `json:"full"`
		Tables []string `json:"tables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	done, err := s.m.pg.VacuumStorageTables(r.Context(), req.Tables, req.Full)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	slogFor("storage").Info("数据库空间整理完成", "full", req.Full, "tables", len(done))
	rep, _ := s.storageReport(r.Context())
	writeJSON(w, 200, map[string]any{"vacuumed": done, "full": req.Full, "report": rep})
}
