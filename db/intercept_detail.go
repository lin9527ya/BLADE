package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// InterceptContextEntry is a bounded, recorded session event, not model reasoning.
type InterceptContextEntry struct {
	Kind      string `json:"kind"`
	Tool      string `json:"tool,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Text      string `json:"text"`
	IsError   bool   `json:"is_error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// InterceptAudit is captured at review time. It is deliberately excluded from
// polling/list responses; old rows have no audit instead of reconstructed data.
type InterceptAudit struct {
	RunID            string                  `json:"run_id,omitempty"`
	ToolUseID        string                  `json:"tool_use_id,omitempty"`
	Correlation      string                  `json:"correlation"` // exact | ambiguous | unavailable
	InputDigest      string                  `json:"input_digest"`
	UserMessage      string                  `json:"user_message"`
	UserTruncated    bool                    `json:"user_truncated,omitempty"`
	Context          []InterceptContextEntry `json:"context"`
	ContextTruncated bool                    `json:"context_truncated,omitempty"`
	CapturedAt       time.Time               `json:"captured_at"`
	ModelFallback    bool                    `json:"model_fallback,omitempty"`
	ModelInput       json.RawMessage         `json:"model_input,omitempty"`
	ModelInputDigest string                  `json:"model_input_digest,omitempty"`
	InitialAction    string                  `json:"initial_action"`
	InitialReason    string                  `json:"initial_reason"`
	EffectiveAction  string                  `json:"effective_action,omitempty"`
	DecisionReason   string                  `json:"decision_reason,omitempty"`
	RuleName         string                  `json:"rule_name,omitempty"`
	ConfigDigest     string                  `json:"config_digest,omitempty"`
	// MatchedSurface / MatchedText：规则命中的具体表面（raw | nested_string |
	// nested_key | percent_decoded:N | tool_name）与命中片段——P12 遮挡面，
	// 让「为什么命中/为什么没命中」可复盘。
	MatchedSurface string `json:"matched_surface,omitempty"`
	MatchedText    string `json:"matched_text,omitempty"`
	// ModelVerdict：结构化裁判（P15）打出的各轴把握与建议，一行 JSON —— 让
	// "模型当时怎么判的、代码为什么这么定"都可复盘。ModelReply 是模型原始回复
	// （截断），解析失败时它就是唯一的线索。
	ModelVerdict     string     `json:"model_verdict,omitempty"`
	ModelReply       string     `json:"model_reply,omitempty"`
	ProfileID        int64      `json:"profile_id,omitempty"`
	ExecutionStatus  string     `json:"execution_status"`
	Output           string     `json:"output,omitempty"`
	OutputTruncated  bool       `json:"output_truncated,omitempty"`
	ExecutionEndedAt *time.Time `json:"execution_ended_at,omitempty"`
}

type InterceptDetail struct {
	InterceptApprovalRow
	Audit *InterceptAudit `json:"audit"`
}

func (d *DB) GetInterceptDetail(id int64) (*InterceptDetail, error) {
	var out InterceptDetail
	var raw []byte
	err := d.QueryRow(approvalRowSelectWithAudit+` WHERE ip.id=$1`, id).Scan(
		&out.ID, &out.RuleID, &out.ConversationID, &out.TaskID, &out.AgentName,
		&out.ToolName, &out.ToolInput, &out.Status, &out.Reason, &out.DecidedAt, &out.CreatedAt,
		&out.DecisionSource, &out.ConvTitle, &out.ConvAgentKey, &out.RuleName, &raw,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Audit); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// ResolveIntercept atomically settles a pending request. A timeout cannot
// overwrite a human decision and repeat decisions cannot rewrite history.
func (d *DB) ResolveIntercept(id int64, status, action, reason string) (bool, error) {
	return d.ResolveInterceptWith(id, status, action, reason, nil)
}

// ResolveInterceptWith is ResolveIntercept plus audit fields for this decision —
// P15 用它把「人工改参放行」的收窄结果（edited_input / narrow_diff）一并写进
// audit，事后再看这条审批时，"到底放行了哪份参数"是可核对的。
func (d *DB) ResolveInterceptWith(id int64, status, action, reason string, extra map[string]any) (bool, error) {
	execution := "not_executed"
	if action == "allow" {
		execution = "awaiting_result"
	}
	fields := map[string]any{
		"effective_action": action, "decision_reason": reason, "execution_status": execution,
	}
	for k, v := range extra {
		fields[k] = v
	}
	patch, err := json.Marshal(fields)
	if err != nil {
		return false, err
	}
	r, err := d.Exec(`UPDATE intercept_pending SET status=$2, decided_at=NOW(),
		audit=CASE WHEN audit IS NULL THEN NULL ELSE audit || $3::jsonb ||
        CASE WHEN $4='allow' AND audit->>'correlation' IS DISTINCT FROM 'exact'
        THEN '{"execution_status":"unknown"}'::jsonb ELSE '{}'::jsonb END END
        WHERE id=$1 AND status='pending'`, id, status, patch, action)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// CompleteIntercept only updates the exact recorded call after it was allowed.
// A blocked tool_result must never be presented as a failed execution.
func (d *DB) CompleteIntercept(id int64, runID, toolUseID, status, output string, truncated bool) error {
	patch, err := json.Marshal(map[string]any{
		"execution_status": status, "output": output, "output_truncated": truncated,
		"execution_ended_at": time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE intercept_pending SET audit=audit || $4::jsonb
		WHERE id=$1 AND audit->>'run_id'=$2 AND audit->>'tool_use_id'=$3
		AND audit->>'effective_action'='allow' AND audit->>'execution_status'='awaiting_result'`,
		id, runID, toolUseID, patch)
	return err
}
