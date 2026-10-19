package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// P28 存储清理：把 <数据目录> 下的产物按分类扫描 → 保留天数 → **试算（不动盘）** → 执行。
//
// 与 CyberStrikeAI 的 storage.Cleaner 同构（Inspect/Clean 两段式、按分类保留、报告先行），
// 但判定依据按 BLADE 的真值来：
//   - 「过期」用单元内**最新写入时间**（递归取 max mtime）——它不是访问时间的猜测，
//     是"最后一次落笔"，可解释、可复现；
//   - 「不能删」用 **DB 里非终态的任务集合**（running/queued/paused…），不用文件时间启发式
//     去猜哪些任务在跑——那是猜，而 DB 里是事实；
//   - 保留天数为 0/未设 = **不清理**（默认什么都不删），这是能在生产上放心启用的前提。
//
// 边界（如实写在页面上）：traffic/ 只统计不清理——_blobs 与 _index 是一套，
// 绕过 traffic 包去删会把索引打烂；要清它得先给 traffic 加删除 API，那是另一件事。

type storageCategorySpec struct {
	Key        string
	Label      string
	SettingKey string // 保留天数设置键
	// StatsOnly 非空 = 只统计不清理，值就是"为什么不动它"的理由（原样展示给人看）。
	StatsOnly string
}

// storageCategories 是参与体检的分类。顺序即页面顺序。
var storageCategories = []storageCategorySpec{
	{Key: "tasks", Label: "任务产物", SettingKey: settingStorageRetentionTasks},
	{Key: "sessions", Label: "会话工作目录", SettingKey: settingStorageRetentionSessions},
	{Key: "drafts", Label: "草稿暂存", SettingKey: settingStorageRetentionDrafts},
	{Key: "transcripts", Label: "会话转写", SettingKey: settingStorageRetentionTranscript},
	{Key: "binary", Label: "更新残留（旧二进制）", SettingKey: settingStorageRetentionBinary},
	{Key: "traffic", Label: "流量归档", StatsOnly: "blobs 与索引是一套，绕过 traffic 包删除会把索引打烂——先用流量页的归档/导出，或在 traffic 里补删除 API"},
}

// storageUnit 是一个可删单元（任务目录 / 会话目录 / 单个转写文件…）。
type storageUnit struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	ModTime time.Time `json:"mod_time"`
	// Protected 非空表示**不许删**，值是理由（在跑的任务 / 目标不存在…）。
	Protected string `json:"protected,omitempty"`
}

type storageCategoryReport struct {
	Key           string        `json:"key"`
	Label         string        `json:"label"`
	StatsOnly     string        `json:"stats_only,omitempty"`
	RetentionDays int           `json:"retention_days"`
	Units         int           `json:"units"`
	Bytes         int64         `json:"bytes"`
	ExpiredUnits  int           `json:"expired_units"`
	ExpiredBytes  int64         `json:"expired_bytes"`
	Protected     int           `json:"protected_units"`
	Items         []storageUnit `json:"items"`
}

type storageReport struct {
	Dir        string                  `json:"dir"`
	DiskTotal  int64                   `json:"disk_total"`
	DiskFree   int64                   `json:"disk_free"`
	ScannedAt  time.Time               `json:"scanned_at"`
	Categories []storageCategoryReport `json:"categories"`
}

