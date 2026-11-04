package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const reviewTextLimit = 4000

// maxReviewScriptBytes 是单个脚本快照与整次调用快照总量的上限（1 MiB）。
// 超过就标 incomplete，由策略决定转人工还是交给模型判断——「截断着审」比「不审」危险。
const maxReviewScriptBytes = 1 << 20

const BackgroundUserMessage = "user_message"

// ReviewBackground is explicitly bound from the current human message. Generated
// Worker summaries are not accepted. Background cannot override review policy.
type ReviewBackground struct {
	Source    string `json:"source"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ReviewScript 是本次工具调用将要执行（或刚刚生成）的脚本快照。裁判只看得到
// 工具参数，而 `bash x.sh`、heredoc 生成脚本、`sed -i` 改文件的实际内容都在参数之外——
// 不附上这些，审批等于只看命令名。
type ReviewScript struct {
	Path      string `json:"path"`
	Source    string `json:"source,omitempty"` // file | command；command 表示尚未落盘
	SHA256    string `json:"sha256,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Content   string `json:"content,omitempty"`
	Complete  bool   `json:"complete"`
	Truncated bool   `json:"truncated,omitempty"`
	ReadError string `json:"read_error,omitempty"`
}

// ReviewInput contains only the current call and explicitly selected background.
// Execution history and call correlation belong to the separate audit record.
type ReviewInput struct {
	Version    int               `json:"version"`
	WorkingDir string            `json:"working_directory,omitempty"`
	Background *ReviewBackground `json:"background,omitempty"`
	Tool       string            `json:"tool_name"`
	Arguments  json.RawMessage   `json:"arguments"`
	// ScriptCaptureStatus 是脚本快照的完整性：not_applicable（不是执行脚本的调用）|
	// complete | incomplete。incomplete 时按 llm_judge_incomplete_script_action 处置。
	ScriptCaptureStatus string         `json:"script_capture_status,omitempty"`
	Scripts             []ReviewScript `json:"script_files,omitempty"`
}

type reviewContextKey struct{}
type reviewEnvironment struct {
	workingDir string
	background ReviewBackground
}

// WithReviewContext explicitly binds the permitted background for one run. Never
// fall back to the raw turn transcript: it may contain the full scheduler prompt.
// This is application wiring, not a model-callable tool.
func WithReviewContext(ctx context.Context, workingDir string, background ReviewBackground) context.Context {
	return context.WithValue(ctx, reviewContextKey{}, reviewEnvironment{workingDir, background})
}

// WithReviewWorkingDirectory preserves only explicitly selected background.
// Chat runs can be human-initiated or scheduled, so the Agent must not infer
// message provenance from the text it receives.
func WithReviewWorkingDirectory(ctx context.Context, workingDir string) context.Context {
	env, _ := ctx.Value(reviewContextKey{}).(reviewEnvironment)
	env.workingDir = workingDir
	return context.WithValue(ctx, reviewContextKey{}, env)
}

func BuildReviewInput(ctx context.Context, tool string, arguments json.RawMessage) (ReviewInput, error) {
	if !json.Valid(arguments) {
		return ReviewInput{}, fmt.Errorf("工具参数不是有效 JSON")
	}
	in := ReviewInput{Version: 4, Tool: tool, Arguments: append(json.RawMessage(nil), arguments...)}
	if env, ok := ctx.Value(reviewContextKey{}).(reviewEnvironment); ok {
		in.WorkingDir = env.workingDir
		background := env.background
		if background.Source == BackgroundUserMessage && strings.TrimSpace(background.Text) != "" {
			var cut bool
			background.Text, cut = bounded(background.Text, reviewTextLimit)
			background.Truncated = background.Truncated || cut
			in.Background = &background
		}
	}
	in.Scripts, in.ScriptCaptureStatus = captureScripts(in.WorkingDir, tool, arguments)
	return in, nil
}
