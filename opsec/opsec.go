// Package opsec 是**事前**的可检出性知识库：这一手会留下什么、有多容易被看见。
//
// 它补的是 P31（检测验证）的另一半。P31 答的是「这一手**被检出**了吗」——那是事后、有证据的
// 结论；这里答的是「这一手**预计**会留下什么」——事前、基于公开的日志与遥测常识。两者刻意分开，
// 措辞也分开：这里从来不说「没被检出」，只说「预计会落在哪里」。混起来就是把预期读成实测，
// 而本仓在检测三态上已经把这条线画死了（unknown 必须独立存在）。
//
// 它是**知识库 + 提示**，不是自动化判断：
//   - 命令命中就给几条笔记（留在哪 / 具体信号 / 预计可见度 / 一句话说明），命中不了就**明说没收录**
//     ——「不知道」要能说出来，否则读的人会把「没提示」当成「没有日志」；
//   - 不替任何人做决定，也不给出规避手法。降暴露的正当做法在本仓是另一套机制：
//     授权范围（RoE）、作业时间窗、三层拦截与人工审批。
//
// 覆盖范围刻意保守：只收**公开、稳定、可核对**的信号（Windows 安全事件 ID、sysmon 事件号、
// auditd 常见记录、边界设备的必然留痕）。宁可少收，不可编——一条编出来的信号会让人据此
// 判断「这条安全」，而那比不给更危险。
package opsec

import (
	"regexp"
	"sort"
	"strings"
)

// Where 是留痕位置的大类。
const (
	WhereTargetSystem = "目标系统日志"   // OS 自身的安全/系统日志（4625、7045、auditd…）
	WhereEDR          = "安全设备/EDR" // EDR/HIDS/AV 的遥测与告警
	WhereNetwork      = "网络侧"      // 边界设备、代理、IDS 的流量记录
	WhereApp          = "应用日志"     // web/数据库/中间件自身的 access log
	WhereLocal        = "本机痕迹"     // 被控端自身的 shell 历史、临时文件
)

// 预计可见度：多容易被看见（不是「会不会被拦」）。
const (
	ExposureLow    = "低"
	ExposureMedium = "中"
	ExposureHigh   = "高"
)

// Note 是一条可检出性笔记。
type Note struct {
	// ID 稳定且短，供报告与界面引用（同一手在报告、审批卡、对话里指的是同一条）。
	ID string `json:"id"`
	// Title 一句话说清这是什么手法。
	Title string `json:"title"`
	// Match 是命令里出现的词（词边界匹配，避免 「at」 命中 「cat」 这类误报）。
	Match []string `json:"-"`
	Where []string `json:"where"`
	// Signals 是具体信号（事件 ID / 日志名 / 遥测项）。
	Signals []string `json:"signals,omitempty"`
	// Exposure 是**预计**可见度。
	Exposure string `json:"exposure"`
	Note     string `json:"note"`
}

// Result 是一次标注的结果。
type Result struct {
	Notes []Note `json:"notes"`
	// Uncovered 为真 = 没有一条笔记命中这条命令。**必须显式说出来**：
	// 读的人若把「没有提示」当成「没有日志」，这个功能就成了误导。
	Uncovered bool `json:"uncovered"`
	// Note 是给 Uncovered 时的一句人话（覆盖时不出现）。
	Message string `json:"message,omitempty"`
}

