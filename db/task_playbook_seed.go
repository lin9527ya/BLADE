package db

import (
	"errors"
	"fmt"
)

type builtinMobilePlaybook struct {
	name        string
	description string
	goal        string
}

var builtinMobilePlaybooks = []builtinMobilePlaybook{
	{
		name:        "Playbook：移动 APP 全流程渗透（真机 + 逆向 + API）",
		description: "适用：已上传 APK/IPA 且可接入真机/模拟器。流程：安全解包与目录白盒 → Frida/透明代理抓包 → UI 状态机与 IPC 探测 → 双账号 A/B 接口越权 → 隐私/沙箱审计 → 证据校验与报告。",
		goal: `对「{{目标 APP 与授权后端}}」完成移动端全流程安全评估，产出可复现且符合 SRC/MASVS 标准的报告。**核心纪律：客户端逆向拿钥匙，双账号后端接口打穿透。**

【授权范围】
- 被测 APP 本体、安装包内解析确认归属的候选后端域名；非授权三方 SDK 域名仅作清单记录与归属核实，不作破坏性测试。

【内部工作流】
1. 静态画像与目录审计：加载 Skill("apk-reverse")，调用 mobile_decompile_audit 对样本做安全解包、jadx/apktool 还原与白盒扫描，提取全量接口、密钥与签名位置；
2. 抓包与对抗绕过：检查 mobile_device_status → Root 推送系统 CA（含 Conscrypt APEX）→ 启用 usb_reverse 或 enable_transparent 透明接管（Flutter/Cronet 配 flutter_ssl_bypass / native_tls_hook）；
3. 客户端本地攻击面：跑通 mobile_ipc_probe(auto_fuzz) 验证导出组件与 DeepLink，执行 mobile_app_ctrl(sandbox_audit/logcat_audit) 检查明文存储与敏感日志；
4. UI 遍历与会话抽样：用 mobile_ui_explore(auto_crawl / screenshot) 完成核心页面走查；利用人机提取或双账号登录态建立 A/B 对照身份，若接口带签名则配合 Frida rpc.exports 与 http_request(mobile_sign_method=...) 跑通未授权/水平与垂直越权/并发/支付逻辑测试；
5. 证据与收尾：用 mobile_evidence_check 核对每条漏洞（包 SHA-256、ROM/OS、成对流量、A/B 脱敏样本<=5条），禁止将“纯未加固/可反编译”或纯本地界面篡改误报高危。`,
	},
	{
		name:        "Playbook：APP 纯静态深度审计与接口挖掘（无真机模式）",
		description: "适用：仅上传了 APK/AAB/XAPK/IPA 安装包、暂未接入测试机。流程：原生画像 + mobile_decompile_audit 白盒扫描 → 提取接口/密钥/加解密与组件风险 → 对可达授权后端接口开展黑盒探测 → 输出待动态复核项清单。",
		goal: `在无真机条件下对「{{目标 APP 样本与后端}}」开展静态深度审计与可达接口验证。

【内部工作流】
1. 样本核验与反编译：加载 Skill("apk-reverse")，执行 mobile_decompile_audit 对 tasks/<id>/samples/ 下的样本解包与目录扫描，汇总 Manifest/Info.plist/ELF/Mach-O 加固选项及 Java/Smali/JS 白盒风险；
2. 钥匙与攻击面沉淀：将硬编码密钥/AKSK（全文落 fact，报告脱敏）、导出组件/WebView/Scheme 隐患及接口端点分别登记到资产与事实图谱；
3. 后端接口探测：若逆向出了加密/签名算法，用脚本复算后直接经 http_probe / http_request 测试授权后端接口的未授权访问、历史接口与注入漏洞；
4. 诚实标注边界：对于需要真机才能实锤的运行时 Hook/Pinning 绕过/UI 交互/沙箱存储检查，明确写入未验证范围与前置条件，不伪造动态证据。`,
	},
}

// seedMobileTaskPlaybooks idempotently seeds built-in APP penetration Playbook templates
// guarded by a dedicated settings flag so legacy upgraded DBs also receive them without
// resurrecting templates that operators deliberately modified or deleted later.
func (d *DB) seedMobileTaskPlaybooks() error {
	const flag = "task_playbooks_mobile_v1"
	if v, _, _ := d.GetSetting(flag); v == "done" {
		return nil
	}
	for _, pb := range builtinMobilePlaybooks {
		in := TaskTemplateInput{
			Name:        pb.name,
			Description: pb.description,
			Goal:        pb.goal,
		}
		if _, err := d.CreateTaskTemplate(in); err != nil {
			if errors.Is(err, ErrTaskTemplateNameConflict) {
				continue
			}
			return fmt.Errorf("mobile playbook %q: %w", pb.name, err)
		}
	}
	return d.SetSetting(flag, "done")
}
