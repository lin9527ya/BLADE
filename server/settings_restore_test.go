package server

import (
	"testing"

	"github.com/lin9527ya/blade/db"
)

// restoreSetting 让用例对**全局** settings 键的修改在用例结束时恢复原值
// （原本不存在就删掉）。
//
// 为什么需要它：settings 是整库共享的，而不少键会被执行路径全局读取——
// intercept_enabled_tools 决定拦截规则面、notify_enabled 是推送总开关、
// finding_traffic_tools_v1 / finding_workflow_tools_v2_reporter 这类一次性迁移标记
// 还决定 server.New 每次启动重不重跑迁移。用例改了不恢复，后面的用例就在别人的
// 配置上跑：轻则断言时对时错，重则把开发者库里的真实配置冲掉。
//
// 用法：在写入之前调用，注册顺序与恢复顺序由 LIFO 自动保证。
func restoreSetting(t *testing.T, pg *db.DB, key string) {
	t.Helper()
	prev, had, err := pg.GetSetting(key)
	if err != nil {
		t.Fatalf("读取设置 %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			if err := pg.SetSetting(key, prev); err != nil {
				t.Logf("恢复设置 %s 失败: %v", key, err)
			}
			return
		}
		if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, key); err != nil {
			t.Logf("清除设置 %s 失败: %v", key, err)
		}
	})
}
