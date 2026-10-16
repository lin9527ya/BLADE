package agent

import "strings"

// 出站产物语言（P41）：命令、参数、payload、落地文件名、注释——**目标能看到的一切**用什么语言。
//
// 为什么把它与"界面语言"分开：界面语言是操作者的阅读偏好，产物语言是**发给目标的东西**。
// 耦合起来会出一个很隐蔽的错——切个界面顺手就改了出站内容。两者是两个设置。
//
// 默认 zh = **不约束**：本平台面向中文授权作业，中文本身不是错；打中文站点时，中文的探针参数、
// 中文用户名枚举恰恰是业务需要。操作者要收紧时切到 en，规约段随系统提示词下发。
//
// 规约是 **code-owned**（用户改不掉），与 P20 的探索策略段同一纪律：它是操作者声明的边界，
// 不该被后续任何提示词编辑悄悄抹掉。
const (
	ArtifactLangZh = "zh" // 不约束（默认）
	ArtifactLangEn = "en" // 面向目标的产物一律 ASCII 英文
)

// ArtifactLanguage 由 server 注入（读 settings.artifact_language，每 run 现读 → 改完即时生效）。
// nil = 不约束。形状与 PromptOverride 一致（agent 包不依赖 db）。
var ArtifactLanguage func() string

// artifactLanguageBlock 渲染"面向目标的产物语言"规约段；**zh/未设时返回空串**——
// 不约束就是不加一个字，而不是加一段"你可以用中文"的废话来占提示词。
func artifactLanguageBlock() string {
	if ArtifactLanguage == nil {
		return ""
	}
	if strings.TrimSpace(ArtifactLanguage()) != ArtifactLangEn {
		return ""
	}
	return artifactLanguageEnBlock
}

// artifactLanguageEnBlock 是规约正文。
//
// 两处刻意的写法：
//   - **划清边界**：只约束"出站产物"，模型与平台之间的中文（意图、事实、报告、思考）不受影响。
//     不写这一句，模型很可能把中文思考也一起改掉——那是无谓的退化，而它读的是全中文提示词。
//   - **给业务需要留门并说明如何留痕**：中文站点的 XSS 探针参数就该是中文；写清"要在意图或
//     fact 里说明理由"，事后复盘时才能区分"业务需要"与"手滑"。
const artifactLanguageEnBlock = `

【面向目标的产物语言：英文（操作者设置）】
你发给目标的**一切**都可能被当成"操作者画像"的证据：命令与参数、payload、落地的文件名与目录名、
脚本注释、日志里留下的字符串。所以这些默认一律用 ASCII 英文：
- 不写中文注释、中文标记字符串、中文临时文件名（例如 echo "测试" > /tmp/x 这种写法）；
- 需要给目标留标记时用英文（如 artex-test-marker）——要可识别、可清理；
- **目标业务确需中文字符串时照常用中文**（中文站点的 XSS 探针参数、中文用户名枚举、中文提示词
  注入…），但请在意图或 fact 里写明这一条的理由，便于复盘时区分"业务需要"与"手误"。
这条规约只管**出站产物**：你与平台之间的中文（意图、事实、发现、报告）不受影响，照常写。`
