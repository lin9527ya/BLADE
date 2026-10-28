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