// Knowledge 是知识库。键即 Note.ID；顺序无关，匹配后按 ID 排序输出（渲染稳定）。
var Knowledge = []Note{
	{
		ID: "portscan-syn", Title: "端口扫描（半开 / 全连接）",
		Match: []string{"nmap", "masscan", "zmap", "nc -zv", "nc -z"},
		Where: []string{WhereNetwork, WhereTargetSystem},
		Signals: []string{"边界防火墙/IDS 的连接记录（源 IP + 目标端口序列）",
			"目标主机：-sT 全连接会进应用/系统连接日志；-sS 半开通常不落系统日志",
			"masscan/zmap 速率高，流量特征明显"},
		Exposure: ExposureMedium,
		Note: "扫描在边界几乎必然留痕（源 IP 与时间）。这是授权作业里最正常的一手，按客户约定的时段与源地址做即可；" +
			"速率越低越像正常访问，但**不要**把它当成隐身——被看见是预期的。",
	},
	{
		ID: "bruteforce", Title: "口令爆破 / 喷洒",
		Match: []string{"hydra", "medusa", "ncrack", "patator", "crackmapexec", "nxc", "kerbrute", " spray"},
		Where: []string{WhereTargetSystem, WhereEDR, WhereNetwork},
		Signals: []string{"Windows：4625（失败登录）成批出现，可能触发账户锁定策略",
			"Linux：sshd 的 Failed password 行 / PAM 记录",
			"EDR/IPS：暴破是标准检测项，命中率高"},
		Exposure: ExposureHigh,
		Note: "爆破是**最容易被检出**的一类动作，而且会真实触发账户锁定、影响客户业务账号。" +
			"优先用已知凭据或离线方式；确需爆破时先与客户确认锁定阈值与可接受时段。",
	},
	{
		ID: "sqli-tool", Title: "SQL 注入扫描/利用工具",
		Match:    []string{"sqlmap", "sqlninja", "nosqlmap"},
		Where:    []string{WhereApp, WhereEDR, WhereNetwork},
		Signals:  []string{"WAF 告警（特征明显）", "web access.log：同一 URI 的高频参数变形", "数据库端慢查询/错误日志"},
		Exposure: ExposureHigh,
		Note:     "工具化注入在 WAF 与 web 日志里特征很强。手工验证单点通常比全自动扫更隐蔽，也更容易控制影响面。",
	},
	{
		ID: "lsass-access", Title: "LSASS 内存读取 / 凭据抓取",
		Match:    []string{"mimikatz", "sekurlsa", "lsadump", "procdump", "comsvcs", "nanodump", "lsassy", "pypykatz"},
		Where:    []string{WhereEDR, WhereTargetSystem},
		Signals:  []string{"Sysmon 10（进程访问 LSASS）", "Windows 4688/进程创建", "EDR 对 LSASS 句柄访问的专项检测（命中率极高）"},
		Exposure: ExposureHigh,
		Note: "这是 EDR 覆盖最好的一类动作，几乎必然告警。它同时也是**最有价值的一手**——所以更该先和蓝队约好" +
			"（是演练还是真打），并在 P31 里如实记下防御侧到底有没有响。",
	},
	{
		ID: "lateral-smb", Title: "横向执行（PsExec / SMB / WMI / at）",
		Match: []string{"psexec", "smbexec", "wmiexec", "atexec", "dcomexec", "impacket", "wmic", "schtasks", "at.exe"},
		Where: []string{WhereTargetSystem, WhereEDR, WhereNetwork},
		Signals: []string{"Windows 7045（服务安装，PsExec 类）", "4698（计划任务创建）", "WMI 活动日志 / Sysmon 1、3、11",
			"SMB 登录 4624 type 3 / 5140 共享访问"},
		Exposure: ExposureHigh,
		Note:     "横向动作会在**两台**机器上留痕（来源与目标），且服务/任务的创建是安全运营最常盯的事件之一。",
	},
	{
		ID: "dcsync", Title: "目录数据库同步（DCSync / 导出 NTDS）",
		Match: []string{"dcsync", "ntdsutil", "esentutl", "vssadmin", "secretsdump", "GetChanges"},
		Where: []string{WhereTargetSystem, WhereEDR},
		Signals: []string{"Windows 4662（DS-Replication-Get-Changes / -All，需目录服务访问审计）",
			"域控上的大量磁盘 IO / 卷影服务调用", "EDR 对 NTDS.dit 访问的专项检测"},
		Exposure: ExposureHigh,
		Note:     "域内影响面最大的一手（拿到全部哈希）。仅在明确授权范围内使用，并注意它会在域控上留下强信号。",
	},
	{
		ID: "kerberoast", Title: "Kerberos 相关（Kerberoast / AS-REP / 票据）",
		Match:    []string{"rubeus", "kerberoast", "getuserspns", "getnpusers", "gettgt", "asktgt", "ticketer"},
		Where:    []string{WhereTargetSystem, WhereEDR},
		Signals:  []string{"4769（服务票据请求，RC4 加密时尤其显眼）", "4768（TGT 请求）", "LDAP 查询 1644 / 4662"},
		Exposure: ExposureHigh,
		Note:     "票据请求本身是正常域行为，但**成批 + 弱加密**的组合是标准检测特征。",
	},
	{
		ID: "dir-query", Title: "目录枚举（LDAP / 域信息查询 / 采集器）",
		Match: []string{"ldapsearch", "bloodhound", "sharphound", "adfind", "net group", "net user", "dsquery", "enum4linux"},
		Where: []string{WhereTargetSystem},
		Signals: []string{"域控 LDAP 查询量大（1644 / 4662）", "采集器（SharpHound）会在短时间内产生大量查询，特征明显",
			"本机 net user / net group 通常无痕（无 /domain 时）"},
		Exposure: ExposureMedium,
		Note:     "枚举通常不触发告警阈值，但采集器的查询量很容易在域控日志里被看出来。域内攻击路径导入（AD 图）用的就是这个。",
	},
	{
		ID: "webshell-download", Title: "下载并执行（curl|bash / wget|sh / 落马）",
		Match: []string{"curl", "wget", "certutil", "bitsadmin", "Invoke-WebRequest", "iwr "},
		Where: []string{WhereApp, WhereLocal, WhereTargetSystem},
		Signals: []string{"目标 web access.log（UA、源 IP）", "被控机 shell 历史 / PowerShell 转录（若开启）",
			"EDR 进程树：下载器拉起解释器（curl→sh）是典型可疑链", "Sysmon 1 / 3、Windows 4688"},
		Exposure: ExposureMedium,
		Note:     "单独看 curl 很普通；**下载后立刻执行**才是告警点。落地文件也会被文件完整性监控与杀软扫描到。",
	},
	{
		ID: "reverse-shell", Title: "反弹 shell / 交互式通路",
		Match: []string{"nc -e", "ncat", "bash -i", "/dev/tcp", "socat", "meterpreter", "msfconsole"},
		Where: []string{WhereNetwork, WhereEDR, WhereLocal},
		Signals: []string{"网络侧：非常规端口的**长连接**与外联（egress）", "进程树：web/服务进程拉起 shell",
			"EDR 对反弹 shell 的专项检测", "被控端 shell 历史"},
		Exposure: ExposureHigh,
		Note: "反弹 shell 是攻防演练里最典型的「红方动作」，网络侧与主机侧都有信号。拿到手之后它就是**据点**，" +
			"记进图里（report_foothold），后续复用比重新打一遍暴露面小得多。",
	},
	{
		ID: "sensitive-read", Title: "读敏感文件（shadow / 配置 / 密钥）",
		Match: []string{"/etc/shadow", "/etc/passwd", "id_rsa", ".ssh/", "wp-config", "web.config", ".env", "kdbx", "unattend.xml"},
		Where: []string{WhereTargetSystem, WhereLocal},
		Signals: []string{"auditd（若配置了文件访问审计：-w /etc/shadow）", "无 auditd 时通常无系统日志",
			"被控端 shell 历史（如果用交互式 shell 敲的）"},
		Exposure: ExposureMedium,
		Note: "**取决于目标有没有开审计**：开了就留痕，没开多半只有 shell 历史。别把「没看到告警」当成「没有记录」——" +
			"P31 的 unknown 说的就是这件事。",
	},
	{
		ID: "privesc-enum", Title: "本机提权枚举（脚本化）",
		Match: []string{"linpeas", "winpeas", "unix-privesc-check", "seatbelt", "beacon", "sudo -l", "find / -perm"},
		Where: []string{WhereLocal, WhereEDR, WhereTargetSystem},
		Signals: []string{"大量文件读取/目录遍历（可能触发 HIDS/FIM）", "auditd execve（若开启）",
			"EDR 对已知枚举脚本的字符串检测"},
		Exposure: ExposureMedium,
		Note:     "枚举脚本会在短时间内读取大量系统文件，主机侧监控（尤其 FIM）容易看出异常。",
	},
	{
		ID: "anti-forensics", Title: "清理痕迹（history -c / 删日志 / 时间戳）",
		Match: []string{"history -c", "unset HISTFILE", "shred", "wevtutil cl", "Clear-EventLog", "timestomp", "log cleaner"},
		Where: []string{WhereTargetSystem, WhereEDR},
		Signals: []string{"日志服务被清空本身是**高危告警**（Windows 1102：审核日志已清除）",
			"文件时间戳异常（MACB 不一致）是取证的标准发现点", "EDR 对清日志行为的专项检测"},
		Exposure: ExposureHigh,
		Note: "本仓不建议做：它既是**最响的告警之一**，又会破坏你自己的作业证据链（执行台账与流量留痕是交付物的基础）。" +
			"授权作业的价值在「做过什么可自证」，不在「没留下痕迹」。",
	},
}

