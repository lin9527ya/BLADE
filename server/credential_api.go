package server

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/lin9527ya/blade/db"
)

// 凭据明文的**受控入口**（P36）。
//
// 为什么要有这么一个入口，而不是让界面直接显示 payload 里的 secret：报告、交付包、概览、
// 提示词、日志里都不该出现明文（它们会被转发、被折进上下文），而"人要看一眼"是真实需求。
// 两者的折中是——**明文只在有人主动点开时出，且那一次留痕**：
//
//	· 节点详情默认只给指纹（sha256 前 8 位），可指认、不可反推；
//	· 点「显示明文」调这个接口，返回原文，同时往任务时间线写一条「查看凭据明文（指纹 xxx）」，
//	  并记下请求来源地址——事后能回答"这条口令是谁、什么时候看过"。
//
// 不把它做成 GET：它**有副作用**（写留痕），GET 该是幂等可缓存的读。
func (s *Server) revealCredential(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("node")
	}
	task := s.m.ResolveTask(r.URL.Query().Get("task"))
	if task == nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	nodeID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || nodeID <= 0 {
		writeErr(w, 400, "凭据节点 id 不合法")
		return
	}
	node, err := task.Store.GetNode(nodeID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindCredential {
		writeErr(w, 404, "该节点不是本任务里的凭据")
		return
	}
	v := db.ParseCredential(node)
	fp := v.Fingerprint()

	// 留痕：这是本接口存在的理由之一，所以**先写痕再回包**——回包失败也要留下"有人看过"。
	s.engine.emitActivity(task, db.Activity{
		Worker: "system", Kind: "text",
		Summary: fmt.Sprintf("查看凭据明文（指纹 %s，来源 %s）", nzStr(fp, "无指纹"), clientAddr(r)),
	})
	log.Printf("[credential] 明文查看：task#%s node=%d 指纹=%s 来源=%s", task.ID, nodeID, nzStr(fp, "无"), clientAddr(r))

	resp := map[string]any{
		"id": nodeID, "kind": v.Kind, "username": v.Username, "secret": v.Secret,
		"fingerprint": fp, "summary": v.Summary,
		"revealed_by": clientAddr(r),
	}
	if v.Secret == "" {
		resp["note"] = "这条凭据没有登记本体（只记了身份/来源），没有可显示的明文。"
	}
	writeJSON(w, 200, resp)
}

func nzStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
