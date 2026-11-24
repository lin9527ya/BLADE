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

// taskMetadataPatch is the editable list-only metadata. Description/goal and
// lifecycle are deliberately absent: they are immutable or controlled elsewhere.
type taskMetadataPatch struct {
	Name                    *string `json:"name"`
	Pinned                  *bool   `json:"pinned"`
	Remark                  *string `json:"remark"`
	AssetScopePolicy        *string `json:"asset_scope_policy"`
	LootExecEnabled         *bool   `json:"loot_exec_enabled"`
	CredentialAttemptBudget *int    `json:"credential_attempt_budget"`
}

// updateTaskMetadata changes list-only task metadata. It intentionally does not
// touch lifecycle state, scheduling, or the task's immutable description/goal.
func (s *Server) updateTaskMetadata(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskMetadataRequestSize)
	var request taskMetadataPatch
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	task, status, err := s.updateTaskMetadataCore(taskID, request)
	if err != nil {
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, status, taskDTO(task, s.resolvedTaskStatus(task)))
}

// updateTaskMetadataCore is shared by the HTTP handler and IM /edit. The int is
// the HTTP status the handler should use; IM only consumes the error text.
func (s *Server) updateTaskMetadataCore(taskID string, request taskMetadataPatch) (*Task, int, error) {
	task, ok := s.m.Task(taskID)
	if !ok || task == nil {
		return nil, http.StatusNotFound, errors.New("task not found")
	}
	if request.Name == nil && request.Pinned == nil && request.Remark == nil && request.AssetScopePolicy == nil && request.LootExecEnabled == nil && request.CredentialAttemptBudget == nil {
		return nil, http.StatusBadRequest, errors.New("至少需要提供 name、pinned、remark、asset_scope_policy、loot_exec_enabled 或 credential_attempt_budget")
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			return nil, http.StatusBadRequest, errors.New("任务名称不能为空")
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			return nil, http.StatusBadRequest, fmt.Errorf("任务名称最多 %d 个字符", maxTaskNameRunes)
		}
		request.Name = &name
	}
	if request.Remark != nil {
		remark := strings.TrimSpace(*request.Remark)
		if utf8.RuneCountInString(remark) > db.MaxTaskRemarkRunes {
			return nil, http.StatusBadRequest, fmt.Errorf("任务备注最多 %d 个字符", db.MaxTaskRemarkRunes)
		}
		request.Remark = &remark
	}
	if request.CredentialAttemptBudget != nil && *request.CredentialAttemptBudget < -1 {
		return nil, http.StatusBadRequest, errors.New("credential_attempt_budget 必须 >= -1（-1=继承全局，0=不限）")
	}
	if request.AssetScopePolicy != nil {
		policy := strings.ToLower(strings.TrimSpace(*request.AssetScopePolicy))
		if err := db.ValidateAssetScopePolicy(policy); err != nil {
			return nil, http.StatusBadRequest, err
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
				return nil, http.StatusInternalServerError, err
			}
		}
		task.setAssetScopePolicy(db.NormalizeAssetScopePolicy(policy))
		s.m.interceptor.InvalidateTaskScope(id)
	}
	task, err := s.m.UpdateTaskMetadata(taskID, db.TaskPatch{
		Name: request.Name, Pinned: request.Pinned, Remark: request.Remark,
		LootExecEnabled:         request.LootExecEnabled,
		CredentialAttemptBudget: request.CredentialAttemptBudget,
	})
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if task == nil {
		return nil, http.StatusNotFound, errors.New("task not found")
	}
	return task, http.StatusOK, nil
}

func firstRoot(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}
