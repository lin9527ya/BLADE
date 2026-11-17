package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/lin9527ya/blade/db"
)

type AssetDetailTask struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
}

type AssetDetailCompany struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Logo string `json:"logo,omitempty"`
}

type AssetDetailDTO struct {
	Asset         *db.Asset           `json:"asset"`
	Company       *AssetDetailCompany `json:"company,omitempty"`
	Tasks         []AssetDetailTask   `json:"tasks"`
	Neighbors     []db.AssetNeighbor  `json:"neighbors"`
	Findings      []FindingDTO        `json:"findings"`
	FindingsTotal int                 `json:"findings_total"`
	Refs          []db.AssetRef       `json:"refs"`
	Sources       []db.AssetSource    `json:"sources"`
}

// getAssetDetail is the investigation landing page for one asset: identity,
// owning company, tasks that touched it, direct neighbors, findings and graph
// references. All reads are existing stores; no schema change.
func (s *Server) getAssetDetail(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid asset id")
		return
	}
	taskID, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	rows, err := as.GetByIDsInScope(taskID, []int64{id})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(rows) == 0 {
		writeErr(w, 404, "asset not found")
		return
	}
	asset := rows[0]
	out := AssetDetailDTO{
		Asset:     asset,
		Tasks:     []AssetDetailTask{},
		Neighbors: []db.AssetNeighbor{},
		Findings:  []FindingDTO{},
		Refs:      []db.AssetRef{},
	}

	if asset.CompanyID != nil {
		if c, err := s.m.PG().Companies().GetCompany(*asset.CompanyID); err == nil && c != nil {
			logo := ""
			if c.Logo != nil {
				logo = *c.Logo
			}
			out.Company = &AssetDetailCompany{ID: c.ID, Name: c.Name, Logo: logo}
		}
	}

	tasks, err := s.m.PG().TasksByIDs(asset.TaskIDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, t := range tasks {
		status := t.Status
		if t.Paused {
			status += " · 已暂停"
		} else if t.Queued {
			status += " · 排队中"
		}
		out.Tasks = append(out.Tasks, AssetDetailTask{
			ID:          i64s(t.ID),
			Name:        t.Name,
			Description: searchClip(t.Description, 200),
			Status:      status,
		})
	}

	out.Sources, err = as.AssetSources(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out.Neighbors, err = as.AssetNeighbors(taskID, id, 12)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	filter := db.FindingFilter{AssetScope: fmt.Sprintf("a:%d", id), Sort: "severity"}
	findings, total, err := s.m.PG().ListFindingsPage(filter, 1, 20)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out.FindingsTotal = total
	assetMap := s.resolveFindingAssets(findings)
	for _, f := range findings {
		out.Findings = append(out.Findings, findingFromDB(f, assetMap))
	}

	seen := map[int64]bool{}
	addRefs := func(refs []db.AssetRef) {
		for _, ref := range refs {
			if seen[ref.ID] || len(out.Refs) >= 100 {
				continue
			}
			seen[ref.ID] = true
			out.Refs = append(out.Refs, ref)
		}
	}
	if taskID > 0 {
		if t, err := s.m.PG().GetTask(taskID); err == nil && t != nil {
			if refs, err := s.m.PG().Exploration(t.ExplorationID).AssetRefsWithSources(id); err == nil {
				addRefs(refs)
			}
		}
	} else {
		for _, t := range tasks {
			refs, err := s.m.PG().Exploration(t.ExplorationID).AssetRefsWithSources(id)
			if err != nil {
				continue
			}
			addRefs(refs)
		}
	}

	writeJSON(w, 200, out)
}
