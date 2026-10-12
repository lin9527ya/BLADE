package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
)

// TestAcceptance 是 P1–P7 机制 + P11 交付导出 + P12 拦截可信化的端到端验收清单：驱动真实的 HTTP 处理器、真实的工具
// 与真实的引擎路径（只有 LLM 与外部目标被夹具替代），每个机制一个子测试。
//
//	go test -count=1 -run TestAcceptance -v ./server/   # 逐机制 PASS/FAIL
//	./acceptance.sh                                     # 一键：一次性隔离库 + 清单
//
// 需要 Postgres（无则整体 skip）。已知边界：P4 的"100% 后落终态"一环需要 LLM 就绪
// 才能跑完终局规划轮，这里断言到"进入收尾时序"为止（终态落库由单测与真实任务覆盖）。
func TestAcceptance(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	td := t.TempDir()
	s := New(ctx, m, td, td, td)
	// LIFO：先停全部任务、关库，再轮到 TempDir 清理（避免后台写与 RemoveAll 竞态）。
	t.Cleanup(func() {
		for _, tk := range m.List() {
			s.engine.StopTask(tk.ID)
		}
		_ = m.Close()
	})
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	env := &accEnv{t: t, m: m, s: s, token: token, ctx: ctx}

	t.Run("P1 语义终态与发现撤回入报告", func(t *testing.T) { accP1(t, env.forTest(t)) })
	t.Run("P2 覆盖矩阵（资产×风险类）入报告", func(t *testing.T) { accP2(t, env.forTest(t)) })
	t.Run("P3 复测结论入报告复核段", func(t *testing.T) { accP3(t, env.forTest(t)) })
	t.Run("P4 预算 70/85% 提示与 100% 收尾时序", func(t *testing.T) { accP4(t, env.forTest(t)) })
	t.Run("P5 租约看门狗回收僵死意图", func(t *testing.T) { accP5(t, env.forTest(t)) })
	t.Run("P6 候选三态闭合入报告", func(t *testing.T) { accP6(t, env.forTest(t)) })
	t.Run("P7 常驻技能注入 system 稳定段", func(t *testing.T) { accP7(t, env.forTest(t)) })
	t.Run("P11 SRC 交付导出（简报 md → docx 可提交件）", func(t *testing.T) { accP11(t, env.forTest(t)) })
	t.Run("P12 拦截遮挡面与失效安全", func(t *testing.T) { accP12(t, env.forTest(t)) })
}

type accEnv struct {
	t     *testing.T
	m     *Manager
	s     *Server
	token string
	ctx   context.Context
}

// newTask 建一个验收任务，并登记收尾：停引擎运行时 → 清本任务 findings/用量行 → 删任务。
// forTest rebinds the env to a subtest's *testing.T so a failure reds only that
// subtest (FailNow on the parent would abort the remaining checklist).
func (e *accEnv) forTest(t *testing.T) *accEnv {
	c := *e
	c.t = t
	return &c
}

func (e *accEnv) newTask(desc, goal string) (*Task, int64) {
	e.t.Helper()
	task, err := e.m.CreateTask(desc, goal, nil, 0, 0)
	if err != nil {
		e.t.Fatalf("create task: %v", err)
	}
	tid, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		e.s.engine.StopTask(task.ID)
		_, _ = e.m.pg.Exec(`DELETE FROM findings WHERE task_id=$1`, tid)
		_, _ = e.m.pg.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, task.ID)
		_ = e.m.pg.DeleteTask(tid)
	})
	return task, tid
}

