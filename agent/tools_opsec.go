package agent

import (
	"context"
	"encoding/json"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/blade/opsec"
)

// opsec_notes（P38）：这一手**预计**会留下什么。
//
// 它是「事前」的那一半，与 report_detection_check（P31，事后、有证据地记下"有没有被检出"）
// 刻意分开——两者混起来就是把预期读成实测。给 agent 这个读口的意义在于：规划层派意图、worker
// 挑手法时能先看一眼"这一手在目标上会落在哪"，而不是打完才想起来问。
//
// 它不替任何人做决定，也不教规避：降暴露在本仓是另一套机制（授权范围、作业时间窗、三层拦截、
// 人工审批）。命中不了时**明说没收录**——"没有提示"被读成"没有日志"是这功能唯一的失败方式。
func (t *ToolSet) opsecNotes() actool.CoreTool {
	return readTool(
		"opsec_notes",
		"查一条命令的**预计**可检出性：它会留在哪（目标系统日志 / EDR / 网络侧 / 应用日志 / 本机痕迹）、"+
			"具体信号（Windows 事件 ID、sysmon、auditd…）与预计可见度（低/中/高）。\n"+
			"用在**动手之前**：挑手法、写意图、判断这一步要不要先和客户/蓝队打招呼时。\n"+
			"注意三点：①这是知识库给的**预期**，不是实测——实测结论用 report_detection_check 记；"+
			"②知识库没收录时会明确说「没收录」，**那不代表这条没有日志**，只代表我们没有依据；"+
			"③它不给规避手法：降暴露在本平台走授权范围、作业时间窗与人工审批。",
		obj(map[string]any{
			"command":   str("要查的命令原文（一行即可；命中按词边界匹配）"),
			"technique": str("ATT&CK 技术 ID（T#### / T####.###）；与 command 二选一，用于查该技术的预计信号"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Command   string `json:"command"`
				Technique string `json:"technique"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			a.Command = strings.TrimSpace(a.Command)
			a.Technique = strings.TrimSpace(a.Technique)
			switch {
			case a.Command == "" && a.Technique == "":
				return actool.Errorf("command 或 technique 必须给一个：command 查这一条命令，technique 查该技术的预计信号"), nil
			case a.Command != "" && a.Technique != "":
				return actool.Errorf("command 与 technique 二选一，不要同时给"), nil
			case a.Technique != "":
				notes := opsec.ByTechnique(a.Technique)
				if len(notes) == 0 {
					return jsonResult(opsec.Result{Uncovered: true, Message: "知识库未收录该 ATT&CK 技术。**未收录不等于没有日志**，只代表本库没有依据；实测仍用 report_detection_check。"})
				}
				return jsonResult(opsec.Result{Notes: notes})
			default:
				return jsonResult(opsec.Annotate(a.Command))
			}
		},
	)
}