// storageRetentionDays 读一个分类的保留天数（未设/非法 = 0 = 不清理）。
func (s *Server) storageRetentionDays(key string) int {
	if key == "" {
		return 0
	}
	v, ok, _ := s.m.pg.GetSetting(key)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// protectedTaskIDs 返回**非终态**任务的 id 集合：这些任务的产物一律不动。
// 读库失败返回错误而不是空集合——空集合会让"在跑的任务"变成可删，方向反了。
func (s *Server) protectedTaskIDs() (map[string]bool, error) {
	tasks, err := s.m.pg.ListTasks()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range tasks {
		if t != nil && !db.IsTerminal(t.Status) {
			out[strconv.FormatInt(t.ID, 10)] = true
		}
	}
	return out, nil
}

// scanStorageUnit 递归统计一个单元的字节数与**最新写入时间**（不跟随符号链接：
// 跟着走会把统计带出数据目录，删的时候也可能删到别处）。
func scanStorageUnit(path string) (int64, time.Time, error) {
	var total int64
	var newest time.Time
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 读不到就跳过这一项，不让单个坏项毁掉整次体检
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += info.Size()
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return 0, time.Time{}, err
	}
	if newest.IsZero() {
		// 空目录：用目录自身的 mtime（否则空目录会因为"没有最新写入"而永远不过期）。
		if info, ierr := os.Stat(path); ierr == nil {
			newest = info.ModTime()
		}
	}
	return total, newest, nil
}

// storageUnitExpired 是保留判定的唯一实现（纯函数，表驱动测试钉住）。
func storageUnitExpired(u storageUnit, retentionDays int, now time.Time) bool {
	if u.Protected != "" {
		return false
	}
	if retentionDays <= 0 {
		return false // 0 = 不清理
	}
	return now.Sub(u.ModTime) > time.Duration(retentionDays)*24*time.Hour
}

// scanStorageCategory 扫一个分类：列出单元、算大小与年龄、标出受保护的。
func (s *Server) scanStorageCategory(spec storageCategorySpec, days int, protected map[string]bool) (storageCategoryReport, error) {
	rep := storageCategoryReport{Key: spec.Key, Label: spec.Label, StatsOnly: spec.StatsOnly, RetentionDays: days}
	root := s.storageCategoryRoot(spec.Key)
	if root == "" {
		return rep, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rep, nil
		}
		return rep, err
	}
	units := make([]storageUnit, 0, len(entries))
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		var bytes int64
		var mod time.Time
		if e.IsDir() {
			bytes, mod, err = scanStorageUnit(p)
			if err != nil {
				continue
			}
		} else {
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			bytes, mod = info.Size(), info.ModTime()
		}
		u := storageUnit{Name: e.Name(), Path: p, Bytes: bytes, ModTime: mod}
		if spec.Key == "tasks" && protected[e.Name()] {
			u.Protected = "任务未到终态（在跑/排队/暂停）"
		}
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ModTime.Before(units[j].ModTime) })
	rep.Items = units
	rep.Units = len(units)
	for _, u := range units {
		rep.Bytes += u.Bytes
		if u.Protected != "" {
			rep.Protected++
		}
	}
	if spec.StatsOnly == "" {
		now := time.Now()
		for _, u := range units {
			if storageUnitExpired(u, days, now) {
				rep.ExpiredUnits++
				rep.ExpiredBytes += u.Bytes
			}
		}
	}
	return rep, nil
}

// storageCategoryRoot 返回分类对应的目录；"" = 该分类没有目录（如二进制残留由扫描器自己找）。
func (s *Server) storageCategoryRoot(key string) string {
	switch key {
	case "tasks", "sessions", "drafts", "transcripts", "traffic":
		return filepath.Join(s.m.dir, key)
	case "binary":
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		return filepath.Dir(exe)
	}
	return ""
}

// storageReport 生成整份体检报告（试算：只读，不动盘）。
func (s *Server) storageReport(ctx context.Context) (*storageReport, error) {
	protected, err := s.protectedTaskIDs()
	if err != nil {
		return nil, fmt.Errorf("读取任务状态失败（拿不到'哪些任务在跑'就不该开始清理）：%w", err)
	}
	rep := &storageReport{Dir: s.m.dir, ScannedAt: time.Now()}
	rep.DiskTotal, rep.DiskFree = diskUsage(s.m.dir)
	for _, spec := range storageCategories {
		cat, cerr := s.scanStorageCategory(spec, s.storageRetentionDays(spec.SettingKey), protected)
		if cerr != nil {
			return nil, fmt.Errorf("扫描 %s 失败：%w", spec.Label, cerr)
		}
		rep.Categories = append(rep.Categories, cat)
	}
	return rep, nil
}