func (e *accEnv) request(method, path, body string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+e.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *accEnv) report(task *Task) string {
	e.t.Helper()
	rec := e.request(http.MethodGet, "/api/report?task="+task.ID, "")
	if rec.Code != http.StatusOK {
		e.t.Fatalf("report status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func (e *accEnv) mustContain(hay, needle, what string) {
	e.t.Helper()
	if !strings.Contains(hay, needle) {
		e.t.Fatalf("%s：缺少 %q\n--- 报告全文 ---\n%s", what, needle, hay)
	}
}

// newFinding 经真实 report_finding 工具落一条 finding，返回 (finding_id, node_id)。
func (e *accEnv) newFinding(task *Task, tid int64, payload string) (int64, int64) {
	e.t.Helper()
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)
	tool := workflowTool(e.t, tsx.WorkerTools(), "report_finding")
	res, err := tool.Call(context.Background(), json.RawMessage(payload), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "finding recorded: ") {
		e.t.Fatalf("report_finding: %v %s", err, res.Flatten())
	}
	var out db.RecordedFinding
	if err := json.Unmarshal([]byte(strings.SplitN(res.Flatten(), "\n", 2)[1]), &out); err != nil {
		e.t.Fatalf("decode finding: %v raw=%s", err, res.Flatten())
	}
	return out.FindingID, out.NodeID
}

func accP1(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p1", "终态与撤回")
	fid, _ := e.newFinding(task, tid, `{"vulnclass":"xss","severity":"medium","summary":"反射型 XSS（待推翻）","evidence":"<script> 原样回显","confidence":"medium"}`)
	rec := e.request(http.MethodPatch, "/api/exploration/findings/"+strconv.FormatInt(fid, 10),
		`{"status":"retracted","retract_reason":"复测推翻：输出被 HTML 编码"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("撤回 status=%d body=%s", rec.Code, rec.Body.String())
	}

	st := task.Store
	sem, err := st.AddIntent(map[string]any{"summary": "语义终态：探针回包否定该假设"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(sem, "work#9", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := st.SetIntentSemanticOutcome(sem, "negative", "探针发出且回包否定"); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CompareAndSetIntentState(sem, "running", "negative"); err != nil || !ok {
		t.Fatalf("semantic settle: ok=%v err=%v", ok, err)
	}
	blocked, err := st.AddIntent(map[string]any{"summary": "未测成：模型故障重试用尽"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CompareAndSetIntentState(blocked, "open", "blocked"); err != nil || !ok {
		t.Fatalf("blocked settle: ok=%v err=%v", ok, err)
	}

	md := e.report(task)
	e.mustContain(md, "## 已撤回（复测推翻；不进正文，保留在案）", "撤回段")
	e.mustContain(md, "复测推翻：输出被 HTML 编码", "撤回原因")
	e.mustContain(md, "## 未测成（blocked/exhausted，不得当作已测干净）", "未测成段")
	e.mustContain(md, "未测成：模型故障重试用尽", "未测成意图")
}

func accP2(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p2", "覆盖矩阵")
	as := e.m.Assets()
	if _, err := as.AddAgentScope(tid, "root_domain", "acc-matrix.example", "验收范围", "agent"); err != nil {
		t.Fatal(err)
	}
	asset, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: "api.acc-matrix.example", TaskID: tid})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = as.DeleteByIDs([]int64{asset}) })

	st := task.Store
	tested, err := st.AddIntent(map[string]any{"summary": "sqli on api（已测干净）", "risk_area": "sqli"}, 5, []int64{asset}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntentState(tested, "negative"); err != nil {
		t.Fatal(err)
	}
	failed, err := st.AddIntent(map[string]any{"summary": "ssrf on api（没测成）", "risk_area": "ssrf"}, 5, []int64{asset}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntentState(failed, "blocked"); err != nil {
		t.Fatal(err)
	}

	md := e.report(task)
	e.mustContain(md, "## 覆盖矩阵（范围内资产 × 风险类）", "覆盖矩阵段")
	e.mustContain(md, "sqli：已测", "已测单元格")
	e.mustContain(md, "ssrf：未测成", "未测成单元格")
	e.mustContain(md, "api.acc-matrix.example", "范围内资产")
}

func accP3(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p3", "复测结论")
	fid, _ := e.newFinding(task, tid, `{"vulnclass":"sqli","severity":"high","summary":"注入（将被复测确认修复）","evidence":"sleep 5 生效","confidence":"high"}`)
	rt, conv, ok, err := e.m.pg.CreateFindingRetest(context.Background(), fid, "验收复测")
	if err != nil || !ok || rt == nil || conv == nil {
		t.Fatalf("create retest: %v ok=%v", err, ok)
	}
	if ok, err := e.m.pg.StartFindingRetest(context.Background(), rt.ID); err != nil || !ok {
		t.Fatalf("start retest: %v ok=%v", err, ok)
	}
	if err := e.m.pg.RecordFindingRetestResult(context.Background(), conv.ID, "fixed", "复测确认修复", "同一 payload 返回 400，控制点已生效"); err != nil {
		t.Fatal(err)
	}
	if err := e.m.pg.FinishFindingRetest(rt.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}

	md := e.report(task)
	e.mustContain(md, "## 复核结论（最新复测非“复现”）", "复核段")
	e.mustContain(md, "已修复（复测确认）", "复测结论标签")
}

func accP4(t *testing.T, e *accEnv) {
	old, had, err := e.m.pg.GetSetting(settingTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.pg.SetSetting(settingTokenBudget, "1000"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = e.m.pg.SetSetting(settingTokenBudget, old)
		} else {
			_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingTokenBudget)
		}
	})
	task, _ := e.newTask("acceptance-p4", "预算护栏")
	use := func(in, out int) {
		if err := e.m.pg.InsertLLMUsage(&db.LLMUsage{TaskID: task.ID, InputTokens: in, OutputTokens: out, Status: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	hints := func() string {
		task.trigMu.Lock()
		defer task.trigMu.Unlock()
		var b strings.Builder
		for _, ev := range task.pendingTriggers {
			b.WriteString(strings.Join(ev.Hints, "\n"))
			b.WriteString("\n")
		}
		return b.String()
	}
	waitFor := func(what string, cond func() bool) {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("等待超时：%s（hints=%q）", what, hints())
	}

	use(500, 200) // 700/1000 → 70%
	e.s.engine.startDeadlineCoordinator(e.ctx, task)
	waitFor("70% 提示投递", func() bool { return strings.Contains(hints(), "预算提示 70%") })
	use(100, 50) // 850/1000 → 85%
	waitFor("85% 提示投递", func() bool { return strings.Contains(hints(), "预算提示 85%") })
	if n := strings.Count(hints(), "预算提示 70%"); n != 1 {
		t.Fatalf("70%% 提示应只投一次，实际 %d 次：%q", n, hints())
	}
	// 档位提示同时要在时间线留痕（system 活动），而不只存在于 planner 上下文里。
	timeline := func() string {
		acts, _, err := task.Store.ActivityList(nil, 0, 200)
		if err != nil {
			t.Fatalf("activity list: %v", err)
		}
		var b strings.Builder
		for _, a := range acts {
			b.WriteString(a.Summary)
			b.WriteString("\n")
		}
		return b.String()
	}
	e.mustContain(timeline(), "Token 预算 70%", "时间线 70% 档位")
	e.mustContain(timeline(), "Token 预算 85%", "时间线 85% 档位")

	use(150, 0) // 1000/1000 → exhausted
	waitFor("进入收尾时序(settling)", func() bool { return e.s.engine.isSettling(task.ID) })
	e.mustContain(timeline(), "Token 预算耗尽", "时间线耗尽记录")
}

func accP5(t *testing.T, e *accEnv) {
	task, _ := e.newTask("acceptance-p5", "租约回收")
	st := task.Store
	intent, err := st.AddIntent(map[string]any{"summary": "将被看门狗回收的僵死意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(intent, "work#1", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := e.m.pg.Exec(`UPDATE exploration_nodes SET heartbeat_at=now()-interval '30 minutes', lease_expires_at=now()-interval '15 minutes' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	e.s.engine.sweepExpiredLeases(task)
	node, err := st.GetNode(intent)
	if err != nil || node == nil || node.State != "open" || node.Owner != "" {
		t.Fatalf("回收后状态不对: %+v err=%v", node, err)
	}
	acts, _, err := st.ActivityList(nil, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if strings.Contains(a.Summary, "意图租约到期回收") {
			found = true
		}
	}
	if !found {
		t.Fatalf("时间线缺少回收记录：%+v", acts)
	}
}

func accP6(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p6", "候选闭合")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)
	closeTool := workflowTool(t, tsx.WorkerTools(), "close_candidate")
	for _, in := range []string{
		`{"candidate":"登录口疑似 SQLi","outcome":"ruled_out","counterevidence":"服务端参数化查询：payload 原样出现在错误日志、未参与执行","confidence":"high","vulnclass":"sqli","severity":"high"}`,
		`{"candidate":"搜索框疑似存储型 XSS","outcome":"open_proof_gap","proof_gap":"缺登录态，无法验证渲染时是否执行","confidence":"low","vulnclass":"xss","severity":"medium"}`,
	} {
		res, err := closeTool.Call(context.Background(), json.RawMessage(in), nil)
		if err != nil || !strings.HasPrefix(res.Flatten(), "candidate closed:") {
			t.Fatalf("close_candidate: %v %s", err, res.Flatten())
		}
	}

	md := e.report(task)
	e.mustContain(md, "## 已排除候选（有反证；不进正文，保留在案）", "已排除段")
	e.mustContain(md, "服务端参数化查询", "反证文本")
	e.mustContain(md, "## 存疑待补证（缺口未闭合，不得视为安全）", "存疑段")
	e.mustContain(md, "缺登录态", "缺口文本")
}

// accP11 走完交付面全链：真实 report_finding 落一条发现 → 绑定一条 proof 原始报文 →
// GET src-report（8 块骨架 + 整包）→ GET src-report.docx（合法 OOXML zip，正文含整包）。
func accP11(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p11", "SRC 交付导出")
	fid, _ := e.newFinding(task, tid, `{"vulnclass":"sqli","severity":"high","summary":"搜索接口注入（交付导出验收）","evidence":"curl http://x/search?q=1 单引号触发报错","confidence":"high"}`)

	// 绑定一条 proof 证据（原始报文头），让导出能生成「涉及接口清单 + 整包步骤」。
	sn := db.TrafficEvidenceSnapshot{
		SourceTrafficID: "acc-p11-1",
		URL:             "https://acc-p11.example/search?q=1'",
		Method:          "GET",
		Status:          500,
		ContentType:     "text/html",
		ReqHead:         "GET /search?q=1' HTTP/1.1\nHost: acc-p11.example\nAccept-Encoding: gzip, deflate\nConnection: close",
		RespHead:        "HTTP/1.1 500 Internal Server Error",
	}
	sn = sn.Normalize()
	sn.ID = db.TrafficSnapshotID(sn)
	if err := e.m.pg.WithEvidenceTx(context.Background(), func(tx *sql.Tx) error {
		return db.AddFindingTrafficTx(tx, fid, []db.PreparedTrafficEvidence{{
			Ref:      db.TrafficRef{Role: "proof", Note: "打穿请求"},
			Snapshot: sn,
		}})
	}); err != nil {
		t.Fatalf("bind proof traffic: %v", err)
	}

	fidStr := strconv.FormatInt(fid, 10)
	rec := e.request(http.MethodGet, "/api/findings/"+fidStr+"/src-report", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("src-report status=%d body=%s", rec.Code, rec.Body.String())
	}
	md := rec.Body.String()
	for _, want := range []string{
		"漏洞标题：",
		"目标网站URL：https://acc-p11.example/search?q=1'",
		"漏洞等级：高危",
		"漏洞描述：\n",
		"漏洞危害：\n",
		"涉及接口清单\n1、GET https://acc-p11.example/search?q=1'（打穿请求（proof）",
		"复现步骤\n1、打穿请求（proof）：打穿请求\n```\nGET /search?q=1' HTTP/1.1\nHost: acc-p11.example",
		"修复建议\n",
	} {
		e.mustContain(md, want, "SRC 简报")
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.TrimSpace(line) == "#" {
			t.Fatalf("SRC 简报出现半角 # 分隔行：\n%s", md)
		}
	}

	rec = e.request(http.MethodGet, "/api/findings/"+fidStr+"/src-report.docx", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("docx status=%d body=%s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("docx 不是合法 zip：%v", err)
	}
	var doc string
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		doc = string(b)
	}
	if doc == "" {
		t.Fatal("docx 缺少 word/document.xml")
	}
	for _, want := range []string{"漏洞等级", "复现步骤", "GET /search?q=1", "Host: acc-p11.example", "修复建议"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("docx 正文缺少 %q", want)
		}
	}
}

// accP12 走真实的 guard → intercept 链路（与生产同一条 hook 入口）：编码混淆不再
// 绕过 deny 规则、RoE 对 base64 目标可见、规则加载失败失效安全、dry-run 不落库。
func accP12(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p12", "拦截遮挡面")
	// 环境变量 ARTEX_SCOPE_ENFORCEMENT 优先于 setting（部署可用 .env 强制开关）；
	// 本用例要验证的是 setting 路径与抽取逻辑，先清掉环境覆盖保证确定性。
	t.Setenv("ARTEX_SCOPE_ENFORCEMENT", "")
	// 与生产同款：把任务上下文挂进 ctx（guard 的审计行按 task_id 归属）。
	ctx := intercept.WithTaskContext(context.Background(), task.ID, "acceptance", nil)

	// 先把 RoE 模式显式置 off：scope 查询有 30s TTL 缓存（生产设计），若前面的
	// 用例把模式留成 deny/ask，①② 会先查一次 scope 并把「空范围」缓存下来，
	// ③ 再翻 deny 也读不到刚加的范围行——本用例必须不依赖缓存冷暖。
	oldMode, had, err := e.m.pg.GetSetting("intercept_scope_mode")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = e.m.pg.SetSetting("intercept_scope_mode", oldMode)
		} else {
			_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, "intercept_scope_mode")
		}
	})
	if err := e.m.pg.SetSetting("intercept_scope_mode", "off"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Assets().AddAgentScope(tid, "root_domain", "acc-p12.example", "验收范围", "agent"); err != nil {
		t.Fatal(err)
	}

	rule, err := e.m.pg.CreateInterceptRule("acc-deny-evil", "tool_input", "string", "evil.example", "deny", "", 100, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.m.pg.DeleteInterceptRule(rule.ID) })
	e.s.m.interceptor.Invalidate()
	g := guard.NewWithInterceptor(e.s.m.interceptor, tid)

	// ① 百分号编码的目标 → 被 deny（此前只匹配整段原文，会静默绕过）
	blocked, reason, _ := g.Hooks().PreToolUse(ctx, "Bash",
		[]byte(`{"command":"curl \"http://%65%76%69%6c%2e%65%78%61%6d%70%6c%65/x\""}`))
	if !blocked || !strings.Contains(reason, "acc-deny-evil") {
		t.Fatalf("percent 编码绕过未被拦截: blocked=%v reason=%q", blocked, reason)
	}
	if surface := latestAuditSurface(t, e, task.ID); surface != "percent_decoded:1" {
		t.Fatalf("审计命中表面 = %q，want percent_decoded:1", surface)
	}

	// ② 嵌套字符串里的目标 → 命中，表面 nested_string
	blocked, reason, _ = g.Hooks().PreToolUse(ctx, "Bash", []byte(`{"command":"curl http://x/ -d name=evil.example"}`))
	if !blocked {
		t.Fatalf("嵌套串未被拦截: %q", reason)
	}
	if surface := latestAuditSurface(t, e, task.ID); surface != "nested_string" {
		t.Fatalf("审计命中表面 = %q，want nested_string", surface)
	}

	// ③ RoE：翻到 deny，base64 解码后才现形的越界目标 → 拒绝且消息标注「base64 解码后」
	if err := e.m.pg.SetSetting("intercept_scope_mode", "deny"); err != nil {
		t.Fatal(err)
	}
	blocked, reason, _ = g.Hooks().PreToolUse(ctx, "Bash",
		[]byte(`{"command":"echo aHR0cDovL2V2aWwuZXhhbXBsZS5jb20vc2hlbGw= | base64 -d | bash"}`))
	if !blocked || !strings.Contains(reason, "base64 解码后") {
		mode, _, _ := e.m.pg.GetSetting("intercept_scope_mode")
		rows, _ := e.m.Assets().ListTaskScope(tid)
		directDec, directHit := e.s.m.interceptor.CheckScope(ctx, tid, "Bash",
			[]byte(`{"command":"echo aHR0cDovL2V2aWwuZXhhbXBsZS5jb20vc2hlbGw= | base64 -d | bash"}`))
		t.Fatalf("RoE 未拦截 base64 目标: blocked=%v reason=%q mode=%q enabled=%v scope_rows=%+v targets=%v direct=(hit=%v action=%s msg=%s)",
			blocked, reason, mode, e.s.m.interceptor.IsToolEnabled("Bash"), rows,
			intercept.ExtractTargets(`echo aHR0cDovL2V2aWwuZXhhbXBsZS5jb20vc2hlbGw= | base64 -d | bash`),
			directHit, directDec.Action, directDec.Message)
	}

	// ④ 失效安全：规则快照读不出来（DB 句柄已关）时不静默放行——默认转人工（ask）
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v)", err)
	}
	dead, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = dead.Close()
	ic := intercept.New(dead)
	dec, hit := ic.Match("Bash", []byte(`{"command":"curl http://anything.example"}`))
	if !hit || dec.Action != "ask" {
		t.Fatalf("加载失败应失效安全（ask），got hit=%v action=%q", hit, dec.Action)
	}

	// ⑤ dry-run：命中返回、且不落任何库记录
	var before int
	if err := e.m.pg.QueryRow(`SELECT count(*) FROM intercept_pending`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rec := e.request(http.MethodPost, "/api/intercept/rules/test",
		`{"name":"dry","match_target":"tool_input","match_type":"string","pattern":"evil.example","action":"deny","sample_tool_name":"Bash","sample_input":"{\"command\":\"curl http://%65%76%69%6c%2e%65%78%61%6d%70%6c%65\"}"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"matched":true`) || !strings.Contains(body, "percent_decoded:1") {
		t.Fatalf("dry-run 未命中编码目标: %s", body)
	}
	var after int
	if err := e.m.pg.QueryRow(`SELECT count(*) FROM intercept_pending`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("dry-run 不应落库: before=%d after=%d", before, after)
	}
}

