package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CompanyKnowledge 是一条公司级知识（P23）：跨任务、只读背景。
type CompanyKnowledge struct {
	ID          int64  `json:"id"`
	CompanyID   int64  `json:"company_id"`
	CompanyName string `json:"company_name,omitempty"`
	Kind        string `json:"kind"`
	Summary     string `json:"summary"`
	Detail      string `json:"detail,omitempty"`
	// SourceTaskID / SourceNodeID：来源可回溯（节点可能属于上游任务）。
	SourceTaskID string     `json:"source_task_id,omitempty"`
	SourceNodeID int64      `json:"source_node_id,omitempty"`
	Confidence   string     `json:"confidence,omitempty"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	DeprecatedAt *time.Time `json:"deprecated_at,omitempty"`
	// DeprecatedReason 是废弃理由（人话；与发现撤回同款纪律：留下"为什么不算数了"）。
	DeprecatedReason string `json:"deprecated_reason,omitempty"`
}

// 公司知识的 kind 封闭词表。
const (
	KnowledgeKindFact         = "fact"
	KnowledgeKindConclusion   = "conclusion"
	KnowledgeKindAssetHint    = "asset_hint"
	KnowledgeKindPlaybookHint = "playbook_hint"
)

// KnowledgeKinds 是全部合法 kind（写入侧白名单）。
var KnowledgeKinds = []string{KnowledgeKindFact, KnowledgeKindConclusion, KnowledgeKindAssetHint, KnowledgeKindPlaybookHint}

// NormalizeKnowledgeKind 归一 kind；未知值落到 fact（写入侧另有严格校验）。
func NormalizeKnowledgeKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	for _, v := range KnowledgeKinds {
		if k == v {
			return v
		}
	}
	return ""
}

// NormalizeConfidence 归一置信度；未知/空 → ""（不猜）。
func NormalizeConfidence(c string) string {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "high", "medium", "low":
		return strings.ToLower(strings.TrimSpace(c))
	}
	return ""
}

const knowledgeCols = `k.id, k.company_id, COALESCE(c.name,''), k.kind, k.summary, COALESCE(k.detail,''),
COALESCE(k.source_task_id,''), k.source_node_id, COALESCE(k.confidence,''), k.status, k.created_at,
k.deprecated_at, COALESCE(k.deprecated_reason,'')`

const knowledgeFrom = ` FROM company_knowledge k LEFT JOIN companies c ON c.id=k.company_id`

func scanKnowledge(sc interface{ Scan(...any) error }) (*CompanyKnowledge, error) {
	var k CompanyKnowledge
	if err := sc.Scan(&k.ID, &k.CompanyID, &k.CompanyName, &k.Kind, &k.Summary, &k.Detail,
		&k.SourceTaskID, &k.SourceNodeID, &k.Confidence, &k.Status, &k.CreatedAt,
		&k.DeprecatedAt, &k.DeprecatedReason); err != nil {
		return nil, err
	}
	return &k, nil
}

// CreateCompanyKnowledge 沉淀一条公司知识（显式调用；调用方负责校验公司存在与来源可见）。
func (s *AssetStore) CreateCompanyKnowledge(k CompanyKnowledge) (*CompanyKnowledge, error) {
	kind := NormalizeKnowledgeKind(k.Kind)
	if kind == "" {
		kind = KnowledgeKindFact
	}
	var id int64
	if err := s.db.QueryRow(`
INSERT INTO company_knowledge(company_id, kind, summary, detail, source_task_id, source_node_id, confidence)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		k.CompanyID, kind, k.Summary, k.Detail, k.SourceTaskID, k.SourceNodeID,
		NormalizeConfidence(k.Confidence)).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetCompanyKnowledge(id)
}

// GetCompanyKnowledge 按 id 取一条（不存在 → nil, nil）。
func (s *AssetStore) GetCompanyKnowledge(id int64) (*CompanyKnowledge, error) {
	k, err := scanKnowledge(s.db.QueryRow(`SELECT `+knowledgeCols+knowledgeFrom+` WHERE k.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

// ListCompanyKnowledge 取这些公司**生效中**的知识（注入用：只读背景知识，不含已废弃）。
// limit ≤0 → 20（注入面刻意收紧，与常驻索引同一条纪律）。
func (s *AssetStore) ListCompanyKnowledge(companyIDs []int64, limit int) ([]CompanyKnowledge, error) {
	if len(companyIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+knowledgeCols+knowledgeFrom+`
WHERE k.company_id = ANY($1) AND k.status='active'
ORDER BY k.id DESC LIMIT $2`, companyIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompanyKnowledge{}
	for rows.Next() {
		k, err := scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// ListCompanyKnowledgeAll 取某公司的全部知识（含已废弃；UI 展示与"恢复"用）。
func (s *AssetStore) ListCompanyKnowledgeAll(companyID int64, limit int) ([]CompanyKnowledge, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+knowledgeCols+knowledgeFrom+`
WHERE k.company_id=$1 ORDER BY k.id DESC LIMIT $2`, companyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompanyKnowledge{}
	for rows.Next() {
		k, err := scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// SetCompanyKnowledgeStatus 废弃/恢复：deprecated 必须给理由（学发现撤回：
// "为什么不算数了"是后来人最需要的信息）；恢复则清空理由。
func (s *AssetStore) SetCompanyKnowledgeStatus(id int64, status, reason string) (*CompanyKnowledge, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "active" && status != "deprecated" {
		return nil, fmt.Errorf("状态只能是 active 或 deprecated")
	}
	var err error
	switch status {
	case "deprecated":
		_, err = s.db.Exec(`UPDATE company_knowledge SET status='deprecated', deprecated_at=now(), deprecated_reason=$2 WHERE id=$1`, id, reason)
	case "active":
		_, err = s.db.Exec(`UPDATE company_knowledge SET status='active', deprecated_at=NULL, deprecated_reason='' WHERE id=$1`, id)
	}
	if err != nil {
		return nil, err
	}
	return s.GetCompanyKnowledge(id)
}

// CountCompanyKnowledge 数某公司生效中的知识条数（UI 角标用）。
func (s *AssetStore) CountCompanyKnowledge(companyID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM company_knowledge WHERE company_id=$1 AND status='active'`, companyID).Scan(&n)
	return n, err
}
