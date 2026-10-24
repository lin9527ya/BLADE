package server

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/report"
)

// P19 批量交付：一次交付一个任务的全部发现。
//
//	GET /api/tasks/{id}/src-batch       交付计划（清单 + 证据门 + 疑似重复分组，只读）
//	GET /api/tasks/{id}/src-report.zip  打包下载（提交说明 + 每条发现的 md/docx）
//
// 两条路径共用同一份计划，所以"预览里说缺证据/疑似重复"与"包里写的"永远一致。
// 进包按**状态白名单**：被推翻（撤回/误报/忽略/已排除/重复）的不进包，并在清单里写明
// 为什么；同指纹的一组只交一条代表。证据门（缺 proof 流量等）只提示、不阻断——交不交
// 是人决定的，平台负责把缺什么说清楚。

type srcBatchEntry struct {
	ID          int64    `json:"id"`
	Title       string   `json:"title"`
	Severity    string   `json:"severity"`
	SeverityCN  string   `json:"severity_cn"`
	VulnClass   string   `json:"vuln_class"`
	Target      string   `json:"target"`
	Status      string   `json:"status"`
	Steps       int      `json:"steps"`
	Endpoints   int      `json:"endpoints"`
	Warnings    []string `json:"warnings"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	// DuplicateGroup >0 表示这条与同组其它发现疑似同一个洞（组号从 1 起）。
	DuplicateGroup int  `json:"duplicate_group,omitempty"`
	Included       bool `json:"included"`
	// SkipReason 说明这条为什么没进包（状态不符 / 与另一条同指纹）。
	SkipReason string `json:"skip_reason,omitempty"`
}

type srcBatchPlan struct {
	TaskID      string          `json:"task_id"`
	TaskName    string          `json:"task_name"`
	Goal        string          `json:"goal"`
	GeneratedAt string          `json:"generated_at"`
	Entries     []srcBatchEntry `json:"entries"`
	Groups      [][]int64       `json:"duplicate_groups"`
	Warnings    []string        `json:"warnings"`
	Included    int             `json:"included"`
	Skipped     int             `json:"skipped"`
}

// buildSrcBatch 组装交付计划：一次查完所有发现的绑定端点，据此算指纹与分组。
func (s *Server) buildSrcBatch(ctx context.Context, t *Task) (*srcBatchPlan, error) {
	rows, _, err := s.m.pg.ListFindingsPage(db.FindingFilter{TaskID: t.ID, Sort: "time"}, 1, 500)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, f := range rows {
		ids = append(ids, f.ID)
	}
	endpoints, err := s.m.pg.FindingEndpoints(ids)
	if err != nil {
		return nil, err
	}
	// 详细报告（按节点 id）：用来判断导出稿的两个必填段是否齐全。
	tid, _ := strconv.ParseInt(t.ID, 10, 64)
	reports, rerr := s.m.pg.FindingReportsByNode(tid)
	if rerr != nil {
		log.Printf("[src-batch] 任务 %s 详细报告读取失败，完整性检查将按缺失处理：%v", t.ID, rerr)
	}
	assets := s.resolveAssetIDsForFindings(rows)

	plan := &srcBatchPlan{
		TaskID:      t.ID,
		TaskName:    t.Name,
		Goal:        t.Goal,
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		Entries:     make([]srcBatchEntry, 0, len(rows)),
	}
	type groupInfo struct {
		ids []int64
	}
	byFingerprint := map[string]*groupInfo{}
	for _, f := range rows {
		eps := endpoints[f.ID]
		refs := make([]report.EndpointRef, 0, len(eps))
		roles := make([]string, 0, len(eps))
		steps := 0
		for _, e := range eps {
			refs = append(refs, report.EndpointRef{Method: e.Method, URL: e.URL})
			roles = append(roles, e.Role)
			if e.Role == "proof" || e.Role == "verification" {
				steps++
			}
		}
		entry := srcBatchEntry{
			ID: f.ID, Title: report.FindingTitle(f), Severity: f.Severity,
			SeverityCN: report.SrcSeverityLabel(f.Severity), VulnClass: f.VulnClass,
			Status: f.Status, Steps: steps, Endpoints: len(eps),
			Warnings:    s.srcWarnings(f, roles),
			Fingerprint: report.Fingerprint(f.VulnClass, refs),
			Target:      srcTargetLabel(f, assets[f.ID], eps),
		}
		if f.NodeID != nil {
			entry.Warnings = append(entry.Warnings, srcCompletenessWarnings(reports[*f.NodeID])...)
		}
		// 状态白名单：被推翻/证伪/重复/忽略的发现不进包。此前只挡了已撤回——于是
		// 一份交付物里可能带着人工判过的误报，"确认"二字就此贬值。
		if srcIncludedStatus(f.Status) {
			entry.Included = true
			plan.Included++
		} else {
			entry.SkipReason = srcSkipReason(f.Status)
			plan.Skipped++
		}
		if entry.Fingerprint != "" {
			g := byFingerprint[entry.Fingerprint]
			if g == nil {
				g = &groupInfo{}
				byFingerprint[entry.Fingerprint] = g
			}
			g.ids = append(g.ids, f.ID)
			entry.DuplicateGroup = len(plan.Groups) + 1 // 先占位，重组后回填
		}
		plan.Entries = append(plan.Entries, entry)
	}
	// 只保留真正成组的指纹（≥2 条），组号从 1 起重排。
	fp := make([]string, 0, len(byFingerprint))
	for k := range byFingerprint {
		fp = append(fp, k)
	}
	sort.Strings(fp)
	groupOf := map[int64]int{}
	for _, k := range fp {
		g := byFingerprint[k]
		if len(g.ids) < 2 {
			continue
		}
		plan.Groups = append(plan.Groups, g.ids)
		for _, id := range g.ids {
			groupOf[id] = len(plan.Groups)
		}
	}
	for i := range plan.Entries {
		plan.Entries[i].DuplicateGroup = groupOf[plan.Entries[i].ID]
	}
	// 同指纹只交一条代表：重复交付会让客户以为洞很多、严重度合计虚高。
	// 代表取组内第一条**状态允许进包**的；其余标成跳过并指认它重复于哪一条。
	repOf := map[int]int64{}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		if e.DuplicateGroup == 0 || !e.Included {
			continue
		}
		if rep, ok := repOf[e.DuplicateGroup]; ok {
			e.Included = false
			e.SkipReason = fmt.Sprintf("与 #%d 同指纹（疑似同一个洞），只交代表项", rep)
			plan.Included--
			plan.Skipped++
			continue
		}
		repOf[e.DuplicateGroup] = e.ID
	}
	// 证据门只覆盖【进包】的发现：不进包的（已撤回）在"未进包的发现"一节交代，
	// 把它们的缺项也塞进证据门会让"每条进包发现证据都完整"这句话自相矛盾。
	for _, e := range plan.Entries {
		if !e.Included {
			continue
		}
		for _, w := range e.Warnings {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("#%d %s：%s", e.ID, e.Title, w))
		}
	}
	return plan, nil
}

// resolveAssetIDsForFindings 批量取发现的资产展示标签（与单条导出同一口径的第一顺位）。
func (s *Server) resolveAssetIDsForFindings(rows []*db.DBFinding) map[int64]string {
	out := map[int64]string{}
	seen := map[int64]bool{}
	var ids []int64
	for _, f := range rows {
		for _, a := range f.AssetIDs {
			if !seen[a] {
				seen[a] = true
				ids = append(ids, a)
			}
		}
	}
	if len(ids) == 0 {
		return out
	}
	assets := s.resolveAssetIDs(ids)
	for _, f := range rows {
		for _, a := range f.AssetIDs {
			if asset := assets[a]; asset != nil {
				if lbl := strings.TrimSpace(assetLabel(asset)); lbl != "" {
					out[f.ID] = lbl
					break
				}
			}
		}
	}
	return out
}

// srcTargetLabel：优先锚定资产的展示标签，其次绑定流量的 URL（与单条导出同序）。
func srcTargetLabel(f *db.DBFinding, assetLabel string, eps []db.FindingEndpoint) string {
	if assetLabel != "" {
		return assetLabel
	}
	if len(eps) > 0 {
		return eps[0].URL
	}
	return ""
}

func (s *Server) getTaskSrcBatch(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	plan, err := s.buildSrcBatch(r.Context(), t)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, plan)
}

func (s *Server) getTaskSrcReportZip(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	plan, err := s.buildSrcBatch(r.Context(), t)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	blob, err := s.buildSrcZip(r.Context(), t, plan)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	name := report.SrcFilename(t.Name, "")
	if strings.TrimSpace(t.Name) == "" {
		name = "task-" + t.ID
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name+"-交付包.zip"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob)
}

// buildSrcZip 打包：提交说明 + 每条（未撤回）发现的 md 与 docx。
func (s *Server) buildSrcZip(ctx context.Context, t *Task, plan *srcBatchPlan) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err := add("提交说明.md", []byte(srcBatchReadme(t, plan))); err != nil {
		return nil, err
	}
	for _, e := range plan.Entries {
		if !e.Included {
			continue
		}
		f, err := s.m.pg.GetFinding(e.ID)
		if err != nil || f == nil {
			return nil, fmt.Errorf("读取发现 #%d 失败: %v", e.ID, err)
		}
		in, _ := s.buildSrcInput(ctx, f)
		base := fmt.Sprintf("findings/%d-%s", e.ID, srcFileSlug(e.Title, e.VulnClass))
		if err := add(base+".md", []byte(report.SrcMarkdown(in))); err != nil {
			return nil, err
		}
		docx, err := report.SrcDOCX(in)
		if err != nil {
			return nil, err
		}
		if err := add(base+".docx", docx); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// srcFileSlug 把标题变成安全的文件名片段（保留中文，去掉路径分隔符与控制字符）。
func srcFileSlug(title, fallback string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		s = strings.TrimSpace(fallback)
	}
	if s == "" {
		s = "finding"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
		case r < 0x20:
			// drop control chars
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if runes := []rune(out); len(runes) > 40 {
		out = string(runes[:40])
	}
	if out == "" {
		out = "finding"
	}
	return out
}

// srcBatchReadme 是包里的"提交说明"：清单 + 证据门 + 疑似重复。它同时是给人看的
// 交付封面，也是"哪些没进包、为什么"的唯一说明处。
func srcBatchReadme(t *Task, plan *srcBatchPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 交付包说明 · %s\n\n", firstNonEmpty(t.Name, "任务 #"+t.ID))
	fmt.Fprintf(&b, "- 任务：#%s\n", t.ID)
	if strings.TrimSpace(t.Goal) != "" {
		fmt.Fprintf(&b, "- 目标：%s\n", oneLine(t.Goal))
	}
	fmt.Fprintf(&b, "- 生成时间：%s\n", plan.GeneratedAt)
	fmt.Fprintf(&b, "- 发现：共 %d 条，进包 %d 条", len(plan.Entries), plan.Included)
	if plan.Skipped > 0 {
		fmt.Fprintf(&b, "，未进包 %d 条（原因见下）", plan.Skipped)
	}
	b.WriteString("\n\n")

	b.WriteString("## 一、发现清单\n\n")
	if plan.Included == 0 {
		b.WriteString("（本次没有可提交的发现）\n\n")
	}
	n := 0
	for _, e := range plan.Entries {
		if !e.Included {
			continue
		}
		n++
		mark := ""
		if e.DuplicateGroup > 0 {
			mark = fmt.Sprintf("（疑似重复组 %d）", e.DuplicateGroup)
		}
		evidence := fmt.Sprintf("%d 步整包 / %d 个端点", e.Steps, e.Endpoints)
		if len(e.Warnings) > 0 {
			evidence += fmt.Sprintf("，⚠ %d 项待补", len(e.Warnings))
		}
		fmt.Fprintf(&b, "%d. **#%d %s**%s\n", n, e.ID, e.Title, mark)
		fmt.Fprintf(&b, "   - 等级：%s；类型：%s；目标：%s\n", e.SeverityCN, firstNonEmpty(e.VulnClass, "—"), firstNonEmpty(e.Target, "—"))
		fmt.Fprintf(&b, "   - 证据：%s\n", evidence)
		fmt.Fprintf(&b, "   - 附件：findings/%d-%s.md（规范简报）、.docx（可提交件）\n", e.ID, srcFileSlug(e.Title, e.VulnClass))
	}
	b.WriteString("\n")

	b.WriteString("## 二、导出前的证据门（缺什么、要不要补）\n\n")
	if len(plan.Warnings) == 0 {
		b.WriteString("无：每条进包发现的证据都完整（有绑定流量且含 proof/verification 整包）。\n\n")
	} else {
		for _, w := range plan.Warnings {
			fmt.Fprintf(&b, "- ⚠ %s\n", w)
		}
		b.WriteString("\n这些**不阻断导出**：交不交由人决定，平台只负责把缺什么说清楚。\n\n")
	}

	b.WriteString("## 三、疑似重复（同一漏洞被多次登记）\n\n")
	if len(plan.Groups) == 0 {
		b.WriteString("无。\n")
	} else {
		b.WriteString("同一漏洞类型 + 同一组端点（方法/路径/参数名一致，payload 与措辞无关）会被归为一组。\n")
		b.WriteString("提交前建议：确认是同一个洞就只交一条（或合并成一条），确实是不同的洞再分别提交。\n\n")
		byID := map[int64]srcBatchEntry{}
		for _, e := range plan.Entries {
			byID[e.ID] = e
		}
		for i, g := range plan.Groups {
			b.WriteString(fmt.Sprintf("- 组 %d（指纹 %s）：", i+1, byID[g[0]].Fingerprint))
			for j, id := range g {
				if j > 0 {
					b.WriteString("、")
				}
				fmt.Fprintf(&b, "#%d「%s」", id, byID[id].Title)
			}
			b.WriteString("\n")
		}
	}

	if plan.Skipped > 0 {
		b.WriteString("\n## 四、未进包的发现\n\n")
		for _, e := range plan.Entries {
			if e.Included {
				continue
			}
			reason := e.SkipReason
			if reason == "" {
				reason = "状态不符，不进交付包"
			}
			fmt.Fprintf(&b, "- #%d「%s」：%s\n", e.ID, e.Title, reason)
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

// srcIncludedStatus 决定一条发现能不能进交付包。口径与总报告的「确认发现」一致：
// 被推翻/证伪/重复/忽略的不进——交一份带着已知误报的包，比少交一条更糟。
// open_proof_gap（存疑待补证）仍进包，但 srcWarnings 会点名缺什么。
func srcIncludedStatus(status string) bool {
	switch status {
	case db.FindingRetracted, db.FindingFalsePositive, db.FindingDuplicate, db.FindingIgnored, db.FindingRuledOut:
		return false
	}
	return true
}

func srcSkipReason(status string) string {
	switch status {
	case db.FindingRetracted:
		return "已撤回（复测推翻），不进交付包"
	case db.FindingFalsePositive:
		return "已判定为误报，不进交付包"
	case db.FindingDuplicate:
		return "已标记为重复，不进交付包"
	case db.FindingIgnored:
		return "已忽略，不进交付包"
	case db.FindingRuledOut:
		return "已排除（有反证），不进交付包"
	}
	return "状态不符，不进交付包"
}
