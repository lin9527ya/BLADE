package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lin9527ya/Norma/llm"
	actool "github.com/lin9527ya/Norma/tool"
)

// failureHintTool 给**失败**的工具结果追加一行「下一步可以怎么改」。
//
// 此前模型只看到 `[exit code 1]` 和一大段原文：命令不存在就去猜、权限不足就反复重试、
// 超时就一直等。guard 的 postToolUse 其实已经做了 blocked/error 分类，但 Norma 的
// harness 会丢弃那个返回值，所以改 guard 没用——只能做成工具装饰器。
//
// 只在 IsError 时追加：成功结果里出现的 "permission denied" 很可能只是扫描到的内容，
// 不能当成失败信号。
type failureHintTool struct{ actool.CoreTool }

func (t *failureHintTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	res, err := t.CoreTool.Call(ctx, in, tc)
	if err == nil && res.IsError {
		if hint := failureHint(res.Flatten()); hint != "" {
			res.Content = append(res.Content, llm.TextBlock("\n"+hint))
		}
	}
	return res, err
}

// WithFailureHint 给执行类工具（目前是 Bash）挂上失败提示装饰器。
func WithFailureHint(t actool.CoreTool) actool.CoreTool { return &failureHintTool{CoreTool: t} }

// failureHint 把常见的失败形态映射成一句可执行的下一步。只认**明确的**失败特征，
// 宁可少提示，也不要给一句牛头不对马嘴的建议。
func failureHint(text string) string {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(text, "[exit code 127]") || strings.Contains(low, "command not found"):
		return "提示：命令不存在。换用已安装的同类工具，或先 which <cmd> 确认路径。"
	case strings.Contains(low, "permission denied"):
		return "提示：权限不足。换一条不需要该权限的路径，或先确认当前身份与可写位置。"
	case strings.Contains(low, "connection refused"):
		return "提示：连接被拒。先确认端口是否开放、服务是否在该地址监听，再重试。"
	case strings.Contains(low, "no such file or directory"):
		return "提示：路径不存在。先列出父目录确认实际位置，别猜路径。"
	case strings.Contains(low, "timed out") || strings.Contains(low, "timeout"):
		return "提示：执行超时。长任务改用 run_execution 后台跑，再用 get_execution 取输出。"
	case strings.Contains(text, "[exit code "):
		return "提示：命令非零退出。先读上面的报错定位原因，别原样重试。"
	}
	return ""
}
