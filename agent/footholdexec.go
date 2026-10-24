package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// normalizeFootholdAccess 校验并归一化据点的**结构化连法**。
//
// 空 protocol = 只有自由文本的据点：合法（很多通路平台确实没法代为执行），但没有可执行
// 连法。非空则必须**真的能执行**——否则等于给规划层一个看起来能用、实际用不了的战果。
func (t *ToolSet) normalizeFootholdAccess(a footholdItem) (map[string]any, error) {
	out := map[string]any{}
	protocol := strings.ToLower(strings.TrimSpace(a.Protocol))
	host := strings.TrimSpace(a.Host)
	urlStr := strings.TrimSpace(a.URL)
	credID := pid(a.CredentialID)
	if protocol == "" {
		if host != "" || urlStr != "" || credID > 0 {
			return nil, fmt.Errorf("填了 host/url/credential_id 却没写 protocol：结构化连法要一起给（protocol=ssh 或 webshell），" +
				"否则平台不知道拿它们做什么")
		}
		return out, nil
	}
	if !db.ValidFootholdProtocol(protocol) {
		return nil, fmt.Errorf("protocol=%q 暂不支持代为执行（当前只支持 %s）。其它协议把连法写进 detail 自由文本，"+
			"由 worker 自己连；不要填一个平台用不了的 protocol", a.Protocol, strings.Join(db.FootholdProtocols, " / "))
	}
	out["protocol"] = protocol
	switch protocol {
	case "ssh":
		if host == "" {
			return nil, fmt.Errorf("protocol=ssh 时必须给 host（连哪台）")
		}
		if credID <= 0 {
			return nil, fmt.Errorf("protocol=ssh 时必须给 credential_id：口令/私钥来自凭据节点（先用 report_credential 登记），" +
				"平台不在据点里存明文")
		}
		cred, err := t.ts.GetNode(credID)
		if err != nil || cred == nil || cred.Kind != db.KindCredential {
			return nil, fmt.Errorf("credential_id %d 不是本任务的凭据节点", credID)
		}
		if cred.State != db.StateLootActive {
			return nil, fmt.Errorf("凭据 %d 当前状态是 %q（已失效），不能挂到据点上", credID, cred.State)
		}
		kind := strings.ToLower(strings.TrimSpace(db.ParseCredential(cred).Kind))
		if kind != "password" && kind != "key" {
			return nil, fmt.Errorf("凭据 %d 的类型是 %q：ssh 只支持 password / key", credID, kind)
		}
		port := a.Port
		if port <= 0 {
			port = 22
		}
		if port > 65535 {
			return nil, fmt.Errorf("port=%d 不合法", a.Port)
		}
		out["host"], out["port"], out["credential_id"] = host, port, credID
	case "webshell":
		if urlStr == "" {
			return nil, fmt.Errorf("protocol=webshell 时必须给 url（webshell 的入口）")
		}
		u, err := url.Parse(urlStr)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("url=%q 不是合法的 http(s) 地址", urlStr)
		}
		if host == "" {
			host = u.Hostname()
		}
		param := strings.TrimSpace(a.Param)
		if param == "" {
			param = "cmd"
		}
		out["host"], out["url"], out["param"] = host, urlStr, param
	}
	return out, nil
}

