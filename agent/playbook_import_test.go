package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
)

type playbookImportFakeProvider struct {
	reply string
	err   error
}

func (p playbookImportFakeProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if p.err != nil {
		return llm.Message{}, "", llm.Usage{}, p.err
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(p.reply)}}, "end_turn", llm.Usage{}, nil
}

func (p playbookImportFakeProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) {}
}

func TestExtractPlaybooksFromMarkdown(t *testing.T) {
	reply := "```json\n" + `{
		"playbooks": [
			{"title":" Shiro RememberMe 反序列化 ","summary":"适合暴露 Shiro 的站点","technique_key":"Shiro RememberMe","steps":"## 识别\n看 rememberMe cookie","evidence":"GET / HTTP/1.1","confidence":"high"},
			{"title":"","steps":"missing title"},
			{"title":"同手法重复卡","steps":"重复步骤","technique_key":"shiro-rememberme","confidence":"low"},
			{"title":"另一套独立打法","steps":"另一套步骤","technique_key":"other-technique","confidence":"medium"}
		],
		"warnings":["文档未给出目标版本"]
	}` + "\n```"
	drafts, warnings, err := ExtractPlaybooksFromMarkdown(context.Background(), playbookImportFakeProvider{reply: reply}, "doc.md", "# 文档\n内容")
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 {
		t.Fatalf("drafts=%d: %+v", len(drafts), drafts)
	}
	if drafts[0].Title != "Shiro RememberMe 反序列化" || drafts[0].TechniqueKey != "shiro-rememberme" {
		t.Fatalf("normalization failed: %+v", drafts[0])
	}
	if drafts[0].Confidence != "low" || drafts[1].Confidence != "low" {
		t.Fatalf("imported drafts must be low confidence: %+v", drafts)
	}
	if drafts[1].Title != "另一套独立打法" || drafts[1].TechniqueKey != "other-technique" {
		t.Fatalf("independent playbook missing: %+v", drafts)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "版本") {
		t.Fatalf("warnings lost: %+v", warnings)
	}
}

func TestExtractPlaybooksFromMarkdownBareArray(t *testing.T) {
	reply := `[{"title":"裸数组卡片","technique_key":"bare-array","steps":"步骤","confidence":"medium"}]`
	drafts, _, err := ExtractPlaybooksFromMarkdown(context.Background(), playbookImportFakeProvider{reply: reply}, "", "内容")
	if err != nil || len(drafts) != 1 || drafts[0].Confidence != "low" {
		t.Fatalf("bare array: %+v %v", drafts, err)
	}
}

func TestExtractPlaybooksFromMarkdownErrors(t *testing.T) {
	if _, _, err := ExtractPlaybooksFromMarkdown(context.Background(), playbookImportFakeProvider{reply: `not json`}, "x.md", "内容"); err == nil {
		t.Fatal("invalid model output should fail")
	}
	if _, _, err := ExtractPlaybooksFromMarkdown(context.Background(), playbookImportFakeProvider{err: errors.New("boom")}, "x.md", "内容"); err == nil {
		t.Fatal("provider error should propagate")
	}
	if _, _, err := ExtractPlaybooksFromMarkdown(context.Background(), nil, "x.md", "内容"); err == nil {
		t.Fatal("nil provider should fail")
	}
	if _, _, err := ExtractPlaybooksFromMarkdown(context.Background(), playbookImportFakeProvider{reply: `{"playbooks":[]}`}, "x.md", "  "); err == nil {
		t.Fatal("empty markdown should fail")
	}
}