// latestAuditSurface 读该任务最近一条 denied 审计里的命中表面（P12 留痕）。
func latestAuditSurface(t *testing.T, e *accEnv, taskID string) string {
	t.Helper()
	var audit string
	if err := e.m.pg.QueryRow(`SELECT COALESCE(audit::text,'') FROM intercept_pending WHERE task_id=$1 AND status='denied' ORDER BY id DESC LIMIT 1`, taskID).Scan(&audit); err != nil {
		t.Fatalf("读拦截审计: %v", err)
	}
	var parsed struct {
		MatchedSurface string `json:"matched_surface"`
	}
	if err := json.Unmarshal([]byte(audit), &parsed); err != nil {
		t.Fatalf("审计不是 JSON: %v (%s)", err, audit)
	}
	return parsed.MatchedSurface
}

func accP7(t *testing.T, e *accEnv) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "always-rules")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: always-rules\ndescription: 验收常驻技能\n---\n\n# 铁律\n永远先读证据、再下结论。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldAugment := agent.ToolAugment
	wireAgentAugment(e.m.pg, dir, nil)
	t.Cleanup(func() { agent.ToolAugment = oldAugment })

	a, err := e.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		t.Fatalf("worker agent: %v", err)
	}
	if err := e.m.pg.SetAgentSkillBindings(a.ID, []db.SkillBinding{{Name: "always-rules", Always: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.m.pg.SetAgentSkillBindings(a.ID, nil) })

	_, def, cleanup := agent.ToolAugment(context.Background(), "worker")
	if cleanup != nil {
		defer cleanup()
	}
	if !strings.Contains(def.AlwaysSkills, "【常驻技能 · 始终遵守】") || !strings.Contains(def.AlwaysSkills, "永远先读证据") {
		t.Fatalf("常驻技能未注入 system 稳定段：%q", def.AlwaysSkills)
	}
}
