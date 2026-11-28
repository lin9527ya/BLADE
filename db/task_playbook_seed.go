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

var builtinMiniAppPlaybooks = []builtinMobilePlaybook{
	{
		name:        "Playbook：微信小程序全流程渗透（解包 + 云开发 + A/B 越权 + SRC 报告）",
		description: "适用：已上传 .wxapkg 或含小程序包的 .zip（或在任务中给出微信小程序 AppID）。流程：安全解密解包 → miniapp_audit 白盒扫描 → 路由/云开发审计 → A/B 双账号接口越权 → 证据校验与报告。",
		goal: `对「{{目标微信小程序与授权后端}}」开展微信小程序全流程安全测试，产出可复现且符合 SRC/微信安全标准的渗透报告。**核心纪律：wxapkg 安全解包拿钥匙，A/B 双身份打穿后端接口与云开发边界。**

【授权范围】
- 被测微信小程序 AppID 对应的业务域名、API 网关、WebView 业务域及授权测试的 CloudBase 云环境；第三方公共统计/地图 SDK 仅作资产清单核查，不作超范围攻击。

【内部工作流】
1. 样本确认与安全解包：加载 Skill("mini-program")；核查 samples/ 或 uploads/ 下的 .wxapkg / .zip 样本，使用 mobile_decompile_audit 或 ${SKILL_DIR}/scripts/miniapp_unpack.sh 执行安全解包（防路径穿越 + V1MMWX 解密 + 质量门控），并以 insert_assets(type="app", bundle_id=<AppID>) 登记小程序资产；
2. 静态白盒审计：对解包源码目录调用 miniapp_audit(dir=..., appid=...)，提取 app.json/app-config.json 全量路由、wx.request/uploadFile/connectSocket 接口、CloudBase env/云函数/集合、签名算法位置与脱敏密钥提示；端点批量入 insert_assets(type="endpoint")，关键线索落 record_fact；
3. 云开发与开放能力审计：针对检出的云开发 env、wx.cloud.callFunction、数据库 collection、web-view 容器及小程序码/登录口（loginByOpenId/code2Session 校验、手机号新旧版流程），结合 references/03~07 做逐项核查；
4. 抓包与 A/B 双身份越权：引导代理抓包（PC 微信 Proxifier 进程代理或工作台提取会话），使用 session_login / extract-session 建立 A/B 测试身份，对核心接口跑无令牌、空令牌、A/B 水平越权及普通/管理垂直越权，并用 diff_responses 比对差异；
5. 证据校验与报告：严禁将“未加固/可反编译”或无利用链的公开 SDK Key 定性高危；提交 report_finding 前调用 mobile_evidence_check 自检包哈希、源码定位、HTTP 请求响应闭环与凭据脱敏。`,
	},
}

// seedMiniAppTaskPlaybooks idempotently seeds built-in WeChat Mini Program playbooks
// using an isolated settings flag so upgrades never collide with task_playbooks_mobile_v1.
func (d *DB) seedMiniAppTaskPlaybooks() error {
	const flag = "task_playbooks_miniapp_v1"
	if v, _, _ := d.GetSetting(flag); v == "done" {
		return nil
	}
	for _, pb := range builtinMiniAppPlaybooks {
		in := TaskTemplateInput{
			Name:        pb.name,
			Description: pb.description,
			Goal:        pb.goal,
		}
		if _, err := d.CreateTaskTemplate(in); err != nil {
			if errors.Is(err, ErrTaskTemplateNameConflict) {
				continue
			}
			return fmt.Errorf("miniapp playbook %q: %w", pb.name, err)
		}
	}
	return d.SetSetting(flag, "done")
}
