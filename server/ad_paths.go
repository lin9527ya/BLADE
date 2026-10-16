package server

import (
	"net/http"
	"strconv"

	"github.com/lin9527ya/blade/db"
)

// AD 攻击路径的 HTTP 面（P32）：读任务图上的域内路径。
//
// 路径重建本身在 db（db/ad_paths.go）——任务页、报告、agent 工具三处读的是同一份实现。
// 这里只负责取数、装配 DTO、回包。

// adPathsResponse 是 AD 路径视图的数据。paths/stats/starts 与报告、agent 工具同源。
type adPathsResponse struct {
	Paths   []db.ADPath    `json:"paths"`
	Stats   map[string]any `json:"stats"`
	Starts  []db.ADStart   `json:"starts"`
	StartID int64          `json:"start_id,omitempty"`
}

// taskADPaths 返回某个任务图上到高价值目标的域内路径。
func (s *Server) taskADPaths(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("task")
	}
	t := s.m.ResolveTask(id)
	if t == nil {
		writeJSON(w, 200, adPathsResponse{Paths: []db.ADPath{}, Stats: map[string]any{}, Starts: []db.ADStart{}})
		return
	}
	startID, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	resp, err := adPathsFor(t.Store, startID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, resp)
}

// adPathsFor 装配一次路径读：图 + 路径 + 统计 + 起点候选。
//
// **域内与云上一起读**：两者共用同一套边（member_of / can），路径算法也是同一条
// （db/ad_paths.go）。分开读会得到一个荒唐的结果——同一条链上只要跨了域/云边界就断掉，
// 而现实里"从一台机器上的凭据到云上 admin 角色"正是要连起来看的那种路径。
func adPathsFor(store *db.ExplorationStore, startID int64) (adPathsResponse, error) {
	var nodes []*db.Node
	for _, kind := range []string{db.KindADObject, db.KindCloudObject} {
		ns, err := store.ListByKind(kind, 20000)
		if err != nil {
			return adPathsResponse{}, err
		}
		nodes = append(nodes, ns...)
	}
	edges, err := store.Edges(20000)
	if err != nil {
		return adPathsResponse{}, err
	}
	res := db.ADPaths(nodes, edges, startID)
	return adPathsResponse{
		Paths:  res.Paths,
		Starts: res.Starts,
		Stats: map[string]any{
			"objects": res.Objects, "edges": res.Edges,
			"nodes": res.Nodes, "highvalue": res.HighValue,
		},
		StartID: startID,
	}, nil
}

// adPathsForReport 给报告用：与任务页同一份实现，只是不需要统计与起点候选。
func adPathsForReport(store *db.ExplorationStore) []db.ADPath {
	resp, err := adPathsFor(store, 0)
	if err != nil {
		return nil
	}
	return resp.Paths
}