// getStorageReport 是页面上的「试算」：只读，不写盘、不删任何东西。
func (s *Server) getStorageReport(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	rep, err := s.storageReport(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	last := map[string]any{}
	if v, ok, _ := s.m.pg.GetSetting(settingStorageLastClean); ok && strings.TrimSpace(v) != "" {
		_ = json.Unmarshal([]byte(v), &last)
	}
	writeJSON(w, 200, map[string]any{"report": rep, "last_clean": last})
}

// cleanStorage 执行清理：**只删过期且未受保护的单元**，删完出报告。
// body 可选 {"categories": ["tasks", ...]} 限定分类；缺省 = 所有可清理分类都按各自保留天数走。
func (s *Server) cleanStorage(w http.ResponseWriter, r *http.Request) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		Categories []string `json:"categories"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	want := map[string]bool{}
	for _, c := range req.Categories {
		want[strings.TrimSpace(c)] = true
	}

	// 关键：**执行前重新取一次**保护集合与报告（试算与执行之间任务状态会变），
	// 判定只用这一次的结果——不复用页面拿到的旧报告。
	rep, err := s.storageReport(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	now := time.Now()
	deleted := []map[string]any{}
	var deletedBytes int64
	failures := []string{}
	for ci := range rep.Categories {
		cat := &rep.Categories[ci]
		if cat.StatsOnly != "" {
			continue
		}
		if len(want) > 0 && !want[cat.Key] {
			continue
		}
		root := s.storageCategoryRoot(cat.Key)
		for _, u := range cat.Items {
			if !storageUnitExpired(u, cat.RetentionDays, now) {
				continue
			}
			// 二次把关：删除路径必须落在分类根目录之内（防符号链接/拼接误删别处）。
			if !pathWithin(root, u.Path) {
				failures = append(failures, fmt.Sprintf("%s：路径不在分类目录内，已跳过", u.Path))
				continue
			}
			if rerr := os.RemoveAll(u.Path); rerr != nil {
				failures = append(failures, fmt.Sprintf("%s：%v", u.Path, rerr))
				continue
			}
			deleted = append(deleted, map[string]any{"category": cat.Key, "name": u.Name, "bytes": u.Bytes, "mod_time": u.ModTime})
			deletedBytes += u.Bytes
		}
	}
	summary := map[string]any{
		"at":         now,
		"deleted":    len(deleted),
		"bytes":      deletedBytes,
		"categories": req.Categories,
		"failures":   failures,
		"items":      deleted,
	}
	if raw, merr := json.Marshal(summary); merr == nil {
		_ = s.m.pg.SetSetting(settingStorageLastClean, string(raw))
	}
	// 这条是**成功**汇总：以前因为文案里有「失败 %d 项」被判成红色 error（那个数字
	// 通常是 0）。级别由调用点写明。
	slogFor("storage").Info("清理完成",
		"deleted", len(deleted), "freed_bytes", deletedBytes, "failed", len(failures))

	// 删完再体检一次：页面拿到的 always 是**当下真实**的占用，而不是"删之前的报告减一减"。
	after, rerr := s.storageReport(r.Context())
	if rerr != nil {
		after = rep
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted, "deleted_bytes": deletedBytes, "failures": failures, "report": after, "last_clean": summary})
}

// pathWithin 判断 p 是否在 root 之内（先 Clean 再比较前缀 + 分隔符，防 /data/tasks-evil 这类）。
func pathWithin(root, p string) bool {
	r := filepath.Clean(root)
	c := filepath.Clean(p)
	if r == "" || c == r {
		return false
	}
	return strings.HasPrefix(c, r+string(filepath.Separator))
}
