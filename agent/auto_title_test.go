package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/lin9527ya/Norma/llm"
)

// 标题必须清洗成单行：引号、"标题："前缀、换行、多余空白都要去掉——
// 它会直接出现在侧栏里。
func TestCleanAutoTitle(t *testing.T) {
	cases := map[string]string{
		`"Web 登录口 SQL 注入"`:      "Web 登录口 SQL 注入",
		"标题：后台弱口令排查":            "后台弱口令排查",
		"Title:   API 越权验证  ":   "API 越权验证",
		"第一行\n第二行":              "第一行",
		"“内网上线前资产梳理”":           "内网上线前资产梳理",
		strings.Repeat("长", 60): strings.Repeat("长", autoTitleMaxRunes),
		"  多个   空格  合并 ":        "多个 空格 合并",
		"":                      "",
	}
	for in, want := range cases {
		if got := cleanAutoTitle(in); got != want {
			t.Errorf("cleanAutoTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// 生成失败必须报错而不是返回半成品；空输入直接拒绝，不花模型调用。
func TestGenerateAutoTitleRejectsEmpty(t *testing.T) {
	if _, err := GenerateAutoTitle(context.Background(), stubProvider{}, "   "); err == nil {
		t.Fatal("空内容应被拒绝")
	}
	if _, err := GenerateAutoTitle(context.Background(), nil, "x"); err == nil {
		t.Fatal("没有 provider 时应报错")
	}
}

// 模型回一段无法清洗的内容时，生成应失败（调用方据此保持默认标题）。
func TestGenerateAutoTitleCleansModelOutput(t *testing.T) {
	title, err := GenerateAutoTitle(context.Background(), stubProvider{reply: "标题：\n后台入口排查"}, "帮我看看后台")
	if err != nil {
		t.Fatal(err)
	}
	if title != "后台入口排查" {
		t.Fatalf("标题清洗不对：%q", title)
	}
	if _, err := GenerateAutoTitle(context.Background(), stubProvider{err: errors.New("boom")}, "x"); err == nil {
		t.Fatal("provider 报错时应向上返回")
	}
}

type stubProvider struct {
	reply string
	err   error
}

func (s stubProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (s stubProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if s.err != nil {
		return llm.Message{}, "", llm.Usage{}, s.err
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(s.reply)}}, s.reply, llm.Usage{}, nil
}
