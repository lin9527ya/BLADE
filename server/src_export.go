package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/evidence"
	"github.com/lin9527ya/blade/report"
)

// 交付面（P11）：把一条发现导出成 src-dig 报告规范的可提交件。
//
// 机器只填它确知的部分——标题 / 目标 URL / 等级 / 涉及接口清单 / 复现整包（来自绑定
// 证据的原始报文）——描述、危害、修复建议给规范占位提示，交人工按写作纪律补写。
// 两种输出（Markdown / docx）共用同一份 report.SrcInput，版式一致。

func (s *Server) registerSrcExport(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/findings/{id}/src-report", s.getFindingSrcReport)
	mux.HandleFunc("GET /api/findings/{id}/src-report.docx", s.getFindingSrcReportDocx)
	// 证据门（P15）：下载前先看"这份简报缺什么"。
	mux.HandleFunc("GET /api/findings/{id}/src-report/check", s.getFindingSrcReportCheck)
	// 批量交付（P19）：一个任务的全部发现 -> 计划预览 / 打包下载。
	mux.HandleFunc("GET /api/tasks/{id}/src-batch", s.getTaskSrcBatch)
	mux.HandleFunc("GET /api/tasks/{id}/src-report.zip", s.getTaskSrcReportZip)
}

func (s *Server) srcFinding(w http.ResponseWriter, r *http.Request) *db.DBFinding {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return nil
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil
	}
	if f == nil {
		writeErr(w, 404, "finding not found")
		return nil
	}
	return f
}

// buildSrcInput assembles the export skeleton from the finding row, its anchored
// assets and its bound traffic evidence.
func (s *Server) buildSrcInput(ctx context.Context, f *db.DBFinding) (report.SrcInput, []string) {
	in := report.SrcInput{
		Title:     report.FindingTitle(f),
		Severity:  f.Severity,
		VulnClass: f.VulnClass,
	}
	if d := strings.TrimSpace(f.Summary); d != "" {
		in.Description = d
		if rep := strings.TrimSpace(f.Report); rep != "" {
			in.Description = d + "\n\n" + rep
		}
	} else if rep := strings.TrimSpace(f.Report); rep != "" {
		in.Description = rep
	}

	// 目标 URL：优先锚定资产的展示标签，其次绑定流量里的 URL。
	assets := s.resolveAssetIDs(f.AssetIDs)
	for _, aid := range f.AssetIDs {
		if a := assets[aid]; a != nil {
			if lbl := strings.TrimSpace(assetLabel(a)); lbl != "" {
				in.TargetURL = lbl
				break
			}
		}
	}

	// warnings 是导出前的证据缺项清单（P15 交付证据门）。空 = 本发现可以直接交。
	traffic, err := s.m.pg.GetFindingTraffic(ctx, f.ID)
	if err != nil {
		traffic = nil
	}
	var roles []string
	if traffic != nil {
		for _, b := range traffic.Bindings {
			roles = append(roles, b.Role)
		}
	}
	warnings := s.srcWarnings(f, roles)
	store := s.evidenceStore()
	if traffic != nil {
		seen := map[string]bool{}
		stepNo := 0
		baselineNote := ""
		for _, b := range traffic.Bindings {
			sn := b.Snapshot
			if in.TargetURL == "" {
				in.TargetURL = sn.URL
			}
			key := strings.TrimSpace(sn.Method + " " + sn.URL)
			if key != "" && !seen[key] {
				seen[key] = true
				in.Endpoints = append(in.Endpoints, report.SrcEndpoint{
					Method: sn.Method, URL: sn.URL, Source: srcBindingSource(b),
				})
			}
			if b.Role == "baseline" {
				// 规范：对照写进那一步的描述里，不单独成步。
				baselineNote = sn.Method + " " + sn.URL
				continue
			}
			if b.Role != "proof" && b.Role != "verification" {
				continue
			}
			stepNo++
			raw := strings.TrimRight(sn.ReqHead, "\r\n")
			if body := srcRequestBody(store, sn); body != "" {
				raw = strings.TrimRight(raw+"\n\n"+body, "\r\n")
			}
			desc := srcRoleLabel(b.Role)
			if n := strings.TrimSpace(b.Note); n != "" {
				desc += "：" + clipRunes(n, 160)
			}
			in.Steps = append(in.Steps, report.SrcStep{Desc: desc, Raw: raw})
		}
		if baselineNote != "" && len(in.Steps) > 0 {
			in.Steps[0].Desc += "（同接口正常对照：" + baselineNote + "，对照请求不单独成步）"
		}
	}

	notes := []string{"发现 #" + itoa64(f.ID) + " · 证据版本 v" + itoa64(f.EvidenceVersion) + " · 生成时间 " + time.Now().Format("2006-01-02 15:04:05")}
	if strings.TrimSpace(f.TaskDescription) != "" {
		notes = append(notes, "来源任务："+clipRunes(f.TaskDescription, 80))
	}
	// 证据门（P15）：缺项既进 Notes 顶部（人下载就看得见），也由 API 以 warnings[]
	// 单独返回（前端/自动化在下载前就能看到）。两者同一份来源，不会各说各话。
	for _, w := range warnings {
		notes = append(notes, "⚠ "+w)
	}
	in.Notes = append(notes, in.Notes...)
	return in, warnings
}

