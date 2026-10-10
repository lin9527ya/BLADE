package server

import (
	"context"
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
)

// TestAcceptance 是 P1–P7 机制的端到端验收清单：驱动真实的 HTTP 处理器、真实的工具
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

	t.Run("P1 语义终态与发现撤回入报告", func(t *testing.T) { accP1(t, env) })
	t.Run("P2 覆盖矩阵（资产×风险类）入报告", func(t *testing.T) { accP2(t, env) })
	t.Run("P3 复测结论入报告复核段", func(t *testing.T) { accP3(t, env) })
	t.Run("P4 预算 70/85% 提示与 100% 收尾时序", func(t *testing.T) { accP4(t, env) })
	t.Run("P5 租约看门狗回收僵死意图", func(t *testing.T) { accP5(t, env) })
	t.Run("P6 候选三态闭合入报告", func(t *testing.T) { accP6(t, env) })
	t.Run("P7 常驻技能注入 system 稳定段", func(t *testing.T) { accP7(t, env) })
}

type accEnv struct {
	t     *testing.T
	m     *Manager
	s     *Server
	token string
	ctx   context.Context
}

// newTask 建一个验收任务，并登记收尾：停引擎运行时 → 清本任务 findings/用量行 → 删任务。
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
