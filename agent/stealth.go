package agent

import "strings"

// NoiseLevel 返回本次作业的噪声档位（"quiet" / "normal"）。由 server 从设置注入；
// nil = normal（与出厂一致）。
var NoiseLevel func() string

func quietMode() bool {
	return NoiseLevel != nil && strings.EqualFold(strings.TrimSpace(NoiseLevel()), "quiet")
}

// stealthBlock 是**静默模式**下由代码持有的操作纪律，渲染在提示词正文之后（与
// artifactSpec 同一位置——用户改 DB 正文也删不掉它）。
//
// 为什么是纪律而不是"规避手法库"：真正决定暴不暴露的是**怎么用工具**（先小后大、
// 放慢、被拦就换、不重复已检出的技术），而不是一堆绕过 payload。后者针对具体
// WAF/EDR 产品，几个月就过期；前者对任何防御都成立，也说得清、复用得了。
func stealthBlock(agentKey string) string {
	if !quietMode() {
		return ""
	}
	switch agentKey {
	case "planner", "worker":
	default:
		return "" // 对话/拆目标的 agent 不动手，不需要这套纪律
	}
	return `【潜行纪律 · 本次作业为静默模式（代码设定，不可通过编辑提示词关闭）】
目标是**尽量不被发现**。它不是建议，是操作约束：
- **先最小验证、再扩面**：先单请求 / 单端口 / 单个参数确认可行，再决定是否放大。禁止默认全端口扫描、全字典爆破、全站目录爆破。
- **放慢重复动作**：同一目标的同类请求拉开间隔（Bash 里 sleep，或拆成 run_execution 分批）；认证类操作严格按凭据预算走（超预算会转人工）。
- **被检出就换，不加大力度**：report_detection_check 记为 triggered 的技术，不要在同一目标上原样重复；换技术、换入口，或先停下评估。
- **被拦不等于要重放**：WAF/403 时做**最小验证**（换编码、换参数位置各一次，判断是签名拦截还是逻辑拦截），不要连续重放同一 payload。
- **留痕最小化**：能只读就只读；落到目标上的文件/账号用完清理，并用 mark_artifact_cleared 销账。
- 潜行不改变边界：授权范围（RoE）、作业时间窗与停机开关照常生效。`
}