// srcWarnings 是「交付证据门」的唯一实现（P19 起）：每条发现导出前缺什么。
// roles 是该发现的绑定流量角色（空 = 没有任何绑定）。调用方给它，是因为批量交付
// 走的是"一次查完所有发现的绑定"的批量路径，不该为了算警告再逐条查一遍库——
// 两处必须给同一份清单，否则批量包里的话和单条导出的话会互相打架。
func (s *Server) srcWarnings(f *db.DBFinding, roles []string) []string {
	var warnings []string
	if len(roles) == 0 {
		warnings = append(warnings, "该发现没有任何绑定流量：复现步骤与整包证据缺失，请先绑定流量再交付。")
	} else {
		hasProof := false
		for _, r := range roles {
			if r == "proof" || r == "verification" {
				hasProof = true
				break
			}
		}
		if !hasProof {
			warnings = append(warnings, "已绑定流量中没有 proof/verification 角色：复现步骤缺少整包证据，请补绑后重新导出。")
		}
	}
	if f.Status == db.FindingRetracted {
		warnings = append(warnings, "该发现已撤回（复测推翻），不要提交；如需提交请先重新确认。")
	}
	// P13：所属任务还有长任务执行在跑 → 输出尚未落地，此刻的证据可能不完整。
	if f.TaskID != nil {
		if x := s.m.Executions(); x != nil {
			if n := len(x.ActiveForTask(itoa64(*f.TaskID))); n > 0 {
				warnings = append(warnings, fmt.Sprintf(
					"所属任务仍有 %d 个长任务执行未结束（扫描/枚举输出未落地）：本次证据可能不完整，建议等执行收束后重新导出。", n))
			}
		}
	}
	return warnings
}

// srcBindingSource renders an endpoint's provenance for 涉及接口清单.
func srcBindingSource(b db.FindingTrafficBinding) string {
	src := srcRoleLabel(b.Role)
	if n := strings.TrimSpace(b.Note); n != "" {
		src += " · " + clipRunes(n, 60)
	}
	return src
}

func srcRoleLabel(role string) string {
	switch role {
	case "proof":
		return "打穿请求（proof）"
	case "baseline":
		return "正常对照（baseline）"
	case "verification":
		return "补充验证（verification）"
	case "supporting":
		return "辅助证据（supporting）"
	}
	if strings.TrimSpace(role) == "" {
		return "证据"
	}
	return role
}

// srcRequestBody inlines the staged request body when it is textual and small;
// binary or oversized bodies are skipped (the exported step keeps head only).
func srcRequestBody(store *evidence.Store, sn db.TrafficEvidenceSnapshot) string {
	const maxInline = 64 * 1024
	if sn.ReqLen <= 0 || sn.ReqLen > maxInline {
		return ""
	}
	ct := strings.ToLower(sn.ContentType)
	for _, bin := range []string{"multipart", "octet-stream", "image/", "audio/", "video/", "application/zip", "application/pdf"} {
		if strings.Contains(ct, bin) {
			return ""
		}
	}
	body, _, err := store.OpenBody(sn, "request")
	if err != nil {
		return ""
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, maxInline))
	if err != nil || len(b) == 0 || !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
		return ""
	}
	return string(b)
}

func clipRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func (s *Server) getFindingSrcReport(w http.ResponseWriter, r *http.Request) {
	f := s.srcFinding(w, r)
	if f == nil {
		return
	}
	in, _ := s.buildSrcInput(r.Context(), f)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(report.SrcFilename(in.Title, ".md")))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(report.SrcMarkdown(in)))
}

func (s *Server) getFindingSrcReportDocx(w http.ResponseWriter, r *http.Request) {
	f := s.srcFinding(w, r)
	if f == nil {
		return
	}
	in, _ := s.buildSrcInput(r.Context(), f)
	blob, err := report.SrcDOCX(in)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(report.SrcFilename(in.Title, ".docx")))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob)
}

// getFindingSrcReportCheck is the delivery evidence gate (P15): it reports what a
// submission would be missing BEFORE anyone downloads it — no bound traffic, no
// proof/verification step, a retracted finding, still-running long tasks. The
// export itself is never blocked (a human decides); the gate makes the gap loud.
func (s *Server) getFindingSrcReportCheck(w http.ResponseWriter, r *http.Request) {
	f := s.srcFinding(w, r)
	if f == nil {
		return
	}
	in, warnings := s.buildSrcInput(r.Context(), f)
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, 200, map[string]any{
		"ready":     len(warnings) == 0,
		"warnings":  warnings,
		"steps":     len(in.Steps),
		"endpoints": len(in.Endpoints),
	})
}
