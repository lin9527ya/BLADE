package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
)

const (
	maxFindingAssigneeRunes   = 120
	maxFindingRemediationRune = 8000
	maxFindingCommentRunes    = 4000
	maxFindingCVEIDs          = 20
)

func (s *Server) enrichFindingCollab(dto *FindingDTO, id int64) {
	c, err := s.m.pg.GetFindingCollab(id)
	if err != nil || c == nil {
		return
	}
	dto.Assignee = c.Assignee
	dto.CVSSScore = c.CVSSScore
	dto.CVEIDs = c.CVEIDs
	dto.Remediation = c.Remediation
	dto.Comments = c.Comments
	if c.DueAt != nil {
		dto.DueAt = rfc3339(*c.DueAt)
	}
}

func parseFindingDueAt(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, errors.New("due_at 必须是 RFC3339 或 YYYY-MM-DD")
	}
	// A date-only due date means end of that day, not 00:00 (which reads as overdue
	// for the whole day it is due).
	t = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
	return &t, nil
}

func normalizeFindingCVEIDs(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		id := db.NormalizeCVEID(item)
		if id == "" {
			continue
		}
		if !db.ValidCVEID(id) {
			return nil, errors.New("CVE 编号格式无效：" + id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) > maxFindingCVEIDs {
			return nil, errors.New("关联 CVE 最多 20 个")
		}
	}
	return out, nil
}

// findingComments returns the collaboration log for one finding.
func (s *Server) findingComments(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	comments, err := s.m.pg.ListFindingComments(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"comments": comments})
}

func (s *Server) addFindingComment(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" {
		writeErr(w, 400, "评论内容不能为空")
		return
	}
	if utf8.RuneCountInString(body.Body) > maxFindingCommentRunes {
		writeErr(w, 400, "评论最多 4000 个字符")
		return
	}
	if f, err := s.m.pg.GetFinding(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	} else if f == nil {
		writeErr(w, 404, "finding not found")
		return
	}
	comment, err := s.m.pg.AddFindingComment(id, "operator", body.Body)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, comment)
}

func (s *Server) deleteFindingComment(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	commentID := int64(atoiDefault(r.PathValue("comment_id"), 0))
	if id <= 0 || commentID <= 0 {
		writeErr(w, 400, "bad finding or comment id")
		return
	}
	n, err := s.m.pg.DeleteFindingComment(id, commentID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "comment not found")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}
