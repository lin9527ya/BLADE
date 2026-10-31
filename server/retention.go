package server

import (
	"context"
	"log"
	"time"
)

// retentionSweepInterval 是数据库保留清理的扫描节奏。一天一次足够：保留期以「天」
// 为单位，早几小时晚几小时没有区别，而扫得太勤只会白费 IO。
const retentionSweepInterval = 24 * time.Hour

// startRetentionSweeper 起一个后台清理器：按各表自己的保留天数删旧数据。
//
// 与文件清理（存储页的 P28）共用同一套契约：保留天数 0/未设 = **不清理**。
// 默认全 0，所以装上平台不会自己删东西——只有显式配了保留期的表才会动，
// 且每张表各自判定（配了 0 的那张完全不参与）。
func (s *Server) startRetentionSweeper(ctx context.Context) {
	s.loops.start(func() {
		// 启动后先跑一轮：进程可能已经停了很久，旧数据不该等到 24 小时后才清。
		s.runRetentionSweep(ctx)
		t := time.NewTicker(retentionSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runRetentionSweep(ctx)
			}
		}
	})
}

// runRetentionSweep 跑一轮：逐个数据库分类按各自保留天数清理。
// 每一档独立判定、独立容错——一张表清理失败不该影响其他表。
func (s *Server) runRetentionSweep(ctx context.Context) {
	if s.m == nil || s.m.pg == nil {
		return
	}
	for _, spec := range storageCategories {
		if spec.DBTable == "" && !spec.TrafficIndex {
			continue // 文件分类由存储页手动触发，不参与每日自动清理
		}
		days := s.storageRetentionDays(spec.SettingKey)
		if days <= 0 {
			continue // 0 = 不清理（默认）
		}
		n, err := s.purgeRetentionCategory(ctx, spec, days)
		if err != nil {
			log.Printf("[retention] %s 清理失败：%v", spec.Label, err)
			continue
		}
		if n > 0 {
			log.Printf("[retention] %s：清理 %d 行（保留 %d 天）", spec.Label, n, days)
		}
	}
}
