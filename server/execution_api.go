package server

import (
	"net/http"
	"strconv"
)

// getExecution 返回一条长任务执行台账（时间线里 execution 事件点进去看的那条）。
//
// 只回台账字段：命令、状态、退出码、已抓取的输出尾部(partial_tail)、取消/失败原因。
// 不在这里读磁盘日志——那会把一个只读展示接口变成任意路径读取面；完整日志仍由
// 执行服务/工作目录管理。
func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	eid, err := strconv.ParseInt(r.PathValue("eid"), 10, 64)
	if err != nil || eid <= 0 {
		writeErr(w, 400, "bad execution id")
		return
	}
	if s.m.pg == nil {
		writeErr(w, 503, "execution ledger unavailable")
		return
	}
	row, err := s.m.pg.GetExecution(eid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if row == nil {
		writeErr(w, 404, "执行记录不存在")
		return
	}
	// 时间线总会带 task；带了就校验归属，避免一个任务的页面打开别的任务的执行。
	if task := r.URL.Query().Get("task"); task != "" && row.TaskID != task {
		writeErr(w, 404, "执行记录不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"execution": row})
}