// matchWords 是一条笔记编译好的匹配器。
type matchWords struct {
	note Note
	res  []*regexp.Regexp
}

var compiled = func() []matchWords {
	out := make([]matchWords, 0, len(Knowledge))
	for _, n := range Knowledge {
		mw := matchWords{note: n}
		for _, m := range n.Match {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			pat := regexp.QuoteMeta(m)
			// 词边界：避免 「at」 命中 「cat」、「ir」 命中 「dir」。命令里常见的前后字符是空格、
			// 斜杠、引号、分号——把它们当边界而不是 \b，因为 「curl|bash」 这种紧贴写法也要命中。
			boundary := `(^|[\s/|;&("'` + "`" + `])`
			tail := `($|[\s/|;&)"'` + "`" + `])`
			re, err := regexp.Compile(boundary + pat + tail)
			if err != nil {
				continue
			}
			mw.res = append(mw.res, re)
		}
		if len(mw.res) > 0 {
			out = append(out, mw)
		}
	}
	return out
}()

// Annotate 给一条命令标注**预计**的可检出性。命中不了就如实说没收录。
//
// 为什么宁可说「没收录」：这个功能的全部价值在于让人对「会留下什么」有预期。若命中不了时返回空列表
// 而不作声，读的人很容易把它当成「这条没有日志」——那正好是本仓一路在防的那种误读
// （同 P31 的 unknown、P24 的「没限住就要如实说」）。
func Annotate(command string) Result {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return Result{Uncovered: true, Message: "命令为空，无可标注。"}
	}
	var hits []Note
	for _, mw := range compiled {
		for _, re := range mw.res {
			if re.MatchString(cmd) {
				hits = append(hits, mw.note)
				break
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
	if len(hits) == 0 {
		return Result{
			Uncovered: true,
			Message: "知识库里没有收录这条命令的可检出性——**这不代表没有日志**，只是本库没有依据。" +
				"把它当成未知，必要时用 detection 记录实测结果（P31）。",
		}
	}
	return Result{Notes: hits}
}

// ExposureRank 给可见度一个可排序的档位（报告里按它排先后）。
func ExposureRank(exposure string) int {
	switch exposure {
	case ExposureHigh:
		return 3
	case ExposureMedium:
		return 2
	case ExposureLow:
		return 1
	}
	return 0
}
