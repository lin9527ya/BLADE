package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// enum_privilege：只读提权枚举。固定 profile、没有 command 参数、不生成/投递 linpeas。
// 复用 loot_exec / foothold_exec 同一套连接构造与审批链；默认由 agent_loot_exec 开关控制。
//
// 明确排除：mimikatz / sekurlsa / lsadump / procdump / comsvcs / nanodump / DPAPI / SAM。
// 需要现成二进制时由操作者走 loot_upload + 既有执行工具，仍受同一审批约束。

var privilegeProfiles = map[string]string{
	"linux": "id; hostname; uname -a; cat /etc/os-release 2>/dev/null; sudo -n -l 2>&1; " +
		"getcap -r / 2>/dev/null; find / -perm -4000 -type f 2>/dev/null; ss -lntup 2>/dev/null; " +
		"systemctl list-timers --all 2>/dev/null; ls -la /etc/cron* 2>/dev/null",
	"network": "ip a; ip route; cat /etc/resolv.conf; arp -a; ss -lntup 2>/dev/null",
	"windows": "whoami /all; whoami /priv; systeminfo; net localgroup administrators; " +
		"schtasks /query /fo LIST; sc query; wmic service get name,pathname,startmode; " +
		`reg query "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run"`,
}

func (t *ToolSet) enumPrivilege() actool.CoreTool {
	return writeTool("enum_privilege",
		"在**已有据点/凭据**上执行一组**只读**提权枚举命令（profile 闭集：linux / windows / network）。"+
			"没有 command 参数，命令由平台固定——不包含凭据抓取、不部署持久化、不生成或投递 linpeas/winPEAS。"+
			"输出是后台执行：返回 execution_id，用 get_execution 读取后 record_fact 写回；"+
			"默认关（agent_loot_exec），执行仍走 RoE、拦截规则、裁判与人工审批。",
		obj(map[string]any{
			"foothold_id": idp("据点节点 id（与 loot_id 二选一）"),
			"loot_id":     idp("凭据节点 id（与 foothold_id 二选一）"),
			"host":        str("目标主机（必填；必须与登记的目标一致）"),
			"profile":     map[string]any{"type": "string", "enum": []string{"linux", "windows", "network"}, "description": "只读枚举 profile"},
			"timeout_ms":  intp("可选硬超时毫秒（默认 120000）"),
		}, "host", "profile"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if !lootExecOn() {
				return actool.Errorf("enum_privilege: 该能力默认关闭（会在目标上执行命令）。请让操作者在设置里打开 agent_loot_exec"), nil
			}
			if !t.execToolsReady() {
				return actool.Errorf("enum_privilege: 执行服务未接线，请改用 Bash"), nil
			}
			var a struct {
				FootholdID json.RawMessage `json:"foothold_id"`
				LootID     json.RawMessage `json:"loot_id"`
				Host       string          `json:"host"`
				Profile    string          `json:"profile"`
				TimeoutMs  int             `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("enum_privilege: host 必填——平台要按它做授权范围校验"), nil
			}
			profile := strings.ToLower(strings.TrimSpace(a.Profile))
			command, ok := privilegeProfiles[profile]
			if !ok {
				return actool.Errorf(fmt.Sprintf("enum_privilege: profile 必须是 linux / windows / network，收到 %q", profile)), nil
			}
			lootID, footholdID := pid(a.LootID), pid(a.FootholdID)
			if (lootID > 0) == (footholdID > 0) {
				return actool.Errorf("enum_privilege: loot_id 与 foothold_id 必须二选一"), nil
			}
			timeout := 120 * time.Second
			if a.TimeoutMs > 0 {
				timeout = time.Duration(a.TimeoutMs) * time.Millisecond
			}

			var shellCmd string
			var cleanup func()
			meta := map[string]any{"host": host, "profile": profile}
			if lootID > 0 {
				node, err := t.ts.GetNode(lootID)
				if err != nil || node == nil || node.Kind != db.KindCredential {
					return actool.Errorf(fmt.Sprintf("enum_privilege: 凭据 %d 不存在或不是凭据", lootID)), nil
				}
				if node.State != db.StateLootActive {
					return actool.Errorf(fmt.Sprintf("enum_privilege: 凭据 %d 当前状态是 %q，不能再用", lootID, node.State)), nil
				}
				v := db.ParseCredential(node)
				kind := strings.ToLower(strings.TrimSpace(v.Kind))
				if kind != "password" && kind != "key" && kind != "hash" {
					return actool.Errorf(fmt.Sprintf("enum_privilege: 凭据类型 %q 不支持自动连接", v.Kind)), nil
				}
				target, err := pickCredentialTarget(v, host)
				if err != nil {
					return actool.Errorf("enum_privilege: " + err.Error()), nil
				}
				service := strings.ToLower(strings.TrimSpace(target.Service))
				if service == "" {
					service = defaultServiceForPort(target.Port)
				}
				action, nxc := nxcServices[service]
				if service != "ssh" && service != "mysql" && !nxc {
					return actool.Errorf(fmt.Sprintf("enum_privilege: 目标服务 %q 不支持（ssh/mysql/%s）", target.Service, strings.Join(nxcServiceNames(), "/"))), nil
				}
				user := strings.TrimSpace(v.Username)
				if user == "" {
					return actool.Errorf("enum_privilege: 凭据没有登记 username"), nil
				}
				if nxc {
					shellCmd, cleanup, err = buildNXCCommand(service, action, user, host, target.Port, kind, v.Secret, command)
				} else {
					if service == "ssh" && kind == "hash" {
						return actool.Errorf("enum_privilege: ssh 不支持 hash 凭据"), nil
					}
					if service == "mysql" && kind != "password" {
						return actool.Errorf("enum_privilege: mysql 只支持 password 凭据"), nil
					}
					secretPath, serr := writeLootSecret(v.Secret)
					if serr != nil {
						return actool.Errorf("enum_privilege: 无法暂存凭据：" + serr.Error()), nil
					}
					cleanup = func() { _ = removeLootSecret(secretPath) }
					shellCmd = buildLootCommand(kind, service, user, host, target.Port, secretPath, command)
				}
				if err != nil {
					return actool.Errorf("enum_privilege: " + err.Error()), nil
				}
				meta["loot_id"] = lootID
			} else {
				node, err := t.ts.GetNode(footholdID)
				if err != nil || node == nil || node.Kind != db.KindFoothold {
					return actool.Errorf(fmt.Sprintf("enum_privilege: 据点 %d 不存在或不是据点", footholdID)), nil
				}
				if node.State != db.StateLootActive {
					return actool.Errorf(fmt.Sprintf("enum_privilege: 据点 %d 当前状态是 %q，不能再用", footholdID, node.State)), nil
				}
				v := db.ParseFoothold(node)
				if v.Protocol == "" || normalizeHost(v.Host) != normalizeHost(host) {
					return actool.Errorf("enum_privilege: 据点没有结构化连法，或 host 与登记不一致"), nil
				}
				switch v.Protocol {
				case "ssh":
					cred, cerr := t.ts.GetNode(v.CredentialID)
					if cerr != nil || cred == nil || cred.Kind != db.KindCredential || cred.State != db.StateLootActive {
						return actool.Errorf("enum_privilege: 据点指向的凭据不可用"), nil
					}
					cv := db.ParseCredential(cred)
					kind := strings.ToLower(strings.TrimSpace(cv.Kind))
					if kind != "password" && kind != "key" {
						return actool.Errorf("enum_privilege: 据点的 ssh 凭据只支持 password / key"), nil
					}
					user := strings.TrimSpace(cv.Username)
					if user == "" {
						return actool.Errorf("enum_privilege: 凭据没有登记 username"), nil
					}
					secretPath, serr := writeLootSecret(cv.Secret)
					if serr != nil {
						return actool.Errorf("enum_privilege: 无法暂存凭据：" + serr.Error()), nil
					}
					cleanup = func() { _ = removeLootSecret(secretPath) }
					shellCmd = buildLootCommand(kind, "ssh", user, v.Host, v.Port, secretPath, command)
				case "webshell":
					shellCmd = buildWebshellCommand(v.URL, v.Param, command, timeout)
					cleanup = func() {}
				default:
					return actool.Errorf(fmt.Sprintf("enum_privilege: 据点协议 %q 不可执行", v.Protocol)), nil
				}
				meta["foothold_id"] = footholdID
			}
			desc := fmt.Sprintf("enum_privilege：%s 只读提权枚举（%s）", profile, host)
			st, subErr := t.submitToolMeta(ctx, "enum_privilege", shellCmd, desc, timeout, meta)
			if subErr != nil {
				cleanup()
				return actool.Errorf("enum_privilege: " + subErr.Error()), nil
			}
			if lootID > 0 {
				t.noteLootUse(lootID)
			} else {
				t.noteLootUse(footholdID)
			}
			raw, _ := json.Marshal(st)
			return actool.Text(fmt.Sprintf("enumeration started: %d\n%s\n只读 profile 已提交；用 get_execution(%d) 读取输出，"+
				"再 record_fact 写回结论。不包含凭据抓取或持久化。", st.ID, raw, st.ID)), nil
		})
}
