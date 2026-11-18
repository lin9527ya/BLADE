package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
)

const (
	maxTaskNameRunes           = 200
	maxTaskMetadataRequestSize = 16 << 10
)

// updateTaskMetadata changes list-only task metadata. It intentionally does not
// touch lifecycle state, scheduling, or the task's immutable description/goal.
func (s *Server) updateTaskMetadata(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	task, ok := s.m.Task(taskID)
	if !ok || task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskMetadataRequestSize)
	var request struct {
		Name             *string `json:"name"`
		Pinned           *bool   `json:"pinned"`
		Remark           *string `json:"remark"`
		AssetScopePolicy *string `json:"asset_scope_policy"`
	}
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if request.Name == nil && request.Pinned == nil && request.Remark == nil && request.AssetScopePolicy == nil {
		writeErr(w, http.StatusBadRequest, "至少需要提供 name、pinned、remark 或 asset_scope_policy")
		return
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "任务名称不能为空")
			return
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("任务名称最多 %d 个字符", maxTaskNameRunes))
			return
		}
		request.Name = &name
	}
	if request.Remark != nil {
		remark := strings.TrimSpace(*request.Remark)
		if utf8.RuneCountInString(remark) > db.MaxTaskRemarkRunes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("任务备注最多 %d 个字符", db.MaxTaskRemarkRunes))
			return
		}
		request.Remark = &remark
	}
	if request.AssetScopePolicy != nil {
		policy := strings.ToLower(strings.TrimSpace(*request.AssetScopePolicy))
		if err := db.ValidateAssetScopePolicy(policy); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		id, _ := strconv.ParseInt(taskID, 10, 64)
		var roots []string
		if _, host, _, ok := parseTarget(task.Description + " " + task.Goal); ok && host != "" && net.ParseIP(host) == nil {
			roots = append(roots, host)
		}
		companies := append([]int64(nil), task.CompanyIDs...)
		if as := s.m.Assets(); as != nil {
			if host := firstRoot(roots); host != "" {
				if cid, found, err := as.ResolveCompanyForHost(host); err == nil && found {
					companies = append(companies, cid)
				}
			}
			if _, err := as.SetTaskAssetScopePolicy(id, policy, roots, companies); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		task.setAssetScopePolicy(db.NormalizeAssetScopePolicy(policy))
		s.m.interceptor.InvalidateTaskScope(id)
	}
	task, err := s.m.UpdateTaskMetadata(taskID, db.TaskPatch{Name: request.Name, Pinned: request.Pinned, Remark: request.Remark})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}

func firstRoot(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}