// footholdExec 用一条**已登记的据点**在目标上执行命令。
//
// 与 loot_exec 的分工：loot_exec 解的是「我握着一条凭据，拿它去连」；这里解的是
// 「我已经在目标上了，再执行一条命令」——ssh 据点靠它指向的凭据重连，webshell 据点
// 直接打到既有的 URL。没有这个工具，据点只能是一句自由文本，平台既帮不上忙也做不了
// 授权范围校验。
func (t *ToolSet) footholdExec() actool.CoreTool {
	return writeTool("foothold_exec",
		"用一条**已登记的据点**在目标上执行命令（ssh / webshell）。据点必须带结构化连法"+
			"（report_foothold 的 protocol/host/port/url/credential_id），否则平台不知道该怎么连。\n"+
			"★ ssh：口令/私钥来自据点指向的凭据节点，经 0600 临时文件交给客户端，明文不进命令与台账。\n"+
			"★ webshell：以 POST 把命令放进据点登记的参数（默认 cmd），用 curl 打该入口。\n"+
			"★ host 必填且必须与据点登记的一致——平台据此做授权范围校验，不替你猜目标。\n"+
			"★ 命令在后台执行：返回 execution_id，用 get_execution / wait_execution 取输出，要具体行用 grep_execution。",
		obj(map[string]any{
			"foothold_id": idp("据点节点 id（graph_overview 的 footholds；必须是在手且带结构化连法的）"),
			"host":        str("在**哪台**目标上执行（必填；必须与据点登记的 host 一致）"),
			"command":     str("要执行的命令（非交互）"),
			"timeout_ms":  intp("可选硬超时毫秒（到点杀进程）；省略=不限"),
		}, "foothold_id", "host", "command"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !lootExecOn() {
				return actool.Errorf("foothold_exec: 该能力默认关闭（它会把据点的连法直接变成对目标的动作）。" +
					"请让操作者在设置里打开 agent_loot_exec；在此之前按据点的 detail 用 Bash 自己连。"), nil
			}
			if !t.execToolsReady() {
				return actool.Errorf("foothold_exec: 执行服务未接线（当前运行环境不支持后台执行），请改用 Bash"), nil
			}
			var a struct {
				FootholdID json.RawMessage `json:"foothold_id"`
				Host       string          `json:"host"`
				Command    string          `json:"command"`
				TimeoutMs  int             `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.FootholdID)
			if id <= 0 {
				return actool.Errorf("foothold_exec: foothold_id 必填"), nil
			}
			if strings.TrimSpace(a.Command) == "" {
				return actool.Errorf("foothold_exec: command 不能为空"), nil
			}
			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("foothold_exec: host 必填——平台要按它做授权范围校验，不替你猜目标"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil {
				return actool.Errorf(fmt.Sprintf("foothold_exec: %d 不存在于本任务", id)), nil
			}
			if node.Kind != db.KindFoothold {
				return actool.Errorf(fmt.Sprintf("foothold_exec: %d 是 %s 节点，不是据点", id, node.Kind)), nil
			}
			if node.State != db.StateLootActive {
				return actool.Errorf(fmt.Sprintf("foothold_exec: 据点 %d 当前状态是 %q（已失效），不能再用它执行", id, node.State)), nil
			}
			v := db.ParseFoothold(node)
			if v.Protocol == "" {
				return actool.Errorf(fmt.Sprintf("foothold_exec: 据点 %d 没有结构化连法（只有自由文本）。"+
					"按它的 detail 用 Bash 自己连；下次 report_foothold 时把 protocol/host/… 一并登记，平台才能代为执行", id)), nil
			}
			if normalizeHost(v.Host) != normalizeHost(host) {
				return actool.Errorf(fmt.Sprintf("foothold_exec: host=%s 与据点登记的 %s 不一致：平台按登记的目标做授权范围校验，"+
					"要打到别的主机请另记一条据点", host, v.Host)), nil
			}
			var timeout time.Duration
			if a.TimeoutMs > 0 {
				timeout = time.Duration(a.TimeoutMs) * time.Millisecond
			}
			desc := fmt.Sprintf("foothold_exec：经据点 %d（%s %s）在 %s 上执行", id, v.Protocol, v.URL, host)

			var command string
			var cleanup func()
			switch v.Protocol {
			case "ssh":
				cred, cerr := t.ts.GetNode(v.CredentialID)
				if cerr != nil || cred == nil || cred.Kind != db.KindCredential || cred.State != db.StateLootActive {
					return actool.Errorf(fmt.Sprintf("foothold_exec: 据点指向的凭据 %d 已不可用（缺失/失效/不是凭据）", v.CredentialID)), nil
				}
				cv := db.ParseCredential(cred)
				kind := strings.ToLower(strings.TrimSpace(cv.Kind))
				if kind != "password" && kind != "key" {
					return actool.Errorf(fmt.Sprintf("foothold_exec: 凭据 %d 的类型是 %q（ssh 只支持 password / key）", v.CredentialID, kind)), nil
				}
				user := strings.TrimSpace(cv.Username)
				if user == "" {
					return actool.Errorf(fmt.Sprintf("foothold_exec: 凭据 %d 没有登记 username，无法登录", v.CredentialID)), nil
				}
				secretPath, serr := writeLootSecret(cv.Secret)
				if serr != nil {
					return actool.Errorf("foothold_exec: 无法暂存凭据明文：" + serr.Error()), nil
				}
				cleanup = func() { _ = removeLootSecret(secretPath) }
				command = buildLootCommand(kind, "ssh", user, v.Host, v.Port, secretPath, a.Command)
			case "webshell":
				command = buildWebshellCommand(v.URL, v.Param, a.Command, timeout)
				cleanup = func() {}
			default:
				return actool.Errorf(fmt.Sprintf("foothold_exec: 据点协议 %q 不可执行", v.Protocol)), nil
			}

			st, subErr := t.execService.Submit(ctx, t.taskIDStr(), t.ownerNode, t.worker, command, desc, timeout)
			if subErr != nil {
				cleanup()
				return actool.Errorf("foothold_exec: " + subErr.Error()), nil
			}
			t.auditLootExec("foothold_exec",
				fmt.Sprintf("经据点 %d（%s）在 %s 上执行命令", id, v.Protocol, host),
				fmt.Sprintf("据点 #%d（%s）→ %s\n命令：%s", id, v.Protocol, host, a.Command))
			raw, _ := json.Marshal(st)
			return actool.Text(fmt.Sprintf("execution started: %d\n%s\n据点 %s 已用于连接 %s；用 get_execution(%d) 取输出。",
				st.ID, raw, v.Protocol, host, st.ID)), nil
		})
}

// buildWebshellCommand 构造对 webshell 的调用。
//
// 关键在**不把模型输出当本地 shell 代码执行**：命令整体作为 `--data-urlencode` 的参数值，
// 用单引号包住（shQuote），本地 shell 不会解释里面的分号/引号/反引号；curl 负责 URL 编码。
// 方法固定 POST（最常见的一类），其它形态按要求写进 detail、由 worker 自己构造。
func buildWebshellCommand(endpoint, param, command string, timeout time.Duration) string {
	// curl 的 --max-time 用秒；与托管执行的硬超时（若给）对齐，避免连接悬挂比进程还久。
	maxTime := 120
	if timeout > 0 {
		if secs := int(timeout.Seconds()); secs > 0 {
			maxTime = secs
		}
	}
	return fmt.Sprintf("curl -sS --max-time %d -X POST --data-urlencode %s %s",
		maxTime, shQuote(param+"="+command), shQuote(endpoint))
}
