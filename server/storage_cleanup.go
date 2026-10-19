package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
	// DBTable 非空 = 这是**数据库**分类（不是文件分类）。判定与执行都走 db.Retention*
	// 那套：同一个保留契约，只是对象从目录变成了表。
	DBTable db.RetentionTable
	// DBBodies = 这一档只清空 llm_records 的四个大 TEXT 载荷、保留元数据行
	// （第一级回收）。与整行删的 DBTable 档位分开设置，因为两者保留期通常不同。
	DBBodies bool
	// TrafficIndex = 这是流量录制器的归档（不在 Postgres 里，而在它自己的 SQLite
	// 索引 + blob 目录里）。统计与删除都走 traffic 包的 API——绕过那个包直接删文件
	// 会把索引打烂，这正是这一档此前标着"只统计不清理"的原因。
	TrafficIndex bool
}

// storageCategories 是参与体检的分类。顺序即页面顺序：先文件、后数据库。
var storageCategories = []storageCategorySpec{
	{Key: "tasks", Label: "任务产物", SettingKey: settingStorageRetentionTasks},
	{Key: "sessions", Label: "会话工作目录", SettingKey: settingStorageRetentionSessions},
	{Key: "drafts", Label: "草稿暂存", SettingKey: settingStorageRetentionDrafts},
	{Key: "transcripts", Label: "会话转写", SettingKey: settingStorageRetentionTranscript},
	{Key: "binary", Label: "更新残留（旧二进制）", SettingKey: settingStorageRetentionBinary},
	// 流量归档此后可清：traffic.DeleteOlderThan 在同一个写锁下同时删索引行、正文行、
	// blob 引用与磁盘上的 blob/遗留目录，并按"该 host 还有没有新流量"决定目录去留。
	{Key: "traffic", Label: "流量归档（录制器）", SettingKey: settingRetentionTraffic, TrafficIndex: true},
	// 数据库侧。前三个是 always-on 写的表（每次 LLM 调用一行 / 每个 agent 步骤一行 /
	// 每条服务端日志一行），是无限增长的主力，也是这里最该配的。
	{Key: "db_llm_usage", Label: "LLM 用量台账（数据库）", SettingKey: settingRetentionLLMUsage, DBTable: db.RetentionLLMUsage},
	{Key: "db_activity", Label: "执行活动流（数据库）", SettingKey: settingRetentionActivity, DBTable: db.RetentionActivity},
	{Key: "db_server_logs", Label: "后端日志（数据库）", SettingKey: settingRetentionServerLogs, DBTable: db.RetentionServerLogs},
	// llm_records 默认不写（设置里的 llm_record 开关），所以排在后；它是两级回收：
	// 先按载荷档清空四个大 TEXT（保留元数据行），更早的再由整行档删掉。
	{Key: "db_llm_record_bodies", Label: "LLM 录制载荷（数据库）", SettingKey: settingRetentionLLMRecordBodys, DBTable: db.RetentionLLMRecords, DBBodies: true},
	{Key: "db_llm_records", Label: "LLM 录制整行（数据库）", SettingKey: settingRetentionLLMRecords, DBTable: db.RetentionLLMRecords},
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

// retentionProtection 返回受保护的任务 id 与其探索 id：**非终态任务**的记录一律不动。
// 与 protectedTaskIDs 同源（都读 ListTasks 判终态），但这里额外要探索 id——
// activity 是按 exploration_id 归属任务的，只拿任务 id 保护不了它。
// 读库失败返回错误而不是空集合——空集合会让"在跑的任务"变成可删，方向反了。
func (s *Server) retentionProtection() (tasks []string, explorations []int64, err error) {
	all, err := s.m.pg.ListTasks()
	if err != nil {
		return nil, nil, err
	}
	tasks = []string{}
	explorations = []int64{}
	for _, t := range all {
		if t == nil || db.IsTerminal(t.Status) {
			continue
		}
		tasks = append(tasks, strconv.FormatInt(t.ID, 10))
		if t.ExplorationID > 0 {
			explorations = append(explorations, t.ExplorationID)
		}
	}
	return tasks, explorations, nil
}

// scanRetentionCategory 是数据库分类的体检：只读统计，不动数据。
//
// 与文件分类同一套口径：保留天数 0 = **不标记任何可删项**（但表的总占用照常显示——
// "这张表现在多大"是独立于保留策略的信息，也是发现无限增长的入口）。
func (s *Server) scanRetentionCategory(ctx context.Context, spec storageCategorySpec, days int) (storageCategoryReport, error) {
	rep := storageCategoryReport{Key: spec.Key, Label: spec.Label, RetentionDays: days}
	now := time.Now()
	if spec.TrafficIndex {
		// 流量归档在录制器自己的 SQLite 里。它不归属任务——删的是"多久以前的流量"，
		// 不是"哪个任务的流量"——所以没有需要保护的任务集合。
		tr := s.m.Traffic()
		if tr == nil {
			return rep, nil // 没启用录制 = 空分类，不是错误
		}
		st, err := tr.RetentionStats(now.AddDate(0, 0, -days))
		if err != nil {
			return rep, err
		}
		rep.Units, rep.Bytes = int(st.Exchanges), st.BodyBytes
		if days > 0 {
			rep.ExpiredUnits, rep.ExpiredBytes = int(st.ExpiredExchanges), st.ExpiredBytes
		}
		return rep, nil
	}
	tasks, explorations, err := s.retentionProtection()
	if err != nil {
		return rep, fmt.Errorf("读取任务状态失败（拿不到'哪些任务在跑'就不该开始清理）：%w", err)
	}
	if spec.DBBodies {
		// 第一级回收自己的视图：还有载荷可清的行。两档保留期不同，所以分开统计。
		all, err := s.m.pg.LLMRecordPayloadStats(ctx, now, tasks)
		if err != nil {
			return rep, err
		}
		rep.Units, rep.Bytes = int(all.Rows), all.Bytes
		if days > 0 {
			expired, err := s.m.pg.LLMRecordPayloadStats(ctx, now.AddDate(0, 0, -days), tasks)
			if err != nil {
				return rep, err
			}
			rep.ExpiredUnits, rep.ExpiredBytes = int(expired.ExpiredRows), expired.ExpiredBytes
		}
		return rep, nil
	}
	cutoff := now
	if days > 0 {
		cutoff = now.AddDate(0, 0, -days)
	}
	st, err := s.m.pg.RetentionStats(ctx, spec.DBTable, cutoff, tasks, explorations)
	if err != nil {
		return rep, err
	}
	rep.Units, rep.Bytes = int(st.Rows), st.Bytes
	if days > 0 {
		rep.ExpiredUnits = int(st.ExpiredRows)
		rep.ExpiredBytes = st.ExpiredBytes
		// 单看时间就过期、却被非终态任务挡下的行数——让人看得见"为什么删不掉"。
		rep.Protected = int(st.ExpiredRowsAll - st.ExpiredRows)
	}
	return rep, nil
}

// storageCategorySpecByKey 按 key 找分类规格（执行阶段要用它区分文件/数据库）。
func storageCategorySpecByKey(key string) (storageCategorySpec, bool) {
	for _, spec := range storageCategories {
		if spec.Key == key {
			return spec, true
		}
	}
	return storageCategorySpec{}, false
}

// purgeRetentionCategory 执行一个数据库分类的清理，返回删除（或清空载荷）的行数。
// 保留天数为 0 = **不清理**——与文件分类同一条铁律，这里再挡一道，
// 免得将来有人从别的路径调进来绕过了设置判定。
func (s *Server) purgeRetentionCategory(ctx context.Context, spec storageCategorySpec, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	if spec.TrafficIndex {
		// 走 traffic 包的 API：索引行、正文行、blob 引用与磁盘 blob/遗留目录要一起
		// 处理，且目录去留取决于"该 host 还有没有新流量"。绕开这个包直接删文件会把
		// 索引打烂。
		tr := s.m.Traffic()
		if tr == nil {
			return 0, nil
		}
		return tr.DeleteOlderThan(cutoff)
	}
	tasks, explorations, err := s.retentionProtection()
	if err != nil {
		return 0, err
	}
	if spec.DBBodies {
		return s.m.pg.StripLLMRecordBodiesBefore(ctx, cutoff, tasks, 0)
	}
	switch spec.DBTable {
	case db.RetentionLLMUsage:
		return s.m.pg.PurgeLLMUsageBefore(ctx, cutoff, tasks, 0)
	case db.RetentionActivity:
		// activity 按 exploration_id 归属任务，保护的是探索 id。
		return s.m.pg.PurgeActivityBefore(ctx, cutoff, explorations, 0)
	case db.RetentionServerLogs:
		return s.m.pg.PurgeServerLogsBefore(ctx, cutoff, 0)
	case db.RetentionLLMRecords:
		return s.m.pg.PurgeLLMRecordsBefore(ctx, cutoff, tasks, 0)
	}
	return 0, fmt.Errorf("未知的数据库清理分类 %q", spec.Key)
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
		days := s.storageRetentionDays(spec.SettingKey)
		var cat storageCategoryReport
		var cerr error
		if spec.DBTable != "" || spec.TrafficIndex {
			cat, cerr = s.scanRetentionCategory(ctx, spec, days)
		} else {
			cat, cerr = s.scanStorageCategory(spec, days, protected)
		}
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
		spec, ok := storageCategorySpecByKey(cat.Key)
		if !ok {
			continue
		}
		if spec.DBTable != "" || spec.TrafficIndex {
			n, derr := s.purgeRetentionCategory(r.Context(), spec, cat.RetentionDays)
			if derr != nil {
				failures = append(failures, fmt.Sprintf("%s：%v", cat.Label, derr))
				continue
			}
			if n > 0 {
				// bytes 是试算阶段估的行载荷（不含索引/TOAST），不是精确的释放量——
				// PG 的索引空间要等 vacuum 才还回去，这里如实按估算记。
				deleted = append(deleted, map[string]any{
					"category": cat.Key, "name": cat.Label, "rows": n, "bytes": cat.ExpiredBytes,
				})
				deletedBytes += cat.ExpiredBytes
			}
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
		if serr := s.m.pg.SetSetting(settingStorageLastClean, string(raw)); serr != nil {
			log.Printf("[storage] 记录上次清理摘要失败: %v（清理本身已完成，只是页面看不到记录）", serr)
		}
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
