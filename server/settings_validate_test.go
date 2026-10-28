package server

import (
	"strings"
	"testing"
)

// 校验必须能在第一次写库之前拦下任何非法字段：后字段非法时，前字段不能被改掉。
// 这里用真 server 走一次完整 PUT，断言 400 且前字段保持原值。
func TestSettingsInvalidFieldLeavesNoPartialWrite(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	before, _, _ := s.m.pg.GetSetting(settingSiteName)
	t.Cleanup(func() { _ = s.m.pg.SetSetting(settingSiteName, before) })

	w := request("PUT", "/api/settings", `{
		"site_name":"changed-before-invalid",
		"notify_digest_interval_min":0
	}`)
	if w.Code != 400 {
		t.Fatalf("非法汇总周期应 400，得 %d %s", w.Code, w.Body)
	}
	after, _, _ := s.m.pg.GetSetting(settingSiteName)
	if after != before {
		t.Fatalf("后字段非法时前字段已被写入：%q → %q", before, after)
	}
}

// 纯校验函数的边界：合法请求零误报，各类非法值给出可读错误。
func TestValidateSettingsRequest(t *testing.T) {
	s := &Server{}
	bad := 0
	badBudget := -1
	badNoise := "loud"
	badBackend := "bing"
	cases := []struct {
		name string
		req  settingsRequest
		want string
	}{
		{"合法空请求", settingsRequest{}, ""},
		{"汇总周期为 0", settingsRequest{NotifyDigestMins: &bad}, "汇总周期"},
		{"负预算", settingsRequest{CredentialBudget: &badBudget}, "凭据尝试预算"},
		{"噪声档位非法", settingsRequest{OpsecNoise: &badNoise}, "opsec_noise_level"},
		{"未知搜索后端", settingsRequest{WebSearchBackend: &badBackend}, "未知的搜索后端"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.validateSettingsRequest(tc.req)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("合法请求被拒：%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
		})
	}
}

// 原子提交：一次 PUT 里任一键写入失败时，整批都不落库（这里用关闭事务的方式
// 模拟写失败——用不存在的表无法做到，改用非法值在服务端拦截已在别处覆盖；
// 这里验证 SetSettings 本身的事务语义：批量成功全写，空批次不写）。
func TestSetSettingsBulk(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	k1, k2 := settingSiteName, settingNotifyDigestMinutes
	old1, has1, _ := s.m.pg.GetSetting(k1)
	old2, has2, _ := s.m.pg.GetSetting(k2)
	t.Cleanup(func() {
		if has1 {
			_ = s.m.pg.SetSetting(k1, old1)
		}
		if has2 {
			_ = s.m.pg.SetSetting(k2, old2)
		}
	})
	if err := s.m.pg.SetSettings(map[string]string{k1: "bulk-a", k2: "17"}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.m.pg.GetSetting(k1); v != "bulk-a" {
		t.Fatalf("k1 = %q", v)
	}
	if v, _, _ := s.m.pg.GetSetting(k2); v != "17" {
		t.Fatalf("k2 = %q", v)
	}
	if err := s.m.pg.SetSettings(nil); err != nil {
		t.Fatalf("empty batch must be a no-op: %v", err)
	}
}

// 设置保存顺序：运行态应用在事务提交之后。用一个带内存态的开关验证——
// LLM 录制开启后 Manager 的内存标志必须同步（提交成功后 apply 才跑）。
func TestPutSettingsAppliesRuntimeAfterCommit(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	oldOn, _, _ := s.m.pg.GetSetting(settingLLMRecord)
	t.Cleanup(func() { _ = s.m.pg.SetSetting(settingLLMRecord, oldOn) })

	w := request("PUT", "/api/settings", `{"llm_record":true}`)
	if w.Code != 200 {
		t.Fatalf("put settings: %d %s", w.Code, w.Body)
	}
	if !s.m.LLMRecordEnabled() {
		t.Fatal("runtime apply did not run after commit")
	}
}
