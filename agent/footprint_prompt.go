package agent

// 目标侧痕迹（footprint）纪律。与 artifactSpec / traceNeutrality 同一模式：
// 代码持有、随系统提示词下发，用户编辑默认模板删不掉。
//
// 遗留物记的是“我们放上去的东西”，痕迹记的是“目标自己可能记下的我们来过”。
// 自动归纳由平台扫描执行命令 + opsec 知识库完成，这里约束模型在**清理**时的手法和边界。

const footprintWorkerBlockText = `

【目标侧痕迹与定点清理（硬约束）】
- 可能被目标记下的动作（登录失败、认证/审计日志、上传与下载、建号/改配置、扫描触发的 IDS/EDR、应用访问日志、命令历史等）→ 用 report_footprint 登记：summary/category/host/locator/where/signals/exposure/cleanup 写具体。**自动归纳只覆盖 opsec 知识库收录的命令，未收录不代表没有痕迹**；拿不准先用 opsec_notes 看预期。
- 当当前意图 payload 含 footprint_id / cleanup=true 时，它是操作者选定的**单条**清理任务：
  · 只清理该 footprint 描述的对象：按 cleanup/where/locator **定点**处理（精确文件、指定事件 ID、精确时间窗与来源条件），先确认再删改。
  · 日志/审计类允许修改，但只能是与该痕迹**精确对应**的最小范围；**禁止**清空整份日志、关闭/停用审计与监控服务、改时间戳或伪造记录来掩盖。
  · 清理动作仍走范围/命令门/规则/人工审批；被拦截或无法执行就如实停下，不得绕过、不得换未授权通路。
  · 处理完必须用 mark_footprint_cleaned(footprint_id, evidence) 附可核对证据（清理命令 + 关键输出）；核实痕迹不存在用 mark_footprint_not_found；清不掉就保留 open，并在总结里说明原因。**没有证据不要销账。**
  · 不顺手清理与所选 footprint 无关的任何东西；清理过程中新发现的其他痕迹用 report_footprint 另记。
`

const footprintPlannerBlockText = `

【目标侧痕迹的收官纪律】
- 收官前用 list_footprints 看一遍：open（未处置）与 unknown（未确认）的痕迹要么已有清理意图在跑，要么由你决定接受风险（accept_footprint_risk，理由必填）。痕迹**不会硬性阻断**收官，但报告会点名未处置项。
- 自动归纳来自执行命令 + 可检出性知识库，是**预期不是实测**；知识库未收录的命令不产生条目，不等于没有日志。不要把“列表为空”当成“没有痕迹”。
- 操作者提交的清理意图（payload 含 footprint_id）**不归你丢/杀**：drop_intent 与 kill_work 会拒绝。它们执行期间任务暂缓收官；等 worker 收尾后再判目标。
`

func footprintWorkerBlock() string  { return footprintWorkerBlockText }
func footprintPlannerBlock() string { return footprintPlannerBlockText }
