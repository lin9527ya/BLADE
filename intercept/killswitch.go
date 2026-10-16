package intercept

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 停机开关：一句话——**现在全停**。
//
// 它和作业时间窗（window.go）是同一类东西的两个极端：窗口是"排好的时段"，
// 开关是"此刻就停"（现场发现打错了目标、客户来电叫停、平台要维护……）。
// 两者都落在同一条判定路径的最前面，理由一样：**先判"该不该动手"，再判"能不能动这个目标"**。
//
// 与 scope_mode 的关系（这条最要紧）：空间门有 off/ask/deny 三档，off 是"不做空间判定"；
// 但停机开关**不是一档**，它是"停"。所以它排在 mode 判定**之前**，scope off 时照样拦——
// 否则"关掉空间门"会顺手把紧急停机也关掉，那是最不该有的耦合。作业时间窗同理（见 CheckScope）。

// SettingKillSwitch 是停机开关的存储键。值是一份 JSON 回执（见 KillSwitchReceipt）——
// 存回执而不是一个 bool，是因为"谁在什么时候因为什么停的、停了几条任务"正是事后要自证的东西。
const SettingKillSwitch = "kill_switch"

// EnvKillSwitch 是环境变量覆盖：置 1/true 时**无条件停机**，与库里的设置无关。
//
// 为什么要有它：库里的设置只有平台自己读得到——如果出事的原因恰恰是平台（或它的数据库），
// 那么"从界面上关掉它"这条路本身就不可信。重启时带上这个变量是最外层的刹车，
// 与 BLADE_SCOPE_ENFORCEMENT 同一种做法（同一类需求：让部署者能在配置之外压一档）。
const EnvKillSwitch = "BLADE_KILL_SWITCH"

// KillSwitchEvent 是一次开关动作（开/关各一条），进历史。
type KillSwitchEvent struct {
	On          bool   `json:"on"`
	At          string `json:"at"`
	By          string `json:"by,omitempty"`
	Reason      string `json:"reason,omitempty"`
	TasksPaused int    `json:"tasks_paused,omitempty"`
}

// KillSwitchReceipt 是停机开关的当前状态 + 最近若干次动作。
type KillSwitchReceipt struct {
	On          bool              `json:"on"`
	At          string            `json:"at,omitempty"`
	By          string            `json:"by,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	TasksPaused int               `json:"tasks_paused,omitempty"`
	History     []KillSwitchEvent `json:"history,omitempty"`
}

// killSwitchHistoryCap 是回执历史的条数上限。回执是给人看的台账，不是审计存储——
// 十条足够回答"最近是谁停的、什么时候解除的"，再多就该进日志了。
const killSwitchHistoryCap = 10

// ReadKillSwitch 读当前开关状态。读不到/解析不了 = **未启用**：与 scopeHours 的降级口径一致
// （读不出来时不让"停"这个动作凭空生效——真要停，环境变量那条路是硬保证）。
func ReadKillSwitch(d *db.DB) KillSwitchReceipt {
	if d == nil {
		return KillSwitchReceipt{}
	}
	raw, ok, err := d.GetSetting(SettingKillSwitch)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return KillSwitchReceipt{}
	}
	var r KillSwitchReceipt
	if json.Unmarshal([]byte(raw), &r) != nil {
		return KillSwitchReceipt{}
	}
	return r
}

// KillSwitchForcedByEnv 报告环境变量是否压着开关。
func KillSwitchForcedByEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvKillSwitch)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// KillSwitchOn 是判定入口：库里的开关 **或** 环境变量，任一为真即停机。
// 返回的 reason 供拒绝话术原样带出（"因为什么停的"是模型与人都要知道的）。
func (i *Interceptor) KillSwitchOn() (bool, string) {
	if KillSwitchForcedByEnv() {
		return true, "环境变量 " + EnvKillSwitch + " 已强制停机"
	}
	st := ReadKillSwitch(i.db)
	if !st.On {
		return false, ""
	}
	if st.Reason != "" {
		return true, st.Reason
	}
	return true, "未填写理由"
}

// WriteKillSwitch 写回执（服务端在停机/解除时调用）。这里只管存取，
// "停机要连带暂停任务"是执行侧的事（server/scope_enforce.go）。
func WriteKillSwitch(d *db.DB, r KillSwitchReceipt) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return d.SetSetting(SettingKillSwitch, string(b))
}

// NewKillSwitchEvent 造一条动作记录（时间戳统一在这里取，免得调用方各写一遍格式）。
func NewKillSwitchEvent(on bool, by, reason string, paused int) KillSwitchEvent {
	return KillSwitchEvent{
		On: on, At: time.Now().Format(time.RFC3339), By: by, Reason: reason, TasksPaused: paused,
	}
}

// AppendKillSwitchHistory 把一次动作追加进历史并截断到上限。
func AppendKillSwitchHistory(hist []KillSwitchEvent, ev KillSwitchEvent) []KillSwitchEvent {
	hist = append([]KillSwitchEvent{ev}, hist...)
	if len(hist) > killSwitchHistoryCap {
		hist = hist[:killSwitchHistoryCap]
	}
	return hist
}
