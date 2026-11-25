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
		`"Web 登录口 SQL 注入"`:    "Web 登录口 SQL 注入",
		"标题：后台弱口令排查":          "后台弱口令排查",
		"Title:   API 越权验证  ": "API 越权验证",
		"第一行\n第二行":            "第一行",
		"“内网上线前资产梳理”":         "内网上线前资产梳理",
		"<think>内部推理\n第二行</think>\n### **标题：Web 后台弱口令排查**\n附注": "Web 后台弱口令排查",
		"<thinking>思考内容</thinking>\n- `API 越权漏洞测试`":            "API 越权漏洞测试",
		"1. Kubernetes 集群基线检查":                                 "Kubernetes 集群基线检查",
		"192.168.1.10 端口扫描与指纹识别":                               "192.168.1.10 端口扫描与指纹识别",
		"-h 参数模糊测试排查":                                          "-h 参数模糊测试排查",
		"<think>截断前未输出正文":                                      "",
		strings.Repeat("长", 60):                                strings.Repeat("长", autoTitleMaxRunes),
		"  多个   空格  合并 ":                                       "多个 空格 合并",
		"":                                                     "",
	}
	for in, want := range cases {
		if got := cleanAutoTitle(in); got != want {
			t.Errorf("cleanAutoTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// 生成失败必须报错而不是返回半成品；空输入直接拒绝，不花模型调用。
func TestGenerateAutoTitleRejectsEmpty(t *testing.T) {
	if _, err := GenerateAutoTitle(context.Background(), &stubProvider{}, "   "); err == nil {
		t.Fatal("空内容应被拒绝")
	}
	if _, err := GenerateAutoTitle(context.Background(), nil, "x"); err == nil {
		t.Fatal("没有 provider 时应报错")
	}
}

// 模型回一段无法清洗的内容时，生成应失败（调用方据此保持默认标题）。
func TestGenerateAutoTitleCleansModelOutput(t *testing.T) {
	title, err := GenerateAutoTitle(context.Background(), &stubProvider{reply: "标题：\n后台入口排查"}, "帮我看看后台")
	if err != nil {
		t.Fatal(err)
	}
	if title != "后台入口排查" {
		t.Fatalf("标题清洗不对：%q", title)
	}
	if _, err := GenerateAutoTitle(context.Background(), &stubProvider{err: errors.New("boom")}, "x"); err == nil {
		t.Fatal("provider 报错时应向上返回")
	}
}

type stubProvider struct {
	reply    string
	err      error
	thinking bool
	lastReq  llm.CompletionRequest
}

func (s *stubProvider) PreservesThinking() bool { return s.thinking }

func (s *stubProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {}
}

func (s *stubProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	s.lastReq = req
	if s.err != nil {
		return llm.Message{}, "", llm.Usage{}, s.err
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(s.reply)}}, s.reply, llm.Usage{}, nil
}

func TestGenerateAutoTitleReasoningCompatibility(t *testing.T) {
	std := &stubProvider{reply: "常规端口探测任务"}
	got, err := GenerateAutoTitle(context.Background(), std, "扫描 10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "常规端口探测任务" {
		t.Fatalf("got = %q", got)
	}
	if std.lastReq.MaxTokens != 512 || std.lastReq.Temperature == nil || *std.lastReq.Temperature != 0 {
		t.Fatalf("standard request params = max:%d temp:%v, want max:512 temp:0", std.lastReq.MaxTokens, std.lastReq.Temperature)
	}

	reasoner := &stubProvider{
		thinking: true,
		reply:    "<reasoning>先总结主题</reasoning>\n**目标资产与登录口排查**",
	}
	got, err = GenerateAutoTitle(context.Background(), reasoner, "帮我看看目标站登录口")
	if err != nil {
		t.Fatal(err)
	}
	if got != "目标资产与登录口排查" {
		t.Fatalf("reasoner title = %q", got)
	}
	if reasoner.lastReq.MaxTokens < 2048 || reasoner.lastReq.Temperature != nil {
		t.Fatalf("reasoner request params = max:%d temp:%v, want max>=2048 temp:nil", reasoner.lastReq.MaxTokens, reasoner.lastReq.Temperature)
	}
}
