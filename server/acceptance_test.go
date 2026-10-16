package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/agentcore"
	"github.com/lin9527ya/Norma/harness"
	"github.com/lin9527ya/Norma/llm"
	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/artex/agent"
	"github.com/lin9527ya/artex/db"
	"github.com/lin9527ya/artex/guard"
	"github.com/lin9527ya/artex/intel"
	"github.com/lin9527ya/artex/intercept"
	"github.com/lin9527ya/artex/mcphttp"
)

// TestAcceptance 是 P1–P7 机制 + P11 交付导出 + P12 拦截可信化 + P13 执行台账与收尾门 + P14 领域化压缩
// + P15 结构化裁决与改参放行 + P18 约束可执行化 + P19 批量交付与同源归并 + P20 探索策略门
// + P21 情报测绘 + P22 计划任务 + P23 公司知识 + P24 执行限额与熔断 + P25 MCP 工具级开关
// + P26 CSV 资产导入 + P27 战果层 + P28 资产检索导入与存储清理 + P29 战果动用 + P30 攻击路径
// + P31 检测验证 + P32 授权范围设置 + P33 战役层 + P34 AD 攻击路径导入
// + P35 停机开关与作业时间窗
// + P36 凭据可利用面
// + P37 作业时间线
// + P38 事前可检出性
// + P39 云上枚举结果导入
// + P26 CSV 资产导入 + P27 战果层（据点/凭据）+ P29 战果动用 + P30 攻击路径 + P31 检测验证 + P32 授权范围设置 + P33 战役层的端到端
// 验收清单：驱动真实的 HTTP 处理器、真实的工具与真实的引擎路径（只有 LLM 与外部目标被夹具替代），
// 每个机制一个子测试。
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
	token, err := signJWT(s.signKey.get())
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
	t.Run("P20 探索策略门（意图门/命令门/额度/继承快照/停摆收尾）", func(t *testing.T) { accP20(t, env.forTest(t)) })
	t.Run("P21 情报/测绘工具面（FOFA：查询/落盘/台账/配额/边界）", func(t *testing.T) { accP21(t, env.forTest(t)) })
	t.Run("P22 计划任务（结构化频率/CAS 防重复/失败留痕/立即运行）", func(t *testing.T) { accP22(t, env.forTest(t)) })
	t.Run("P23 公司级知识层（跨任务只读先验/来源可回溯/废弃恢复/唯一写口）", func(t *testing.T) { accP23(t, env.forTest(t)) })
	t.Run("P24 执行限额与 MCP 熔断（限额真生效/信号归类/开闸不打服务端/重置恢复）", func(t *testing.T) { accP24(t, env.forTest(t)) })
	t.Run("P25 MCP 工具级开关（关掉即不注入/刷新不翻回/口径自洽/未知名字 404）", func(t *testing.T) { accP25(t, env.forTest(t)) })
	t.Run("P26 CSV 资产导入（预览只读/预览与落库口径一致/重复只合并不新增/来源如实）", func(t *testing.T) { accP26(t, env.forTest(t)) })
	t.Run("P27 战果层（据点/凭据入概览与报告，凭据本体不外流）", func(t *testing.T) { accP27(t, env.forTest(t)) })
	t.Run("P28 资产 FOFA 检索导入与存储清理（同配额台账/二次导入只合并/试算不动盘）", func(t *testing.T) { accP28(t, env.forTest(t)) })
	t.Run("P29 战果动用（意图声明 uses → outstanding 归零 → 删意图不带走访据）", func(t *testing.T) { accP29(t, env.forTest(t)) })
	t.Run("P30 攻击路径（由 uses/yields 边重建，进报告）", func(t *testing.T) { accP30(t, env.forTest(t)) })
	t.Run("P31 检测验证（三态分记，不进「检出率」；报告成段）", func(t *testing.T) { accP31(t, env.forTest(t)) })
	t.Run("P32 授权范围设置（模式+时间窗经 API 存取，写错当场退回且不改动已存值）", func(t *testing.T) { accP32(t, env.forTest(t)) })
	t.Run("P33 战役层（归属与聚合；删战役只解绑不删任务）", func(t *testing.T) { accP33(t, env.forTest(t)) })
	t.Run("P34 AD 攻击路径导入（预览只读/路径可读/重导只合并/坏输入退回）", func(t *testing.T) { accP34(t, env.forTest(t)) })
	t.Run("P35 停机开关与作业时间窗（到点全停/三层拦得住/恢复靠手动/回执可查）", func(t *testing.T) { accP35(t, env.forTest(t)) })
	t.Run("P36 凭据可利用面（能登哪/验证要有证据/明文受控且留痕）", func(t *testing.T) { accP36(t, env.forTest(t)) })
	t.Run("P37 作业时间线（台账/审批/发现/战果/检测串一条线；导出与页面同源）", func(t *testing.T) { accP37(t, env.forTest(t)) })
	t.Run("P38 事前可检出性（命中给笔记/没收录明说/落在审批卡上/非动作工具不标）", func(t *testing.T) { accP38(t, env.forTest(t)) })
	t.Run("P39 云上枚举导入（AWS IAM + K8s RBAC 进同一张图，路径一起读）", func(t *testing.T) { accP39(t, env.forTest(t)) })
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
	if !waitActivity(t, st, "execution_pending", 10*time.Second) {
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
	if !waitActivity(t, st, "execution_done", 10*time.Second) {
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

// waitActivity 轮询等一条时间线活动出现（观察持久化副作用一律走等待，不立刻断言——
// 生产侧的"改状态"与"落痕"是两次写，中间存在毫秒级窗口；这是 accP4 起反复出现的
// flake 形态，见 CHANGELOG 的 e6330f4 与 accP13 的 CI 复现）。
func waitActivity(t *testing.T, st *db.ExplorationStore, kind string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if activitySeen(t, st, kind) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
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

// accP20 走完「探索策略」的三层执行点：意图内容被硬拒且理由可操作（同时留痕、但不落图）、
// 策略内的正常方向不受影响；同一份策略编译成任务级命令门（本任务拦、别任务不拦、读源码不
// 误伤）；额度到位即停工；全局默认可继承可覆盖而任务持有快照（改全局不回溯）；策略内确实
// 无路可走时以 policy_blocked 体面收尾（而不是继续空转烧 token）。
func accP20(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p20", "探索策略门：手段面/节奏面/敏感写")
	other, otherTid := e.newTask("acceptance-p20-other", "另一个任务（策略不该外溢）")
	// 规则层要确定性判定：先把 RoE 关掉（否则"被拦"的理由可能是授权范围而非策略），
	// 并在收尾时把全局默认策略恢复原状（后面还有别的子测试）。
	t.Setenv("ARTEX_SCOPE_ENFORCEMENT", "")
	oldMode, hadMode, _ := e.m.pg.GetSetting("intercept_scope_mode")
	oldPol, hadPol, _ := e.m.pg.GetSetting(settingExplorationPolicy)
	t.Cleanup(func() {
		if hadMode {
			_ = e.m.pg.SetSetting("intercept_scope_mode", oldMode)
		} else {
			_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, "intercept_scope_mode")
		}
		if hadPol {
			_ = e.m.pg.SetSetting(settingExplorationPolicy, oldPol)
		} else {
			_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, settingExplorationPolicy)
		}
		_, _ = e.m.pg.Exec(`DELETE FROM intercept_rules WHERE COALESCE(task_id,'') <> ''`)
		e.s.m.interceptor.Invalidate()
	})
	if err := e.m.pg.SetSetting("intercept_scope_mode", "off"); err != nil {
		t.Fatal(err)
	}
	ctx := intercept.WithTaskContext(context.Background(), task.ID, "acceptance", nil)
	otherCtx := intercept.WithTaskContext(context.Background(), other.ID, "acceptance", nil)
	bash := func(cmd string) []byte { b, _ := json.Marshal(map[string]string{"command": cmd}); return b }

	// ① 保存策略：禁口令爆破（手段面）+ 禁 PUT/PATCH 与脱库（敏感写）。五轴全给全值，
	// 免得断言被"继承来的全局默认"污染。保存即重建命令门 → 规则数 = 1 手段 + 2 写。
	rec := e.request(http.MethodPatch, "/api/tasks/"+task.ID+"/policy",
		`{"override":{"techniques":["bruteforce"],"forbidden_writes":["http_write_method","db_dump"],`+
			`"max_intents_per_round":0,"max_open_intents":0,"max_intents_total":0}}`)
	if rec.Code != 200 {
		t.Fatalf("保存策略 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var pol policyDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &pol); err != nil {
		t.Fatal(err)
	}
	if len(pol.Effective.Techniques) != 1 || pol.Effective.Techniques[0] != "bruteforce" ||
		len(pol.Effective.ForbiddenWrites) != 2 {
		t.Fatalf("生效策略不是覆盖后的结果: %+v", pol.Effective)
	}
	if pol.RuleCount != 3 {
		t.Fatalf("命令门规则数应为 1+2=3，实际 %d", pol.RuleCount)
	}

	// ② 意图门：命中禁用手段 → 硬拒。理由要能让人（和模型）看懂命中什么、往哪改；
	// 被拒的意图不落图（不污染探索与去重），但时间线必须留痕。
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)
	// 生产里 ToolSet 每 run 新建并现读策略（改策略下一轮生效）；验收里对应"每次改完重新读"。
	reload := func() { tsx.SetPolicy(e.s.taskPolicyByID(tid)) }
	reload()
	intentTool := workflowTool(t, tsx.PlannerTools(), "add_intent")
	before, err := task.Store.CountIntents()
	if err != nil {
		t.Fatal(err)
	}
	res, err := intentTool.Call(context.Background(),
		json.RawMessage(`{"summary":"用 hydra 对登录口做爆破，找出弱口令账号"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	flat := res.Flatten()
	for _, want := range []string{"探索策略·硬拒绝", "口令爆破", "bruteforce", "未写入探索图", "合规改写建议"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("拒绝理由缺少 %q：%s", want, flat)
		}
	}
	if after, _ := task.Store.CountIntents(); after != before {
		t.Fatalf("被拒的意图不该落图：%d → %d", before, after)
	}
	rejects, err := task.Store.RecentPolicyRejects(10)
	if err != nil || len(rejects) == 0 {
		t.Fatalf("被拒意图必须留痕: %v %d", err, len(rejects))
	}
	if !strings.Contains(rejects[0].Detail, "hydra 对登录口做爆破") || !strings.Contains(rejects[0].Summary, "口令爆破") {
		t.Fatalf("留痕没有写清想做什么/被什么拦: %s | %s", rejects[0].Summary, rejects[0].Detail)
	}

	// ③ 定向而非全拒：策略内的正常方向照常进图（否则这道门会把探索一起打死）。
	res, err = intentTool.Call(context.Background(),
		json.RawMessage(`{"summary":"枚举 /api/v1/users 的对象级越权，比较两个账号的返回差异"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "intent created: ") {
		t.Fatalf("策略内的正常方向被误拒：%s", res.Flatten())
	}
	if after, _ := task.Store.CountIntents(); after != before+1 {
		t.Fatalf("正常方向应进图：%d → %d", before, after)
	}

	// ④ 命令门 + 作用域：同一份策略编译出的任务级规则在本任务拦、在别的任务不生效，
	// 且只认"真的在执行写"的形状（读源码/看关键字不误伤）。
	g := guard.NewWithInterceptor(e.s.m.interceptor, tid)
	og := guard.NewWithInterceptor(e.s.m.interceptor, otherTid)
	for _, tc := range []struct{ name, cmd, want string }{
		{"PUT 写方法", "curl -X PUT http://api.example.com/api/user/1 -d x=1", "HTTP 写方法"},
		{"脱库导出", "sqlmap -u http://api.example.com/item?id=1 --dump", "脱库"},
	} {
		blocked, reason, _ := g.Hooks().PreToolUse(ctx, "Bash", bash(tc.cmd))
		if !blocked || !strings.Contains(reason, "任务探索策略") || !strings.Contains(reason, tc.want) {
			t.Fatalf("%s 没被策略命令门拦住: blocked=%v reason=%q", tc.name, blocked, reason)
		}
		if b, _, _ := og.Hooks().PreToolUse(otherCtx, "Bash", bash(tc.cmd)); b {
			t.Fatalf("%s 的策略规则外溢到了别的任务", tc.name)
		}
	}
	for _, cmd := range []string{
		`grep -n "DELETE FROM" ./readme.md`, // 读源码里的 SQL 关键字 ≠ 执行写
		`echo "update available"`,
		`curl -X POST http://api.example.com/login -d u=1`, // 裸 POST 不在敏感写清单（只读接口大量用 POST）
	} {
		if blocked, reason, _ := g.Hooks().PreToolUse(ctx, "Bash", bash(cmd)); blocked {
			t.Fatalf("策略误伤了只读操作: %s → %s", cmd, reason)
		}
	}

	// ⑤ 节奏面（额度）：总数上限 2 时，第 2 条放行、第 3 条被拒且理由指向"别再派了"。
	rec = e.request(http.MethodPatch, "/api/tasks/"+task.ID+"/policy",
		`{"override":{"techniques":["bruteforce"],"forbidden_writes":["http_write_method","db_dump"],`+
			`"max_intents_per_round":0,"max_open_intents":0,"max_intents_total":2}}`)
	if rec.Code != 200 {
		t.Fatalf("改额度 status=%d body=%s", rec.Code, rec.Body.String())
	}
	reload()
	res, err = intentTool.Call(context.Background(), json.RawMessage(`{"summary":"检查订单接口的越权读取"}`), nil)
	if err != nil || !strings.Contains(res.Flatten(), "intent created: ") {
		t.Fatalf("额度内的意图应放行: %v %s", err, res.Flatten())
	}
	res, err = intentTool.Call(context.Background(), json.RawMessage(`{"summary":"再看一眼导出接口的越权"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "意图总预算") || !strings.Contains(res.Flatten(), "上限 2") {
		t.Fatalf("额度用尽应拒绝并说明上限: %s", res.Flatten())
	}

	// ⑤b 清空覆盖（DELETE 无 body）：回到"无策略"——命令门规则一并撤掉，策略卡回到继承态。
	rec = e.request(http.MethodDelete, "/api/tasks/"+task.ID+"/policy", "")
	if rec.Code != 200 {
		t.Fatalf("清空策略 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pol); err != nil {
		t.Fatal(err)
	}
	if pol.Custom || pol.RuleCount != 0 {
		t.Fatalf("清空后不该还有自定义策略/命令门规则: custom=%v rules=%d", pol.Custom, pol.RuleCount)
	}
	if blocked, _, _ := g.Hooks().PreToolUse(ctx, "Bash", bash("curl -X PUT http://api.example.com/api/user/1 -d x=1")); blocked {
		t.Fatal("清空策略后命令门规则仍在拦（删了策略却留着规则）")
	}

	// ⑤c 自定义条目（词表覆盖不到时操作者手写）：关键词两条层都判、command 层只判命令、
	// 正则按原文判、非法正则写不进去（400 说清楚）。
	rec = e.request(http.MethodPatch, "/api/tasks/"+task.ID+"/policy",
		`{"override":{"techniques":[],"forbidden_writes":[],"custom":[`+
			`{"label":"别碰后台","pattern":"wp-admin"},`+
			`{"pattern":"/internal/(?:admin|debug)","regex":true},`+
			`{"label":"生产库","pattern":"prod-db.internal","layer":"command"}]}}`)
	if rec.Code != 200 {
		t.Fatalf("保存自定义条目 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pol); err != nil {
		t.Fatal(err)
	}
	if len(pol.Effective.Custom) != 3 || pol.Override.Custom == nil || len(*pol.Override.Custom) != 3 {
		t.Fatalf("自定义条目没落库（effective/override 各应 3 条）: %+v", pol.Effective.Custom)
	}
	reload()
	res, err = intentTool.Call(context.Background(),
		json.RawMessage(`{"summary":"扫一下 /wp-admin/ 下面的插件漏洞"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if flat := res.Flatten(); !strings.Contains(flat, "自定义·别碰后台") || !strings.Contains(flat, "wp-admin") {
		t.Fatalf("自定义关键词没拦住意图或理由不完整: %s", flat)
	}
	res, err = intentTool.Call(context.Background(),
		json.RawMessage(`{"summary":"看一眼 /internal/debug 的角色配置"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if flat := res.Flatten(); !strings.Contains(flat, "自定义·/internal/(?:admin|debug)") {
		t.Fatalf("自定义正则没拦住意图: %s", flat)
	}
	// command 层：意图措辞不判（"说说没关系"），但命令门照样拦。
	res, err = intentTool.Call(context.Background(),
		json.RawMessage(`{"summary":"连 prod-db.internal 看下版本，只读"}`), nil)
	if err != nil || !strings.Contains(res.Flatten(), "intent created: ") {
		t.Fatalf("command 层条目不该判意图措辞: %v %s", err, res.Flatten())
	}
	blocked, reason, _ := g.Hooks().PreToolUse(ctx, "Bash", bash("curl -s http://t/wp-admin/admin.php"))
	if !blocked || !strings.Contains(reason, "别碰后台") {
		t.Fatalf("自定义关键词没进命令门: blocked=%v reason=%q", blocked, reason)
	}
	blocked, reason, _ = g.Hooks().PreToolUse(ctx, "Bash", bash("nmap -sV prod-db.internal"))
	if !blocked || !strings.Contains(reason, "生产库") {
		t.Fatalf("command 层自定义条目没进命令门: blocked=%v reason=%q", blocked, reason)
	}
	if b, _, _ := og.Hooks().PreToolUse(otherCtx, "Bash", bash("curl -s http://t/wp-admin/admin.php")); b {
		t.Fatal("自定义条目的规则外溢到了别的任务")
	}
	// 非法正则：写入即 400，并说明是哪个条目、错在哪。
	rec = e.request(http.MethodPatch, "/api/tasks/"+task.ID+"/policy",
		`{"override":{"custom":[{"label":"坏正则","pattern":"(unclosed","regex":true}]}}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "不是合法正则") {
		t.Fatalf("非法正则必须 400 并说明原因: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// ⑥ 全局默认继承 / 覆盖 / 快照：改全局默认不回溯已建任务（各自持有 effective 快照）。
	rec = e.request(http.MethodPut, "/api/settings/exploration-policy",
		`{"override":{"techniques":["dos"],"custom":[{"label":"全局禁碰","pattern":"pay-gateway"}]}}`)
	if rec.Code != 200 {
		t.Fatalf("保存全局默认 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var pd struct {
		CustomLimit int `json:"custom_limit"`
	}
	rec = e.request(http.MethodGet, "/api/settings/exploration-policy", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil || pd.CustomLimit <= 0 {
		t.Fatalf("全局默认应答缺少自定义条目上限: %v %s", err, rec.Body.String())
	}
	inherit := e.newTaskHTTP(t, `{"description":"acceptance-p20-inherit","goal":"继承全局默认"}`)
	rec = e.request(http.MethodGet, "/api/tasks/"+inherit+"/policy", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &pol); err != nil {
		t.Fatal(err)
	}
	if pol.Custom || len(pol.Effective.Techniques) != 1 || pol.Effective.Techniques[0] != "dos" {
		t.Fatalf("新任务应继承当时的全局默认: custom=%v eff=%+v", pol.Custom, pol.Effective)
	}
	if len(pol.Effective.Custom) != 1 || pol.Effective.Custom[0].Pattern != "pay-gateway" {
		t.Fatalf("全局默认里的自定义条目没被继承: %+v", pol.Effective.Custom)
	}
	// 显式覆盖为空 = 本任务不禁（"继承"与"显式清空"必须可区分，否则用户关不掉全局默认）。
	override := e.newTaskHTTP(t, `{"description":"acceptance-p20-override","goal":"显式不禁任何手段","exploration_policy":{"techniques":[]}}`)
	rec = e.request(http.MethodGet, "/api/tasks/"+override+"/policy", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &pol); err != nil {
		t.Fatal(err)
	}
	if !pol.Custom || len(pol.Effective.Techniques) != 0 {
		t.Fatalf("显式清空没有生效: custom=%v eff=%+v", pol.Custom, pol.Effective)
	}
	// 改全局默认 → 已建任务不动（快照语义：运行中的任务不会被"另一处改了设置"改变边界）。
	rec = e.request(http.MethodPut, "/api/settings/exploration-policy", `{"override":{"techniques":["dos","webshell"]}}`)
	if rec.Code != 200 {
		t.Fatalf("二次保存全局默认 status=%d", rec.Code)
	}
	rec = e.request(http.MethodGet, "/api/tasks/"+inherit+"/policy", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &pol); err != nil {
		t.Fatal(err)
	}
	if len(pol.Effective.Techniques) != 1 || pol.Effective.Techniques[0] != "dos" {
		t.Fatalf("改全局默认回溯了已建任务（快照语义被破坏）: %+v", pol.Effective.Techniques)
	}

	// ⑦ 停摆收尾：策略内确实无路可走时（窗口内被拒达阈值 + 期间没有任何新意图 + 真的没活干），
	// 不再无限唤醒 planner，而是以 policy_blocked 收尾。窗口/阈值临时调小，驱动的仍是生产
	// 那条判定与收尾路径。
	savedW, savedN, savedG := policyStallWindow, policyStallMinRejects, policyStallGrace
	t.Cleanup(func() { policyStallWindow, policyStallMinRejects, policyStallGrace = savedW, savedN, savedG })
	policyStallWindow, policyStallMinRejects, policyStallGrace = 2*time.Second, 3, 0
	// ⑤b 把策略清空了，这里重新装一条（停摆判定要求"有策略"——没策略的任务永远不因此收尾）。
	rec = e.request(http.MethodPatch, "/api/tasks/"+task.ID+"/policy",
		`{"override":{"techniques":["bruteforce"],"forbidden_writes":[],"max_intents_per_round":0,`+
			`"max_open_intents":0,"max_intents_total":0}}`)
	if rec.Code != 200 {
		t.Fatalf("重装策略 status=%d body=%s", rec.Code, rec.Body.String())
	}
	reload()
	// 在跑/待领的意图都清掉（③⑤ 建的那两条）：否则"还有活干"，停摆判定本就不该成立。
	open, err := task.Store.ListByKind(db.KindIntent, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range open {
		if n.State == "open" {
			if _, err := task.Store.CompareAndSetIntentState(n.ID, "open", "done"); err != nil {
				t.Fatal(err)
			}
		}
	}
	time.Sleep(2200 * time.Millisecond) // 让"最后一次成功创建意图"落到窗口之外
	// （⑤c 建的那条 command 层意图已在上面被置 done，不会挡住停摆判定。）
	for i := 0; i < policyStallMinRejects; i++ {
		if _, err := intentTool.Call(context.Background(),
			json.RawMessage(`{"summary":"继续用 hydra 爆破这个登录口"}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if !e.s.engine.policyStall(task) {
		t.Fatal("窗口内连续被拒且无新意图，应判定为策略停摆")
	}
	// 用已取消的 ctx 驱动正式收尾路径：终局 planner 轮直接返回（验收环境没有 LLM），
	// 终态落 policy_blocked（不是 timeout——"策略封死"与"超时"必须能分清）。
	cctx, cancel := context.WithCancel(e.ctx)
	cancel()
	e.s.engine.settlePolicyStall(cctx, task)
	if got := e.m.TaskStatus(task.ID); got != "policy_blocked" {
		t.Fatalf("策略停摆的终态应为 policy_blocked，实际 %q", got)
	}
	acts, _, err := task.Store.ActivityList(nil, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if strings.Contains(a.Summary, "探索策略内已无可行方向") {
			found = true
		}
	}
	if !found {
		t.Fatal("收尾没有在时间线上说明原因（策略停摆必须留下可解释的痕迹）")
	}
}

// newTaskHTTP 走真实的 HTTP 建任务路径（创建时的策略快照解析只在服务端做，必须从这里进），
// 返回任务 id；收尾时停引擎运行时并删任务。
func (e *accEnv) newTaskHTTP(t *testing.T, body string) string {
	t.Helper()
	rec := e.request(http.MethodPost, "/api/tasks", body)
	if rec.Code != 201 {
		t.Fatalf("建任务 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created TaskDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	e.s.engine.StopTask(created.ID) // 验收环境没有 LLM：停掉后台运行，断言只看策略状态
	t.Cleanup(func() {
		e.s.engine.StopTask(created.ID)
		if id, err := strconv.ParseInt(created.ID, 10, 64); err == nil {
			_, _ = e.m.pg.Exec(`DELETE FROM intercept_rules WHERE task_id=$1`, created.ID)
			_ = e.m.pg.DeleteTask(id)
		}
	})
	return created.ID
}

// accP21 走完情报/测绘工具面：假 FOFA（httptest + 设置里的 base_url，与企业版/自建端点
// 走同一个开关）→ 未配 key 时给可读指引且**不外呼** → 配 key 后查询：表达式原样送达、
// 回执给紧凑清单 + 完整结果落盘路径（大结果不灌上下文）→ 时间线留 intel_query 痕迹 →
// 台账与今日用量可见 → 配额到顶即拒并说明用量 → 设置页只回显"是否已配 key"不回显明文 →
// 「测试连接」打 info 回显剩余额度 → 查到的东西要变成意图目标时**照样被资产拦截挡住**。
func accP21(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p21", "情报/测绘工具面（先接 FOFA）")

	// 假 FOFA：两条路径分别对应查询与账号信息。
	var calls int32
	var lastQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Path {
		case "/api/v1/search/all":
			lastQuery = r.URL.Query()
			_, _ = w.Write([]byte(`{"error":false,"size":123,"page":1,"results":[
				["https://a.example.com:8443","1.2.3.4","8443","https","example.com","后台登录","nginx"],
				["http://b.example.com","5.6.7.8","80","http","example.com","",""],
				["http://c.example.com","9.9.9.9","8080","http","example.com","Jenkins","Jetty"]
			]}`))
		case "/api/v1/info/my":
			_, _ = w.Write([]byte(`{"error":false,"email":"acc@example.com","remain_free_point":999}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	// 设置是全局的：改之前记下来，跑完恢复（后面的子测试还要用这套设置）。
	saveSetting := func(key string) (string, bool) {
		v, ok, _ := e.m.pg.GetSetting(key)
		return v, ok
	}
	restore := map[string]struct {
		v  string
		ok bool
	}{}
	for _, k := range []string{settingIntelKey, settingIntelBaseURL, settingIntelDailyQuota} {
		v, ok := saveSetting(k)
		restore[k] = struct {
			v  string
			ok bool
		}{v, ok}
	}
	t.Cleanup(func() {
		for k, r := range restore {
			if r.ok {
				_ = e.m.pg.SetSetting(k, r.v)
			} else {
				_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, k)
			}
		}
	})

	intelTool := workflowTool(t, e.s.intelTools(), "intel_search")
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: tid})
	call := func(expr string) string {
		t.Helper()
		res, err := intelTool.Call(ctx, json.RawMessage(`{"expr":`+strconv.Quote(expr)+`}`), nil)
		if err != nil {
			t.Fatalf("intel_search: %v", err)
		}
		return res.Flatten()
	}

	// ① 未配 key：可读指引，且一次外呼都没有。
	rec := e.request(http.MethodPut, "/api/settings",
		`{"fofa_key":"","fofa_base_url":`+strconv.Quote(srv.URL)+`}`)
	if rec.Code != 200 {
		t.Fatalf("保存情报源设置 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if flat := call(`domain="example.com"`); !strings.Contains(flat, "还没配 API Key") {
		t.Fatalf("未配 key 应给可读指引: %s", flat)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("未配 key 时不该发起外呼，实际 %d 次", n)
	}

	// ② 配 key（假 key）→ 查询：表达式原样送达、回执含清单与落盘路径。
	rec = e.request(http.MethodPut, "/api/settings",
		`{"fofa_key":"acc-key","fofa_base_url":`+strconv.Quote(srv.URL)+`,"intel_daily_quota":50}`)
	if rec.Code != 200 {
		t.Fatalf("保存 key status=%d body=%s", rec.Code, rec.Body.String())
	}
	const expr = `domain="example.com" && protocol="http"`
	flat := call(expr)
	if !strings.Contains(flat, "命中 123 条") || !strings.Contains(flat, "a.example.com") ||
		!strings.Contains(flat, "1.2.3.4:8443") {
		t.Fatalf("回执应含总数与紧凑清单: %s", flat)
	}
	if lastQuery.Get("key") != "acc-key" {
		t.Fatalf("key 没送达假服务: %v", lastQuery)
	}
	if raw, err := base64.StdEncoding.DecodeString(lastQuery.Get("qbase64")); err != nil || string(raw) != expr {
		t.Fatalf("表达式没有原样编码送达: %q err=%v", raw, err)
	}
	if !strings.Contains(lastQuery.Get("fields"), "host") || !strings.Contains(lastQuery.Get("fields"), "title") {
		t.Fatalf("默认字段集不对: %q", lastQuery.Get("fields"))
	}
	spool := ""
	for _, line := range strings.Split(flat, "\n") {
		if i := strings.Index(line, "完整结果（JSON，含全部字段）："); i >= 0 {
			spool = strings.TrimSpace(line[i+len("完整结果（JSON，含全部字段）："):])
		}
	}
	if spool == "" || !filepath.IsAbs(spool) {
		t.Fatalf("回执必须给绝对路径的完整结果文件: %s", flat)
	}
	raw, err := os.ReadFile(spool)
	if err != nil {
		t.Fatalf("完整结果文件读不到: %v", err)
	}
	var spooled intel.Result
	if err := json.Unmarshal(raw, &spooled); err != nil {
		t.Fatalf("完整结果不是合法 JSON: %v", err)
	}
	if len(spooled.Assets) != 3 || spooled.Total != 123 || spooled.Expr != expr {
		t.Fatalf("落盘结果不完整: %+v", spooled)
	}
	if !strings.Contains(spool, filepath.Join("tasks", task.ID)) {
		t.Fatalf("任务级产物应落在任务目录下: %s", spool)
	}
	if !strings.Contains(flat, "insert_assets") || !strings.Contains(flat, "add_task_scope") {
		t.Fatalf("回执必须说清「只查不入库/不扩范围」的下一步: %s", flat)
	}

	// ③ 时间线留痕（持久化副作用走等待）。
	if !waitActivity(t, task.Store, "intel_query", 5*time.Second) {
		t.Fatal("情报查询必须留 intel_query 痕迹")
	}
	acts, _, err := task.Store.ActivityList(nil, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	// 列表行只带 summary（detail 一律懒加载，与时间线其它活动同款）；细节走 ActivityDetail。
	var auditID int64
	for _, a := range acts {
		if a.Kind == "intel_query" && strings.Contains(a.Summary, "命中 123 条") {
			auditID = a.ID
		}
	}
	if auditID == 0 {
		t.Fatal("intel_query 痕迹没写清命中多少")
	}
	detail, err := task.Store.ActivityDetail(auditID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, expr) || !strings.Contains(detail, spool) {
		t.Fatalf("intel_query 细节应含表达式与落盘路径: %s", detail)
	}

	// ④ 台账与今日用量（设置接口 + 台账接口）。
	rec = e.request(http.MethodGet, "/api/settings/intel/queries", "")
	var ledger struct {
		Queries []struct {
			Provider string `json:"provider"`
			Expr     string `json:"expr"`
			Total    int    `json:"total_hits"`
			TaskID   string `json:"task_id"`
		} `json:"queries"`
		UsedToday  int `json:"used_today"`
		DailyQuota int `json:"daily_quota"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ledger); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Queries) == 0 || ledger.Queries[0].Provider != "fofa" ||
		ledger.Queries[0].Expr != expr || ledger.Queries[0].Total != 123 || ledger.Queries[0].TaskID != task.ID {
		t.Fatalf("查询台账不对: %+v", ledger.Queries)
	}
	if ledger.UsedToday < 1 || ledger.DailyQuota != 50 {
		t.Fatalf("用量/配额回显不对: %+v", ledger)
	}

	// ⑤ 配额到顶即拒，且理由里给用量与出口。
	rec = e.request(http.MethodPut, "/api/settings", `{"intel_daily_quota":1}`)
	if rec.Code != 200 {
		t.Fatalf("改配额 status=%d", rec.Code)
	}
	// 配额是"当天全实例"口径，验收库可能带着更早的调用（重跑不重建库时）——
	// 所以只钉"给上限、给用量、给出口"，不钉具体数字。
	if flat := call(`domain="example.com"`); !strings.Contains(flat, "每日上限") ||
		!regexp.MustCompile(`今日 \d+/1 次`).MatchString(flat) ||
		!strings.Contains(flat, "系统设置 → 情报源") {
		t.Fatalf("超配额应拒绝并说明用量与出口: %s", flat)
	}

	// ⑥ 设置回显：只给布尔，不给明文。
	rec = e.request(http.MethodGet, "/api/settings", "")
	body := rec.Body.String()
	if !strings.Contains(body, `"fofa_key_set":true`) {
		t.Fatalf("设置页应回显 key 已配置: %s", body)
	}
	if strings.Contains(body, "acc-key") {
		t.Fatal("设置接口绝不能回显 key 明文")
	}

	// ⑦ 测试连接：打 info 路径并回显账号与剩余额度。
	rec = e.request(http.MethodPost, "/api/settings/intel/test", `{"provider":"fofa"}`)
	var probe struct {
		OK   bool           `json:"ok"`
		Info map[string]any `json:"info"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatal(err)
	}
	if !probe.OK || probe.Info["email"] != "acc@example.com" ||
		probe.Info["remain_free_point"].(float64) != 999 {
		t.Fatalf("测试连接应答不对: %s", rec.Body.String())
	}

	// ⑧ 查到的东西要当意图目标时，照样被资产拦截挡住（情报不等于授权）。
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)
	tsx.SetAssetStore(e.m.Assets(), e.m.Assets().Companies())
	insTool := workflowTool(t, tsx.WorkerTools(), "insert_assets")
	ires, err := insTool.Call(ctx, json.RawMessage(`{"assets":[{"type":"subdomain","domain":"a.example.com"}]}`), nil)
	if err != nil || !strings.Contains(ires.Flatten(), "results") {
		t.Fatalf("insert_assets: %v %s", err, ires.Flatten())
	}
	var insOut struct {
		Results []struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"results"`
	}
	// insert_assets 的回执就是一段紧凑 JSON（jsonResult 不带表头），从第一个 { 起解。
	rawIns := ires.Flatten()
	if i := strings.Index(rawIns, "{"); i >= 0 {
		rawIns = rawIns[i:]
	}
	if err := json.Unmarshal([]byte(rawIns), &insOut); err != nil {
		t.Fatalf("decode insert_assets: %v raw=%s", err, ires.Flatten())
	}
	if len(insOut.Results) != 1 || insOut.Results[0].ID == 0 {
		t.Fatalf("资产没登记进去: %s", ires.Flatten())
	}
	assetID := insOut.Results[0].ID
	if _, err := e.m.Assets().CreateTaskInterceptRule(tid, "block", "fuzzy_domain", "example.com",
		"验收：情报结果不得越界", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.m.pg.Exec(`DELETE FROM task_intercept_rules WHERE task_id=$1`, tid) })
	intentTool := workflowTool(t, tsx.PlannerTools(), "add_intent")
	tsx.SetAssetStore(e.m.Assets(), e.m.Assets().Companies())
	pres, err := intentTool.Call(ctx, json.RawMessage(
		`{"summary":"对情报查到的 a.example.com 做未授权访问测试","asset_ids":[`+strconv.FormatInt(assetID, 10)+`]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if flat := pres.Flatten(); !strings.Contains(flat, "未通过测试范围校验") {
		t.Fatalf("命中资产拦截的意图必须被拒: %s", flat)
	}

	// ⑨ 未知 provider 给可读错误（框架留了加源的口子，但别静默串到别的源）。
	if flat := call(`domain="example.com"`); strings.Contains(flat, "命中") {
		t.Fatalf("配额已到顶，不该再有成功查询: %s", flat)
	}
	ures, err := intelTool.Call(ctx, json.RawMessage(`{"provider":"quake","expr":"x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ures.Flatten(), "未知情报源") {
		t.Fatalf("未注册的源应给可读错误: %s", ures.Flatten())
	}
}

// accP22 走完计划任务：结构化频率（不引 cron）→ 到点按规格建任务（规格原样落地：描述/目标/
// 任务级拦截规则都跟着走）→ **CAS 抢跑不重复**（再 tick 一次不建第二个）→ 禁用即停 →
// 触发失败如实落 last_error 且不建空任务 → 「立即运行」是额外一次、不改排期 →
// 非法频率/空目标写入即 400。
func accP22(t *testing.T, e *accEnv) {
	// 收尾：删掉本用例造的计划任务与它们建出来的任务。
	var createdTasks []string
	t.Cleanup(func() {
		for _, id := range createdTasks {
			e.s.engine.StopTask(id)
			if n, err := strconv.ParseInt(id, 10, 64); err == nil {
				_ = e.m.pg.DeleteTask(n)
			}
		}
		_, _ = e.m.pg.Exec(`DELETE FROM task_schedules WHERE name LIKE 'acceptance-p22%'`)
	})

	// ① 建计划任务：每 6 小时，规格里带一条任务级拦截规则（证明规格原样落地）。
	rec := e.request(http.MethodPost, "/api/task-schedules", `{
		"name":"acceptance-p22-hourly",
		"frequency":{"kind":"every_hours","every_hours":6},
		"task_spec":{"description":"acceptance-p22 巡检","goal":"对 example.com 做只读复测",
			"intercept_rules":[{"action":"block","kind":"fuzzy_domain","pattern":"never.example.com","note":"验收：规格里的规则要跟着走"}]}
	}`)
	if rec.Code != 201 {
		t.Fatalf("建计划任务 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var schedDTO struct {
		ID            int64  `json:"id"`
		Enabled       bool   `json:"enabled"`
		FrequencyText string `json:"frequency_text"`
		NextRunAt     string `json:"next_run_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &schedDTO); err != nil {
		t.Fatal(err)
	}
	if schedDTO.FrequencyText != "每 6 小时" || !schedDTO.Enabled || schedDTO.NextRunAt == "" {
		t.Fatalf("计划任务应答不对: %s", rec.Body.String())
	}
	next, err := time.Parse(time.RFC3339, schedDTO.NextRunAt)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(next); d < 5*time.Hour+50*time.Minute || d > 6*time.Hour {
		t.Fatalf("下次运行应在约 6 小时后，实际 %s", d)
	}

	// ② 手动把它置为"已到点"，驱动一次 tick：应按规格建任务、并把排期推到下一次。
	if _, err := e.m.pg.Exec(`UPDATE task_schedules SET next_run_at=now()-interval '1 minute' WHERE id=$1`, schedDTO.ID); err != nil {
		t.Fatal(err)
	}
	sc := newScheduler(e.s)
	sc.runDueSchedules(e.ctx)
	row, err := e.m.pg.GetTaskSchedule(schedDTO.ID)
	if err != nil || row == nil {
		t.Fatalf("读计划任务: %v", err)
	}
	if row.LastTaskID == "" || row.LastError != "" {
		t.Fatalf("到点应建成任务且无错误: task=%q err=%q", row.LastTaskID, row.LastError)
	}
	createdTasks = append(createdTasks, row.LastTaskID)
	if row.NextRunAt == nil || !row.NextRunAt.After(time.Now().Add(5*time.Hour)) {
		t.Fatalf("触发后应把排期推到下一次: %v", row.NextRunAt)
	}
	taskID, _ := strconv.ParseInt(row.LastTaskID, 10, 64)
	pgTask, err := e.m.pg.GetTask(taskID)
	if err != nil || pgTask == nil {
		t.Fatalf("读建成任务: %v", err)
	}
	if pgTask.Description != "acceptance-p22 巡检" || pgTask.Goal != "对 example.com 做只读复测" {
		t.Fatalf("规格没有原样落地: %+v", pgTask)
	}
	var ruleCount int
	if err := e.m.pg.QueryRow(`SELECT count(*) FROM task_intercept_rules WHERE task_id=$1 AND pattern='never.example.com'`, row.LastTaskID).Scan(&ruleCount); err != nil {
		t.Fatal(err)
	}
	if ruleCount != 1 {
		t.Fatalf("规格里的任务级拦截规则应跟着建出来，实际 %d 条", ruleCount)
	}

	// ③ 再 tick 一次：排期已推到未来 → **不重复建任务**。
	if _, err := e.m.pg.Exec(`UPDATE task_schedules SET last_task_id='' WHERE id=$1`, schedDTO.ID); err != nil {
		t.Fatal(err)
	}
	sc.runDueSchedules(e.ctx)
	if row2, _ := e.m.pg.GetTaskSchedule(schedDTO.ID); row2 != nil && row2.LastTaskID != "" {
		t.Fatalf("不该重复建任务，却又建了 #%s", row2.LastTaskID)
	}
	var dupCount int
	if err := e.m.pg.QueryRow(`SELECT count(*) FROM tasks WHERE description='acceptance-p22 巡检'`).Scan(&dupCount); err != nil {
		t.Fatal(err)
	}
	if dupCount != 1 {
		t.Fatalf("同一时刻只该建一个任务，实际 %d 个", dupCount)
	}

	// ④ 「立即运行」是额外一次，不改排期。
	before, _ := e.m.pg.GetTaskSchedule(schedDTO.ID)
	rec = e.request(http.MethodPost, "/api/task-schedules/"+strconv.FormatInt(schedDTO.ID, 10)+"/run", "")
	if rec.Code != 200 {
		t.Fatalf("立即运行 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var runResp struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &runResp); err != nil {
		t.Fatal(err)
	}
	createdTasks = append(createdTasks, runResp.TaskID)
	after, _ := e.m.pg.GetTaskSchedule(schedDTO.ID)
	if after.NextRunAt == nil || before.NextRunAt == nil || !after.NextRunAt.Equal(*before.NextRunAt) {
		t.Fatalf("立即运行不该改排期: before=%v after=%v", before.NextRunAt, after.NextRunAt)
	}
	if after.LastTaskID != runResp.TaskID {
		t.Fatalf("立即运行应记 last_task_id: %q", after.LastTaskID)
	}

	// ⑤ 禁用即停：清空排期，强制置过期也不触发。
	rec = e.request(http.MethodPatch, "/api/task-schedules/"+strconv.FormatInt(schedDTO.ID, 10), `{"enabled":false}`)
	if rec.Code != 200 {
		t.Fatalf("禁用 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if row3, _ := e.m.pg.GetTaskSchedule(schedDTO.ID); row3.NextRunAt != nil {
		t.Fatalf("禁用后应清空排期: %v", row3.NextRunAt)
	}
	if _, err := e.m.pg.Exec(`UPDATE task_schedules SET next_run_at=now()-interval '1 minute', last_task_id='' WHERE id=$1`, schedDTO.ID); err != nil {
		t.Fatal(err)
	}
	sc.runDueSchedules(e.ctx)
	if row4, _ := e.m.pg.GetTaskSchedule(schedDTO.ID); row4.LastTaskID != "" {
		t.Fatalf("禁用后不该再触发: %s", row4.LastTaskID)
	}

	// ⑥ 触发失败：分类被删 → 如实落 last_error、不建空任务、排期照推（不热循环重试）。
	rec = e.request(http.MethodPost, "/api/task-schedules", `{
		"name":"acceptance-p22-broken",
		"frequency":{"kind":"every_minutes","every_minutes":30},
		"task_spec":{"description":"acceptance-p22 坏规格","goal":"g","category_id":999999}
	}`)
	// 建的时候就该被拦（分类不存在）→ 400；换一条"建好后再变坏"的路径验证触发期失败。
	if rec.Code != 400 {
		t.Fatalf("不存在的分类应在写入时就拒: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.request(http.MethodPost, "/api/task-schedules", `{
		"name":"acceptance-p22-broken2",
		"frequency":{"kind":"every_minutes","every_minutes":30},
		"task_spec":{"description":"acceptance-p22 坏规格","goal":"g"}
	}`)
	if rec.Code != 201 {
		t.Fatalf("建坏规格计划 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var broken struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &broken); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.pg.Exec(`UPDATE task_schedules SET task_spec = jsonb_set(task_spec, '{description}', '""') WHERE id=$1`, broken.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.pg.Exec(`UPDATE task_schedules SET next_run_at=now()-interval '1 minute' WHERE id=$1`, broken.ID); err != nil {
		t.Fatal(err)
	}
	sc.runDueSchedules(e.ctx)
	brow, _ := e.m.pg.GetTaskSchedule(broken.ID)
	if brow == nil || brow.LastError == "" {
		t.Fatalf("触发失败必须如实落 last_error: %+v", brow)
	}
	if brow.LastTaskID != "" {
		t.Fatalf("失败时不该记任务 id: %s", brow.LastTaskID)
	}
	if brow.NextRunAt == nil || !brow.NextRunAt.After(time.Now()) {
		t.Fatalf("失败也要把排期推到下一次（否则每 5s 热循环重试）: %v", brow.NextRunAt)
	}

	// ⑦ 非法频率 / 空目标：写入即 400。
	if rec := e.request(http.MethodPost, "/api/task-schedules",
		`{"name":"acceptance-p22-bad","frequency":{"kind":"every_minutes","every_minutes":1},"task_spec":{"description":"d","goal":"g"}}`); rec.Code != 400 {
		t.Fatalf("非法频率应 400: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.request(http.MethodPost, "/api/task-schedules",
		`{"name":"acceptance-p22-bad2","frequency":{"kind":"cron","at":"* * * * *"},"task_spec":{"description":"d","goal":"g"}}`); rec.Code != 400 {
		t.Fatalf("cron 表达式应被拒（本设计不支持）: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.request(http.MethodPost, "/api/task-schedules",
		`{"name":"acceptance-p22-bad3","frequency":{"kind":"daily_at","at":"09:30"},"task_spec":{"description":"d","goal":"  "}}`); rec.Code != 400 {
		t.Fatalf("空目标应 400: %d %s", rec.Code, rec.Body.String())
	}

	// ⑧ 列表：人话频率 + 状态齐备（UI 直接渲染）。
	rec = e.request(http.MethodGet, "/api/task-schedules", "")
	var listed struct {
		Schedules []struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			FrequencyText string `json:"frequency_text"`
			Enabled       bool   `json:"enabled"`
			LastError     string `json:"last_error"`
		} `json:"schedules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range listed.Schedules {
		if s.ID == broken.ID {
			found = s.FrequencyText == "每 30 分钟" && s.LastError != ""
		}
	}
	if !found {
		t.Fatalf("列表应给出人话频率与上次错误: %+v", listed.Schedules)
	}
}

// accP23 走完公司级知识层：任务 A 把一条已确认结论沉淀给公司 → **无血缘**的同公司任务 B 在
// 态势（graph_overview）里读到它且带来源 → 别的公司看不到 → 废弃（必填理由）后两边都看不到、
// 恢复后又可见 → 只读性：Agent 只有沉淀工具、没有改/废工具，且沉淀工具拒绝写错公司 →
// 发现详情页那条入口与工具同规则（被撤回的发现不能沉淀）。
func accP23(t *testing.T, e *accEnv) {
	companies := e.m.Assets().Companies()
	companyA, err := companies.UpsertByName("acceptance-p23-A")
	if err != nil {
		t.Fatal(err)
	}
	companyB, err := companies.UpsertByName("acceptance-p23-B")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.m.pg.Exec(`DELETE FROM company_knowledge WHERE company_id IN ($1,$2)`, companyA, companyB)
		_ = companies.DeleteCompany(companyA)
		_ = companies.DeleteCompany(companyB)
	})

	// 两个同公司但**没有任何血缘**的任务（task_relations 里互不引用）。
	taskA, tidA := e.newTaskWithCompanies(t, "acceptance-p23-A", "沉淀方", companyA)
	taskB, _ := e.newTaskWithCompanies(t, "acceptance-p23-B", "读取方（与本任务无血缘）", companyA)
	other, otherTid := e.newTaskWithCompanies(t, "acceptance-p23-other", "别的公司", companyB)

	// ① 任务 A：报一条发现（节点 confirmed）→ 用工具沉淀给公司 A。
	fid, nodeID := e.newFinding(taskA, tidA,
		`{"vulnclass":"authz","severity":"medium","summary":"后台列表接口未校验角色，普通账号可读全量用户","evidence":"账号 B 请求 /api/admin/users 返回 200","confidence":"high"}`)
	tsxA := agent.NewToolSet(taskA.Store, "planner")
	tsxA.SetTaskID(tidA)
	tsxA.SetAssetStore(e.m.Assets(), e.m.Assets().Companies())
	promote := workflowTool(t, tsxA.PlannerTools(), "promote_knowledge")
	pres, err := promote.Call(context.Background(), json.RawMessage(fmt.Sprintf(
		`{"company_id":%d,"node_id":%d,"kind":"conclusion","confidence":"high","detail":"复现：普通账号打 /api/admin/users"}`,
		companyA, nodeID)), nil)
	if err != nil || !strings.Contains(pres.Flatten(), `"id"`) {
		t.Fatalf("沉淀失败: %v %s", err, pres.Flatten())
	}
	var kid int64
	{
		raw := pres.Flatten()
		if i := strings.Index(raw, "{"); i >= 0 {
			raw = raw[i:]
		}
		var out struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode promote: %v %s", err, pres.Flatten())
		}
		kid = out.ID
	}
	if kid == 0 {
		t.Fatalf("没有拿到知识 id: %s", pres.Flatten())
	}

	// ② 无血缘的同公司任务 B：态势里读到它 + 来源 + 只读说明。
	tsxB := agent.NewToolSet(taskB.Store, "planner")
	tsxB.SetTaskID(tidBOf(t, e, taskB))
	tsxB.SetAssetStore(e.m.Assets(), e.m.Assets().Companies())
	overview := workflowTool(t, tsxB.PlannerTools(), "graph_overview")
	ores, err := overview.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	flat := ores.Flatten()
	if !strings.Contains(flat, "后台列表接口未校验角色") {
		t.Fatalf("同公司无血缘任务应读到沉淀：%s", flat)
	}
	if !strings.Contains(flat, "来自任务 #"+taskA.ID) {
		t.Fatalf("读到知识必须带来源任务：%s", flat)
	}
	if !strings.Contains(flat, "不代表本任务已测过") {
		t.Fatalf("必须写明这是只读先验、不是本任务已测：%s", flat)
	}

	// ③ 别的公司看不到。
	tsxOther := agent.NewToolSet(other.Store, "planner")
	tsxOther.SetTaskID(otherTid)
	tsxOther.SetAssetStore(e.m.Assets(), e.m.Assets().Companies())
	oresOther, err := workflowTool(t, tsxOther.PlannerTools(), "graph_overview").Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(oresOther.Flatten(), "后台列表接口未校验角色") {
		t.Fatalf("别的公司不该看到: %s", oresOther.Flatten())
	}

	// ④ 列表接口（含已废弃）+ 废弃必须给理由 + 废弃后两边都读不到 + 恢复后又可见。
	rec := e.request(http.MethodGet, "/api/companies/"+strconv.FormatInt(companyA, 10)+"/knowledge", "")
	var listed struct {
		Knowledge []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"knowledge"`
		Active int `json:"active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Knowledge) != 1 || listed.Knowledge[0].Status != "active" || listed.Active != 1 {
		t.Fatalf("知识列表不对: %s", rec.Body.String())
	}
	base := "/api/companies/" + strconv.FormatInt(companyA, 10) + "/knowledge/" + strconv.FormatInt(kid, 10)
	if rec := e.request(http.MethodPatch, base, `{"status":"deprecated"}`); rec.Code != 400 {
		t.Fatalf("废弃不给理由应 400: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.request(http.MethodPatch, base, `{"status":"deprecated","reason":"复测发现该接口已加角色校验"}`); rec.Code != 200 {
		t.Fatalf("废弃失败: %d %s", rec.Code, rec.Body.String())
	}
	ores, _ = overview.Call(context.Background(), json.RawMessage(`{}`), nil)
	if strings.Contains(ores.Flatten(), "后台列表接口未校验角色") {
		t.Fatalf("废弃后不该再注入: %s", ores.Flatten())
	}
	if rec := e.request(http.MethodPatch, base, `{"status":"active"}`); rec.Code != 200 {
		t.Fatalf("恢复失败: %d %s", rec.Code, rec.Body.String())
	}
	ores, _ = overview.Call(context.Background(), json.RawMessage(`{}`), nil)
	if !strings.Contains(ores.Flatten(), "后台列表接口未校验角色") {
		t.Fatalf("恢复后应重新注入: %s", ores.Flatten())
	}

	// ⑤ 只读性：Agent 侧只有"沉淀"这一个写口——没有任何改/废工具。
	allTools := map[string]bool{}
	for _, tool := range append(tsxA.PlannerTools(), tsxA.WorkerTools()...) {
		allTools[tool.Name()] = true
	}
	for _, tool := range tsxA.MainAgentTools() {
		allTools[tool.Name()] = true
	}
	if !allTools["promote_knowledge"] {
		t.Fatal("planner 应能用 promote_knowledge")
	}
	for name := range allTools {
		if name == "promote_knowledge" {
			continue
		}
		if strings.Contains(name, "knowledge") {
			t.Fatalf("Agent 不该有第二个知识写口（改/废是人的动作）: %s", name)
		}
	}
	// 沉淀工具拒绝写错公司（后来人会当真的用，写错比不写更糟）。
	bad, err := promote.Call(context.Background(), json.RawMessage(fmt.Sprintf(
		`{"company_id":%d,"node_id":%d,"summary":"x"}`, companyB, nodeID)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bad.Flatten(), "不是本任务关联的企业") {
		t.Fatalf("不该允许沉淀到任意公司: %s", bad.Flatten())
	}

	// ⑥ 发现详情页入口：与工具同规则（confirmed 才能沉淀）——先撤回再沉淀应被拒。
	retractedFid, retractedNode := e.newFinding(taskA, tidA,
		`{"vulnclass":"xss","severity":"low","summary":"反射型 XSS（待推翻）","evidence":"原样回显","confidence":"low"}`)
	rec = e.request(http.MethodPatch, "/api/exploration/findings/"+strconv.FormatInt(retractedFid, 10),
		`{"status":"retracted","retract_reason":"复测推翻：输出被 HTML 编码"}`)
	if rec.Code != 200 {
		t.Fatalf("撤回 status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = e.request(http.MethodPost, "/api/findings/"+strconv.FormatInt(retractedFid, 10)+"/promote-knowledge",
		fmt.Sprintf(`{"company_id":%d,"summary":"反射型 XSS"}`, companyA))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "不能沉淀为知识") {
		t.Fatalf("被撤回的发现不能沉淀: %d %s", rec.Code, rec.Body.String())
	}
	// 未撤回的那条走页面入口可以沉淀（同一写入路径）。
	rec = e.request(http.MethodPost, "/api/findings/"+strconv.FormatInt(fid, 10)+"/promote-knowledge",
		fmt.Sprintf(`{"company_id":%d,"kind":"asset_hint","summary":"后台接口面：/api/admin/*"}`, companyA))
	if rec.Code != 200 {
		t.Fatalf("页面入口沉淀失败: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.request(http.MethodGet, "/api/companies/"+strconv.FormatInt(companyA, 10)+"/knowledge", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Active != 2 {
		t.Fatalf("页面入口应沉淀出第 2 条（active=2）: %s", rec.Body.String())
	}
	_ = retractedNode
}

// newTaskWithCompanies 建一个关联了指定企业的任务（走真实建任务路径：task_scope 落 company 行）。
func (e *accEnv) newTaskWithCompanies(t *testing.T, desc, goal string, companyID int64) (*Task, int64) {
	t.Helper()
	task, err := e.m.CreateTaskWithOptions(desc, goal, db.TaskCreateOptions{CompanyIDs: []int64{companyID}})
	if err != nil {
		t.Fatalf("create task with company: %v", err)
	}
	tid := parseInt64(t, task.ID)
	t.Cleanup(func() {
		e.s.engine.StopTask(task.ID)
		_, _ = e.m.pg.Exec(`DELETE FROM findings WHERE task_id=$1`, tid)
		_, _ = e.m.pg.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, task.ID)
		_, _ = e.m.pg.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, task.ID)
		_ = e.m.pg.DeleteTask(tid)
	})
	return task, tid
}

// tidBOf 取任务 id（newTask 已返回，这里只是让读取方的书写与 A 对称）。
func tidBOf(t *testing.T, e *accEnv, task *Task) int64 { t.Helper(); return parseInt64(t, task.ID) }

// accP24 走完执行限额与 MCP 熔断：
//
//	① 内存限额真的限住（同一条命令：设了限额 → 失败且台账写明限额；清掉 → 成功）；
//	② CPU 限额被撞到时，台账理由是"被信号终止（很可能触发 ulimit -t）"而不是含糊的失败；
//	③ 台账的 limits 快照说清"请求了什么、哪些真生效"（本机不支持某轴时如实标注）；
//	④ MCP：设置 → 解析器 → 库行为 → API 可见 → 手动重置，整条链走通（连续失败开闸后
//	   **不再打服务端**，重置后恢复真实调用）。
func accP24(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p24", "执行限额与 MCP 熔断")

	// 设置收尾：本用例改过的键全部恢复原值。
	saved := map[string]struct {
		v  string
		ok bool
	}{}
	for _, k := range []string{settingExecCPUSeconds, settingExecMemoryMB, settingExecMaxProcs,
		settingMCPMaxConcurrent, settingMCPBreakerFails, settingMCPBreakerCooldown} {
		v, ok, _ := e.m.pg.GetSetting(k)
		saved[k] = struct {
			v  string
			ok bool
		}{v, ok}
	}
	t.Cleanup(func() {
		for k, r := range saved {
			if r.ok {
				_ = e.m.pg.SetSetting(k, r.v)
			} else {
				_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, k)
			}
		}
	})

	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)
	tsx.SetExecService(e.m.Executions())
	runTool := workflowTool(t, tsx.WorkerTools(), "run_execution")
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{TaskID: tid})

	// runAndWait 起一条执行并等它落终态，返回台账行。
	runAndWait := func(command string) *db.ToolExecution {
		t.Helper()
		res, err := runTool.Call(ctx, json.RawMessage(`{"command":`+strconv.Quote(command)+`}`), nil)
		if err != nil || !strings.Contains(res.Flatten(), "execution started: ") {
			t.Fatalf("run_execution: %v %s", err, res.Flatten())
		}
		var id int64
		if _, err := fmt.Sscanf(res.Flatten(), "execution started: %d", &id); err != nil || id == 0 {
			t.Fatalf("解析 execution_id 失败: %s", res.Flatten())
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			row, err := e.m.pg.GetExecution(id)
			if err != nil {
				t.Fatal(err)
			}
			if row != nil && !agent.ExecActive(row.Status) {
				return row
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("执行 %d 30s 未落终态", id)
		return nil
	}

	// ① 内存限额：设 256MB → 申请 500MB 的命令失败；清掉限额 → 同一条命令成功。
	// 用 python3 申请内存（环境里必有：自定义工具的解释器探测在用）。
	const memCmd = `python3 -c "x=bytearray(500*1024*1024); print(len(x))"`
	rec := e.request(http.MethodPut, "/api/settings", `{"exec_memory_mb":256,"exec_cpu_seconds":0,"exec_max_procs":0}`)
	if rec.Code != 200 {
		t.Fatalf("设置内存限额 status=%d body=%s", rec.Code, rec.Body.String())
	}
	limited := runAndWait(memCmd)
	var snap struct {
		MemoryMB   int    `json:"memory_mb"`
		AppliedMem bool   `json:"applied_mem"`
		Note       string `json:"note"`
	}
	if len(limited.Limits) == 0 {
		t.Fatal("台账必须记限额快照")
	}
	if err := json.Unmarshal(limited.Limits, &snap); err != nil {
		t.Fatalf("限额快照不是合法 JSON: %s (%v)", limited.Limits, err)
	}
	if snap.MemoryMB != 256 {
		t.Fatalf("限额快照没记请求值: %s", limited.Limits)
	}
	if !snap.AppliedMem {
		t.Skipf("本机 /bin/sh 不支持 ulimit -v（快照如实记了：%s）：内存轴限额无法验证", limited.Limits)
	}
	if limited.Status == db.ExecCompleted && limited.ExitCode != nil && *limited.ExitCode == 0 {
		t.Fatalf("500MB 分配在 256MB 限额下不该成功: %+v", limited)
	}
	full, err := os.ReadFile(limited.OutputPath)
	if err != nil {
		t.Fatalf("读执行输出: %v", err)
	}
	if !strings.Contains(string(full), "MemoryError") {
		t.Fatalf("输出应显示分配失败（MemoryError）: %s", string(full))
	}
	// 清掉限额 → 同一条命令应当成功（这才证明"是限额让它失败"）。
	if rec := e.request(http.MethodPut, "/api/settings", `{"exec_memory_mb":0}`); rec.Code != 200 {
		t.Fatalf("清内存限额 status=%d", rec.Code)
	}
	unlimited := runAndWait(memCmd)
	if unlimited.Status != db.ExecCompleted || unlimited.ExitCode == nil || *unlimited.ExitCode != 0 {
		t.Fatalf("去掉限额后同一条命令应当成功: %+v", unlimited)
	}

	// ② CPU 限额：1 秒上限 + 死循环（exec 让外层 shell 自己变成死循环进程，
	// 这样撞限额的是我们的直接子进程，信号归类才可验证）。
	if rec := e.request(http.MethodPut, "/api/settings", `{"exec_cpu_seconds":1}`); rec.Code != 200 {
		t.Fatalf("设置 CPU 限额 status=%d", rec.Code)
	}
	spin := runAndWait(`exec /bin/sh -c 'i=0; while true; do i=$((i+1)); done'`)
	if spin.Status == db.ExecCompleted {
		t.Fatalf("死循环不该正常结束: %+v", spin)
	}
	if !strings.Contains(spin.Error, "信号 24") || !strings.Contains(spin.Error, "CPU 限额") {
		t.Fatalf("撞 CPU 限额的理由要说清楚（当前: %q）", spin.Error)
	}
	if rec := e.request(http.MethodPut, "/api/settings", `{"exec_cpu_seconds":0}`); rec.Code != 200 {
		t.Fatalf("清 CPU 限额 status=%d", rec.Code)
	}

	// ③ 设置回显：三个执行限额键可读可写（UI 靠它渲染）。
	rec = e.request(http.MethodGet, "/api/settings", "")
	var st struct {
		ExecCPUSeconds *int `json:"exec_cpu_seconds"`
		ExecMemoryMB   *int `json:"exec_memory_mb"`
		MCPConcurrent  *int `json:"mcp_max_concurrent_per_server"`
		MCPBreaker     *int `json:"mcp_breaker_threshold"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.ExecCPUSeconds == nil || st.ExecMemoryMB == nil || st.MCPConcurrent == nil || st.MCPBreaker == nil {
		t.Fatalf("设置载荷缺少 P24 字段: %s", rec.Body.String())
	}

	// ④ MCP 熔断端到端：假服务永远 500 → 连续失败开闸 → 不再打服务端 → API 可见 → 重置恢复。
	var mcpCalls int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mcpCalls, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	mcpName := "acceptance-p24-bad"
	if rec := e.request(http.MethodPost, "/api/mcp", fmt.Sprintf(
		`{"name":%q,"transport":"http","url":%q,"enabled":true}`, mcpName, bad.URL)); rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("建 MCP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var mcpRow struct {
		ID int64 `json:"id"`
	}
	// 列表里拿到 id（POST 可能不返回整行）。
	getMCPID := func() int64 {
		rec := e.request(http.MethodGet, "/api/mcp", "")
		var listed struct {
			Servers []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"servers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		for _, s := range listed.Servers {
			if s.Name == mcpName {
				return s.ID
			}
		}
		return 0
	}
	mcpRow.ID = getMCPID()
	if mcpRow.ID == 0 {
		t.Fatal("MCP 没建出来")
	}
	t.Cleanup(func() {
		_, _ = e.m.pg.Exec(`DELETE FROM mcp_servers WHERE name=$1`, mcpName)
		mcphttp.ResetGuard(mcpName)
	})
	// 阈值 3、冷却拉长（保证测试窗口内一直是开闸态）。
	if rec := e.request(http.MethodPut, "/api/settings",
		`{"mcp_breaker_threshold":3,"mcp_breaker_cooldown_sec":120,"mcp_max_concurrent_per_server":2}`); rec.Code != 200 {
		t.Fatalf("设置 MCP 守卫 status=%d body=%s", rec.Code, rec.Body.String())
	}
	fail := func() error {
		cl, err := mcphttp.New(context.Background(), mcpName, bad.URL, nil, false)
		if err == nil {
			_ = cl.Close()
		}
		return err
	}
	for i := 0; i < 3; i++ {
		if err := fail(); err == nil {
			t.Fatal("坏服务不该连接成功")
		}
	}
	before := atomic.LoadInt32(&mcpCalls)
	start := time.Now()
	err = fail()
	if err == nil || !strings.Contains(err.Error(), "熔断冷却") {
		t.Fatalf("连续失败后应熔断并说明: %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("熔断应立刻失败: %s", time.Since(start))
	}
	if got := atomic.LoadInt32(&mcpCalls); got != before {
		t.Fatalf("熔断期内不该再打服务端（%d → %d）", before, got)
	}
	// API 可见：列表附 guards，开闸 + 剩余秒 + 服务名。
	rec = e.request(http.MethodGet, "/api/mcp", "")
	var withGuards struct {
		Guards map[string]struct {
			Open             bool   `json:"open"`
			OpenRemainingSec int    `json:"open_remaining_sec"`
			MaxConcurrent    int    `json:"max_concurrent"`
			LastError        string `json:"last_error"`
		} `json:"guards"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &withGuards); err != nil {
		t.Fatal(err)
	}
	g, ok := withGuards.Guards[mcpName]
	if !ok || !g.Open || g.OpenRemainingSec <= 0 || g.MaxConcurrent != 2 || g.LastError == "" {
		t.Fatalf("MCP 列表应带守卫快照: %+v (body=%s)", withGuards.Guards, rec.Body.String())
	}
	// 手动重置 → 恢复真实调用（计数增长，且错误不再是熔断话术）。
	rec = e.request(http.MethodPost, "/api/mcp/"+strconv.FormatInt(mcpRow.ID, 10)+"/guard/reset", "")
	if rec.Code != 200 {
		t.Fatalf("重置熔断 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := fail(); err == nil || strings.Contains(err.Error(), "熔断冷却") {
		t.Fatalf("重置后应恢复真实调用: %v", err)
	}
	if got := atomic.LoadInt32(&mcpCalls); got <= before {
		t.Fatal("重置后应真的打了服务端")
	}
}

// fakeMCPToolServer 起一个最小可用的 MCP（SSE 传输，两件工具），给 accP25 用：
// 只实现 initialize / tools/list，够 assembly 的连接 + 列工具路径走通。支持多条
// 并发流（ToolAugment 每次调用都会新开一条）。返回 /sse 端点 URL。
func fakeMCPToolServer(t *testing.T) string {
	t.Helper()
	var mu sync.Mutex
	streams := map[string]chan []byte{}
	var seq int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sse":
			id := fmt.Sprintf("acc-p25-%d", atomic.AddInt64(&seq, 1))
			ch := make(chan []byte, 8)
			mu.Lock()
			streams[id] = ch
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fmt.Fprintf(w, "event: endpoint\ndata: %s/message?sessionId=%s\n\n", "http://"+r.Host, id)
			w.(http.Flusher).Flush()
			for {
				select {
				case payload := <-ch:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					mu.Lock()
					delete(streams, id)
					mu.Unlock()
					return
				}
			}
		case "/message":
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			if req.ID == 0 {
				return // notification（initialized 等）无需回包
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2024-11-05",
					"capabilities": map[string]any{"tools": map[string]any{}},
					"serverInfo":   map[string]any{"name": "acc-p25", "version": "1"}}
			case "tools/list":
				result = map[string]any{"tools": []any{
					map[string]any{"name": "acc_p25_keep", "description": "保留的工具",
						"inputSchema": map[string]any{"type": "object",
							"properties": map[string]any{"target": map[string]any{"type": "string", "description": "目标"}},
							"required":   []any{"target"}}},
					map[string]any{"name": "acc_p25_off", "description": "将被关掉的工具",
						"inputSchema": map[string]any{"type": "object"}},
				}}
			default:
				result = map[string]any{}
			}
			res, err := json.Marshal(result)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(res)})
			mu.Lock()
			ch := streams[r.URL.Query().Get("sessionId")]
			mu.Unlock()
			if ch != nil {
				ch <- payload
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/sse"
}

// accP25 走完 MCP 工具级开关（全局）：
//
//	① 关掉的工具**真的不在 Agent 的工具面里**（ToolAugment 连上 server 后按开关过滤），
//	   同一台 server 的其它工具不受影响；
//	② 重新发现（POST /{id}/refresh，走 SaveMCPTools 重建缓存）**不会把人的开关翻回默认**；
//	③ 三处口径自洽：列表的 tools_disabled、工具列表的 enabled、开关回执；
//	④ 名字不在缓存里 → 404（不静默成功）。
func accP25(t *testing.T, e *accEnv) {
	const (
		keepTool = "acc_p25_keep"
		offTool  = "acc_p25_off"
	)
	mcpName := "acceptance-p25-switch"
	rec := e.request(http.MethodPost, "/api/mcp",
		fmt.Sprintf(`{"name":%q,"transport":"sse","url":%q,"enabled":true}`, mcpName, fakeMCPToolServer(t)))
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("建 MCP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == 0 {
		t.Fatalf("建 MCP 未返回 id: %s (%v)", rec.Body.String(), err)
	}
	id := created.ID
	t.Cleanup(func() {
		_, _ = e.m.pg.Exec(`DELETE FROM mcp_servers WHERE id=$1`, id)
		mcphttp.ResetGuard(mcpName)
	})

	// 建 server 时已自动发现 → 缓存里应有我们的两件工具，且默认全开。
	type toolRow struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Enabled     bool            `json:"enabled"`
		Schema      json.RawMessage `json:"schema"`
	}
	listTools := func() []toolRow {
		t.Helper()
		rec := e.request(http.MethodGet, "/api/mcp/"+strconv.FormatInt(id, 10)+"/tools", "")
		var payload struct {
			Tools []toolRow `json:"tools"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("工具列表不是合法 JSON: %s (%v)", rec.Body.String(), err)
		}
		return payload.Tools
	}
	// 缓存里存的是**全名** mcp__server__tool（mcphttp.ToolName）——UI 与开关都用它。
	fullName := map[string]string{}
	byName := map[string]toolRow{}
	for _, x := range listTools() {
		for _, suffix := range []string{keepTool, offTool} {
			if strings.HasSuffix(x.Name, suffix) {
				fullName[suffix] = x.Name
			}
		}
		byName[x.Name] = x
	}
	if len(byName) != 2 || fullName[keepTool] == "" || fullName[offTool] == "" {
		t.Fatalf("自动发现应缓存两件工具（mcp__%s__ 前缀）: %+v", mcpName, byName)
	}
	if !byName[fullName[keepTool]].Enabled || !byName[fullName[offTool]].Enabled {
		t.Fatalf("新发现的工具默认应启用: %+v", byName)
	}
	// 发现时抓到的 inputSchema 要随缓存给到前端（右栏展开参数表用它，不必再连外部 server）。
	if !strings.Contains(string(byName[fullName[keepTool]].Schema), `"target"`) {
		t.Fatalf("工具 schema 应随缓存返回: %s", byName[fullName[keepTool]].Schema)
	}

	// 装 augment（照 accP7：换掉包级变量并在收尾还原）并把这台 server 绑给 worker。
	oldAugment := agent.ToolAugment
	wireAgentAugment(e.m.pg, t.TempDir(), nil)
	t.Cleanup(func() { agent.ToolAugment = oldAugment })
	a, err := e.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		t.Fatalf("worker agent: %v", err)
	}
	prevVis, err := e.m.pg.AgentVisible(a.ID, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.pg.SetAgentVisibilityKind(a.ID, "mcp", append(append([]int64{}, prevVis...), id)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.m.pg.SetAgentVisibilityKind(a.ID, "mcp", prevVis) })

	// toolSurface 跑一次真实的 ToolAugment，返回它给 worker 的工具名集合。
	toolSurface := func() string {
		t.Helper()
		tools, _, cleanup := agent.ToolAugment(context.Background(), "worker")
		if cleanup != nil {
			defer cleanup()
		}
		var b strings.Builder
		for _, tl := range tools {
			b.WriteString(tl.Name())
			b.WriteString("\n")
		}
		return b.String()
	}

	before := toolSurface()
	if !strings.Contains(before, keepTool) || !strings.Contains(before, offTool) {
		t.Fatalf("开关都还开着时两件工具都该在工具面里:\n%s", before)
	}

	// ① 关掉一件：它从工具面消失，另一件不受影响。
	rec = e.request(http.MethodPost, "/api/mcp/"+strconv.FormatInt(id, 10)+"/tools/toggle",
		fmt.Sprintf(`{"name":%q,"enabled":false}`, fullName[offTool]))
	if rec.Code != 200 {
		t.Fatalf("关工具 status=%d body=%s", rec.Code, rec.Body.String())
	}
	after := toolSurface()
	if strings.Contains(after, offTool) {
		t.Fatalf("关掉的工具不该再注入任何 Agent:\n%s", after)
	}
	if !strings.Contains(after, keepTool) {
		t.Fatalf("同 server 的其它工具不该受影响:\n%s", after)
	}

	// ② 重新发现不得把开关翻回默认（走 SaveMCPTools 重建缓存）。
	rec = e.request(http.MethodPost, "/api/mcp/"+strconv.FormatInt(id, 10)+"/refresh", "")
	if rec.Code != 200 {
		t.Fatalf("刷新 status=%d body=%s", rec.Code, rec.Body.String())
	}
	stale := map[string]toolRow{}
	for _, x := range listTools() {
		stale[x.Name] = x
	}
	if stale[fullName[offTool]].Enabled {
		t.Fatalf("重发现把人的开关翻回默认了: %+v", stale[fullName[offTool]])
	}
	if again := toolSurface(); strings.Contains(again, offTool) {
		t.Fatalf("刷新后关掉的工具仍不该注入:\n%s", again)
	}

	// ③ 列表口径：停用计数与工具级 enabled 自洽。
	rec = e.request(http.MethodGet, "/api/mcp", "")
	var listed struct {
		Servers []struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			ToolsDisabled int    `json:"tools_disabled"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range listed.Servers {
		if s.ID == id {
			found = true
			if s.ToolsDisabled != 1 {
				t.Fatalf("列表的 tools_disabled 应为 1: %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("列表里找不到刚建的 MCP: %s", rec.Body.String())
	}

	// ④ 未知名字 → 404（既不静默成功也不静默失败）。
	rec = e.request(http.MethodPost, "/api/mcp/"+strconv.FormatInt(id, 10)+"/tools/toggle",
		`{"name":"acc_p25_nope","enabled":false}`)
	if rec.Code != 404 {
		t.Fatalf("未知工具应 404，实际 status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// accP26 CSV 资产导入：把「先预览再应用」这条契约钉在端到端上——预览一个字节都不写、
// 预览说的数与应用落库的数同出一源、同一份清单导入两次只合并不新增、任务关联的来源如实标 csv。
//
// 域名带纳秒后缀：验收库若被复用（直接 go test 而不走 acceptance.sh 的一次性库），
// 固定名字会撞上上一轮留下的资产，让「新增/已存在」的断言变成掷骰子。
func accP26(t *testing.T, e *accEnv) {
	_, tid := e.newTask("acceptance-p26", "CSV 导入")
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	root := "acc-p26-" + suffix + ".example"
	sub := "api." + root
	site := "https://" + sub + ":8443/login"
	portHost := "www." + root
	t.Cleanup(func() {
		_, _ = e.m.Assets().DeleteByHost(sub)
		_, _ = e.m.Assets().DeleteByHost(root)
	})

	// domain/url 两列。前四行是四种目标形状，后三行刻意是「进不去」与「打折」的形状。
	csvBody := strings.Join([]string{
		"domain,url",
		root + ",",
		sub + "," + site,
		portHost + ":8443,",
		"subdomain,",
		"1234,",
		"ftp://" + root + ",",
	}, "\n")
	payload, _ := json.Marshal(map[string]any{"csv": csvBody, "task_id": tid})

	// ① 预览：只读。报告总数/分类/新增/跳过/警告，并给出逐条样例。
	rec := e.request(http.MethodPost, "/api/assets/import/preview", string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("预览 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Total        int               `json:"total"`
		New          int               `json:"new"`
		Existing     int               `json:"existing"`
		Duplicates   int               `json:"duplicates"`
		ByType       map[string]int    `json:"by_type"`
		Unrecognized []assetImportNote `json:"unrecognized"`
		Warnings     []assetImportNote `json:"warnings"`
		Samples      []struct {
			Line     int    `json:"line"`
			Value    string `json:"value"`
			Type     string `json:"type"`
			Existing bool   `json:"existing"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("预览解码失败: %v raw=%s", err, rec.Body.String())
	}
	if preview.Total != 4 || preview.New != 4 || preview.Existing != 0 {
		t.Fatalf("预览口径不对：total=%d new=%d existing=%d（应 4/4/0）body=%s",
			preview.Total, preview.New, preview.Existing, rec.Body.String())
	}
	if preview.ByType["root_domain"] != 1 || preview.ByType["subdomain"] != 2 || preview.ByType["service"] != 1 {
		t.Errorf("分类计数不对：%v", preview.ByType)
	}
	if len(preview.Unrecognized) != 3 {
		t.Errorf("该如实报出 3 行进不去的（类型词/纯数字/ftp），得到 %v", preview.Unrecognized)
	}
	if len(preview.Warnings) != 1 || !strings.Contains(preview.Warnings[0].Reason, "8443") {
		t.Errorf("host:port 该有一条端口未登记的警告，得到 %v", preview.Warnings)
	}
	if len(preview.Samples) != 4 {
		t.Errorf("样例该逐条列出 4 条，得到 %d", len(preview.Samples))
	}

	// ② 预览必须只读：库里不该有任何一条。
	// 限定 root_domain/subdomain 两类：服务资产也会带 domain 列（从 URL 里提的主机名），
	// 不限定就会把服务行也算进来。
	var present int
	if err := e.m.pg.QueryRow(`
SELECT count(*) FROM assets WHERE type IN ('root_domain','subdomain') AND domain = ANY($1::text[])`,
		"{"+strings.Join([]string{root, sub, portHost}, ",")+"}").Scan(&present); err != nil {
		t.Fatalf("查资产失败: %v", err)
	}
	if present != 0 {
		t.Fatalf("预览写了库：已存在 %d 条", present)
	}

	// ③ 应用：新增数与预览一致。
	rec = e.request(http.MethodPost, "/api/assets/import", string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("应用 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var applied struct {
		Created int `json:"created"`
		Merged  int `json:"merged"`
		Errors  []struct {
			Line  int    `json:"line"`
			Value string `json:"value"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil {
		t.Fatalf("应用解码失败: %v raw=%s", err, rec.Body.String())
	}
	if applied.Created != preview.New || applied.Merged != preview.Existing {
		t.Fatalf("预览与落库口径不一致：预览 new=%d existing=%d，落库 created=%d merged=%d",
			preview.New, preview.Existing, applied.Created, applied.Merged)
	}
	if len(applied.Errors) != 0 {
		t.Fatalf("不该有落库错误：%v", applied.Errors)
	}

	// ④ 幂等：同一份清单再导一次，一条都不新增。
	rec = e.request(http.MethodPost, "/api/assets/import", string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("二次导入 status=%d body=%s", rec.Code, rec.Body.String())
	}
	applied = struct {
		Created int `json:"created"`
		Merged  int `json:"merged"`
		Errors  []struct {
			Line  int    `json:"line"`
			Value string `json:"value"`
			Error string `json:"error"`
		} `json:"errors"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil {
		t.Fatalf("二次导入解码失败: %v", err)
	}
	if applied.Created != 0 || applied.Merged != 4 {
		t.Fatalf("重复导入该只合并：created=%d merged=%d（应 0/4）", applied.Created, applied.Merged)
	}
	var rows int
	if err := e.m.pg.QueryRow(`
SELECT count(*) FROM assets WHERE type IN ('root_domain','subdomain') AND domain = ANY($1::text[])`,
		"{"+strings.Join([]string{root, sub, portHost}, ",")+"}").Scan(&rows); err != nil {
		t.Fatalf("查资产失败: %v", err)
	}
	if rows != 3 {
		t.Fatalf("重复导入产生了重复行：同名资产 %d 条（应 3）", rows)
	}

	// ⑤ 任务关联与来源：导入到任务时要能在任务资产里查到，且来源如实标 csv。
	// 子域的行是 sub，root_domain 的行是 root（UpsertSubdomain 顺带确保的根域）。
	var source, summary string
	if err := e.m.pg.QueryRow(`
SELECT l.source, l.source_summary FROM task_asset_links l JOIN assets a ON a.id=l.asset_id
WHERE l.task_id=$1 AND a.domain=$2`, tid, sub).Scan(&source, &summary); err != nil {
		t.Fatalf("任务资产关联不存在（导入没进任务的测试资产）：%v", err)
	}
	if source != "csv" {
		t.Errorf("来源该标成 csv，得到 %q", source)
	}
	if !strings.Contains(summary, "CSV") {
		t.Errorf("来源说明该点明 CSV，得到 %q", summary)
	}

	// ⑥ 任务不存在时如实报错，而不是「说成功、任务里什么都没有」。
	badPayload, _ := json.Marshal(map[string]any{"csv": "example.com", "task_id": 1 << 40})
	rec = e.request(http.MethodPost, "/api/assets/import", string(badPayload))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应 404，实际 status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// accP27 战果层：据点/凭据经真实的 worker 工具写回 → graph_overview 可见 → 报告成段。
//
// 端到端才能钉住的两件事：①报告里必须有战果——没有的话，任务打穿了一条链而交付物看不出来，
// 等于白打穿；②**凭据本体绝不进报告**——报告会被下载、被打进 P19 的批量交付包，在里面印
// 可用的凭据就是把战果随交付物一起发走。
func accP27(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p27", "战果层")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)

	intentID, err := task.Store.AddIntent(map[string]any{"summary": "打穿上传点"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intentArg := strconv.FormatInt(intentID, 10)

	foothold := workflowTool(t, tsx.WorkerTools(), "report_foothold")
	res, err := foothold.Call(context.Background(), json.RawMessage(
		`{"summary":"在 web01 上拿到 www-data 的反弹 shell","access":"反弹 shell","privilege":"user",`+
			`"evidence":"nc -lvnp 4444 → connect to 10.0.0.9:47112","confidence":"high","intent_id":`+intentArg+`}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "foothold recorded: ") {
		t.Fatalf("report_foothold: %v %s", err, res.Flatten())
	}

	// 凭据本体用一个不可能偶然撞上的串，好让"报告里有没有它"是个确定性断言。
	const secret = "ACC-P27-SECRET-do-not-print"
	cred := workflowTool(t, tsx.WorkerTools(), "report_credential")
	res, err = cred.Call(context.Background(), json.RawMessage(
		`{"summary":"在 web01 的 wp-config.php 里读到数据库口令","kind":"password","username":"wp_user",`+
			`"secret":"`+secret+`","evidence":"grep DB_PASSWORD wp-config.php","confidence":"high","intent_id":`+intentArg+`}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "credential recorded: ") {
		t.Fatalf("report_credential: %v %s", err, res.Flatten())
	}

	// ① 规划层看得见：计数与窗口都要在 graph_overview 里。
	overview := workflowTool(t, tsx.PlannerTools(), "graph_overview")
	ores, err := overview.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("graph_overview: %v", err)
	}
	flat := ores.Flatten()
	e.mustContain(flat, `"footholds_total":1`, "概览的据点计数")
	e.mustContain(flat, `"credentials_total":1`, "概览的凭据计数")
	e.mustContain(flat, "反弹 shell", "概览里的据点摘要")

	// ② 报告成段，且凭据本体不在里面。
	md := e.report(task)
	e.mustContain(md, "## 战果（已攻陷的通路与取得的凭据）", "战果段")
	e.mustContain(md, "在 web01 上拿到 www-data 的反弹 shell", "据点摘要")
	e.mustContain(md, "wp_user", "凭据身份")
	e.mustContain(md, "当前可用", "战果状态")
	if strings.Contains(md, secret) {
		t.Fatalf("凭据本体进了报告——报告会随交付物外流：\n%s", md)
	}
}

// accP28 走完 P28 的两条链：资产页的 FOFA 检索导入，与存储清理。
//
//	① 检索：假 FOFA（官方结构）→ POST /api/intel/search 出结果 → 进查询台账 → 配额耗尽时被拒；
//	② 导入：结果折成 CSV 走 **P26 同一套** preview/apply → 新增；**再导一次不新增**（只合并）；
//	③ 清理：试算**不动盘** → 执行只删过期项、在跑任务的产物毫发无伤 → traffic 只统计不清理。
func accP28(t *testing.T, e *accEnv) {
	// ── 假 FOFA：官方结构（results 数组 + size） ──────────────────────────────
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/api/v1/search/all" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"error":false,"size":3,"page":1,"fields":["host","ip","port","protocol","domain","title","server"],"results":[
			["https://acc28-a.example.com:8443","10.28.0.1","8443","https","example.com","后台登录","nginx"],
			["http://acc28-b.example.com","10.28.0.2","80","http","example.com","",""],
			["http://acc28-c.example.com","10.28.0.3","8080","http","example.com","Jenkins","Jetty"]
		]}`))
	}))
	t.Cleanup(srv.Close)

	// 设置是全局的：改前记下、跑完恢复（与 accP21 同一纪律）。
	restore := map[string]struct {
		v  string
		ok bool
	}{}
	for _, k := range []string{settingIntelKey, settingIntelBaseURL, settingIntelDailyQuota,
		settingStorageRetentionTasks, settingStorageRetentionSessions} {
		v, ok, _ := e.m.pg.GetSetting(k)
		restore[k] = struct {
			v  string
			ok bool
		}{v, ok}
	}
	t.Cleanup(func() {
		for k, r := range restore {
			if r.ok {
				_ = e.m.pg.SetSetting(k, r.v)
			} else {
				_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, k)
			}
		}
	})
	if rec := e.request(http.MethodPut, "/api/settings",
		fmt.Sprintf(`{"fofa_key":"acc-p28-key","fofa_base_url":%q,"intel_daily_quota":0}`, srv.URL)); rec.Code != 200 {
		t.Fatalf("设置情报源 status=%d body=%s", rec.Code, rec.Body.String())
	}

	// ① 检索：结果 + 台账。
	rec := e.request(http.MethodPost, "/api/intel/search", `{"expr":"domain=\"example.com\"","limit":10}`)
	if rec.Code != 200 {
		t.Fatalf("FOFA 检索 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var search struct {
		Total    int `json:"total"`
		Returned int `json:"returned"`
		Fields   []string
		Assets   []struct {
			Host string `json:"host"`
			IP   string `json:"ip"`
			Port int    `json:"port"`
		} `json:"assets"`
		UsedToday int    `json:"used_today"`
		Spool     string `json:"spool"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &search); err != nil {
		t.Fatalf("检索应答不是合法 JSON: %v", err)
	}
	if search.Total != 3 || search.Returned != 3 || len(search.Assets) != 3 {
		t.Fatalf("应命中 3 条（size=3）：total=%d returned=%d len=%d", search.Total, search.Returned, len(search.Assets))
	}
	// 验收库是共享的（accP21 也查过 FOFA），不能断言"正好 1"——断言"这次确实被计入了"。
	if search.UsedToday < 1 {
		t.Fatalf("本次查询应计入今日用量，实际 %d", search.UsedToday)
	}
	if search.Spool == "" {
		t.Fatal("大结果应落盘并给出路径")
	}
	ledger, err := e.m.pg.ListIntelQueries(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range ledger {
		if row.Provider == "fofa" && strings.Contains(row.Expr, "example.com") && row.Returned == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("查询应进台账：%+v", ledger)
	}

	// ② 导入：折成 CSV 走 P26 同一套；再导一次**不新增**。
	// FOFA 的 host 字段常常就是**带协议的 URL**：导入器的列语义是死的（host 列只认 host、
	// url 列只认 URL），所以按值的形状分列——URL 进 url 列，其余进 host 列，且只在无端口时补端口。
	// 前端弹窗同一口径（同一个坑，测试先踩到）。
	csvLines := []string{"host,url"}
	for _, a := range search.Assets {
		v := a.Host
		if a.Port > 0 && !strings.Contains(v, ":") {
			v = fmt.Sprintf("%s:%d", v, a.Port)
		}
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			csvLines = append(csvLines, ","+v)
		} else {
			csvLines = append(csvLines, v+",")
		}
	}
	csv := strings.Join(csvLines, "\n")
	preview := e.request(http.MethodPost, "/api/assets/import/preview", fmt.Sprintf(`{"csv":%q}`, csv))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"total":3`) {
		t.Fatalf("导入预览应识别 3 行: status=%d body=%s", preview.Code, preview.Body.String())
	}
	type importResult struct {
		Created int `json:"created"`
		Merged  int `json:"merged"`
	}
	var first importResult
	rec = e.request(http.MethodPost, "/api/assets/import", fmt.Sprintf(`{"csv":%q}`, csv))
	if rec.Code != 200 {
		t.Fatalf("导入 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Created == 0 {
		t.Fatalf("首次导入应有新增：%+v", first)
	}
	var second importResult
	rec = e.request(http.MethodPost, "/api/assets/import", fmt.Sprintf(`{"csv":%q}`, csv))
	if rec.Code != 200 {
		t.Fatalf("二次导入 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.Created != 0 || second.Merged == 0 {
		t.Fatalf("二次导入应只合并不新增：%+v", second)
	}
	t.Cleanup(func() { _, _ = e.m.pg.Exec(`DELETE FROM assets WHERE domain LIKE 'acc28-%'`) })

	// ①b 配额闸门：把上限设成已用量，下一次查询必须被拒并说明原因。
	if rec := e.request(http.MethodPut, "/api/settings", `{"intel_daily_quota":1}`); rec.Code != 200 {
		t.Fatalf("设配额 status=%d", rec.Code)
	}
	rec = e.request(http.MethodPost, "/api/intel/search", `{"expr":"domain=\"example.com\""}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "每日上限") {
		t.Fatalf("配额耗尽应 400 且说明：status=%d body=%s", rec.Code, rec.Body.String())
	}

	// ── ③ 存储清理 ──────────────────────────────────────────────────────────
	// 一个在跑的任务（受保护）+ 一个"过期"的假任务目录。
	task, tid := e.newTask("acceptance-p28-storage", "存储清理")
	_ = task
	liveDir := filepath.Join(e.m.dir, "tasks", strconv.FormatInt(tid, 10))
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "keep.txt"), []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(e.m.dir, "tasks", "28-000-old")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(oldDir, "old.log")
	if err := os.WriteFile(oldFile, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(oldFile, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(oldDir) })

	if rec := e.request(http.MethodPut, "/api/settings", `{"storage_retention_tasks_days":30}`); rec.Code != 200 {
		t.Fatalf("设保留天数 status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = e.request(http.MethodGet, "/api/storage/report", "")
	if rec.Code != 200 {
		t.Fatalf("试算 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var inspect struct {
		Report struct {
			Categories []struct {
				Key           string `json:"key"`
				StatsOnly     string `json:"stats_only"`
				RetentionDays int    `json:"retention_days"`
				ExpiredUnits  int    `json:"expired_units"`
				Protected     int    `json:"protected_units"`
			} `json:"categories"`
		} `json:"report"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inspect); err != nil {
		t.Fatal(err)
	}
	var tasksCat, trafficCat *struct {
		Key           string `json:"key"`
		StatsOnly     string `json:"stats_only"`
		RetentionDays int    `json:"retention_days"`
		ExpiredUnits  int    `json:"expired_units"`
		Protected     int    `json:"protected_units"`
	}
	for i := range inspect.Report.Categories {
		switch inspect.Report.Categories[i].Key {
		case "tasks":
			tasksCat = &inspect.Report.Categories[i]
		case "traffic":
			trafficCat = &inspect.Report.Categories[i]
		}
	}
	if tasksCat == nil || tasksCat.RetentionDays != 30 || tasksCat.ExpiredUnits < 1 || tasksCat.Protected < 1 {
		t.Fatalf("试算应显示 30 天保留、至少 1 项过期、至少 1 项受保护：%+v", tasksCat)
	}
	if trafficCat == nil || trafficCat.StatsOnly == "" {
		t.Fatalf("流量分类必须如实标成只统计不清理：%+v", trafficCat)
	}
	// 试算不动盘。
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("试算不该删任何东西：%v", err)
	}

	rec = e.request(http.MethodPost, "/api/storage/clean", `{"categories":["tasks"]}`)
	if rec.Code != 200 {
		t.Fatalf("清理 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var cleanRes struct {
		Deleted []struct {
			Category string `json:"category"`
			Name     string `json:"name"`
		} `json:"deleted"`
		Failures []string `json:"failures"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cleanRes); err != nil {
		t.Fatal(err)
	}
	if len(cleanRes.Deleted) < 1 {
		t.Fatalf("应至少删掉那一个过期目录：%+v（失败 %v）", cleanRes.Deleted, cleanRes.Failures)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("过期目录应已删除：%v", err)
	}
	if _, err := os.Stat(filepath.Join(liveDir, "keep.txt")); err != nil {
		t.Fatalf("在跑任务的产物必须毫发无伤：%v", err)
	}
}

// accP29 战果轴：意图声明「我在动用哪条战果」→ graph_overview 的 outstanding 归零 → 新边经
// HTTP 到达前端图的边表。钉的是「哪些能力还没被接管」从提示词散文变成可核对状态这件事。
//
// 最后一步（删掉那条意图）守的是一个设计选择：uses 当初若复用 derived_from，删除时会沿边做
// 独占级联，把据点本身一起带走——战果是证据级数据，不能挂在会被级联的关系上。
func accP29(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p29", "战果动用")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)

	nodeID := func(out string) int64 {
		t.Helper()
		line, _, _ := strings.Cut(out, "\n")
		_, raw, _ := strings.Cut(line, ": ")
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			t.Fatalf("回执里没有节点 id：%q", out)
		}
		return id
	}
	overview := workflowTool(t, tsx.PlannerTools(), "graph_overview")
	overviewOut := func() string {
		t.Helper()
		res, err := overview.Call(context.Background(), json.RawMessage(`{}`), nil)
		if err != nil {
			t.Fatalf("graph_overview: %v", err)
		}
		return res.Flatten()
	}

	res, err := workflowTool(t, tsx.WorkerTools(), "report_foothold").Call(context.Background(), json.RawMessage(
		`{"summary":"在 web01 上拿到 root 的反弹 shell","access":"反弹 shell","privilege":"root",`+
			`"evidence":"nc -lvnp 4444 → connect from 10.0.0.9","confidence":"high"}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "foothold recorded: ") {
		t.Fatalf("report_foothold: %v %s", err, res.Flatten())
	}
	footholdID := nodeID(res.Flatten())

	// ① 到手但还没人动用 → outstanding
	e.mustContain(overviewOut(), `"outstanding_loot_total":1`, "刚到手的据点该是 outstanding")

	// ② 派一条意图动用它
	res, err = workflowTool(t, tsx.PlannerTools(), "add_intent").Call(context.Background(), json.RawMessage(
		`{"summary":"以 web01 为跳板看内网还有什么","uses_loot_ids":[`+strconv.FormatInt(footholdID, 10)+`]}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "intent created: ") {
		t.Fatalf("add_intent（带 uses_loot_ids）: %v %s", err, res.Flatten())
	}
	intentID := nodeID(res.Flatten())

	// ③ 归零
	e.mustContain(overviewOut(), `"outstanding_loot_total":0`, "已被动用的据点不该还算 outstanding")

	// ③b 报告里讲清「拿它走到了哪一步」。只列「拿到了什么」是资产清单，写清动用关系才是作业叙述。
	e.mustContain(e.report(task), "动用于：intent-", "报告里的战果动用关系")

	// ④ 新边经 HTTP 到达前端图的边表（前端按 rel 上色/标注，认不出的会退化成裸 rel 串）
	rec := e.request(http.MethodGet, "/api/exploration/graph?task="+task.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("exploration/graph status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), `"rel":"uses"`, "uses 边")

	// ⑤ 删掉那条意图：据点必须活下来，且重新变成 outstanding
	if _, err := task.Store.CancelIntent(intentID); err != nil {
		t.Fatalf("取消意图: %v", err)
	}
	if nodes, err := task.Store.ListByKind(db.KindFoothold, 10); err != nil || len(nodes) != 1 {
		t.Fatalf("删掉调用它的意图把据点一起带走了（战果是证据级数据，不能被级联）：%v err=%v", nodes, err)
	}
	e.mustContain(overviewOut(), `"outstanding_loot_total":1`, "用过它的意图没了，据点该重新算 outstanding")
	// 报告也跟着回到「尚未动用」——两边口径必须一致，否则交付物会讲一个图里不成立的故事。
	e.mustContain(e.report(task), "尚未动用", "取消后报告该说它尚未动用")
}

// accP30 攻击路径：把 uses/yields 两种边接成一条读得出来的路，并进报告。
//
// 兑现的是分析里那句「报告里的提权/横向只能靠文案」：这条路径**不是另写的叙述**，是从图里
// 已经记着的边重建出来的——所以它必然与图一致，图变了它跟着变。
func accP30(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p30", "攻击路径")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)

	nodeID := func(out string) int64 {
		t.Helper()
		line, _, _ := strings.Cut(out, "\n")
		_, raw, _ := strings.Cut(line, ": ")
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			t.Fatalf("回执里没有节点 id：%q", out)
		}
		return id
	}
	// worker 面与 planner 面分开取：战果是 worker 写的，意图只有 planner 能派。
	callOn := func(face []actool.CoreTool, toolName, in string) string {
		t.Helper()
		res, err := workflowTool(t, face, toolName).Call(context.Background(), json.RawMessage(in), nil)
		if err != nil {
			t.Fatalf("%s: %v", toolName, err)
		}
		return res.Flatten()
	}
	worker := tsx.WorkerTools()
	planner := tsx.PlannerTools()

	// 第一步：打下 web01，再用它去读配置。
	fhA := nodeID(callOn(worker, "report_foothold",
		`{"summary":"在 web01 上拿到 root 的反弹 shell","access":"反弹 shell","privilege":"root",`+
			`"evidence":"nc -lvnp 4444 → connect from 10.0.0.9","confidence":"high"}`))
	i1Out := callOn(planner, "add_intent",
		`{"summary":"读 wp-config 找数据库口令","uses_loot_ids":[`+strconv.FormatInt(fhA, 10)+`],`+
			`"techniques":["T1552.001"]}`)
	if !strings.HasPrefix(i1Out, "intent created: ") {
		t.Fatalf("第一步意图没建上：%s", i1Out)
	}
	i1 := nodeID(i1Out)
	callOn(worker, "report_credential",
		`{"summary":"wp_user 口令","kind":"password","username":"wp_user","secret":"s",`+
			`"evidence":"grep DB_PASSWORD wp-config.php","confidence":"high","intent_id":`+strconv.FormatInt(i1, 10)+`}`)

	// 第二步：用凭据横到 db01。凭据节点 id 从上一步的产出去取（它是 report_credential 建的，
	// 回执里那段 JSON 不必再解一遍）。
	creds, err := task.Store.ListByKind(db.KindCredential, 10)
	if err != nil || len(creds) != 1 {
		t.Fatalf("该正好有一条凭据，得到 %d 条 (err=%v)", len(creds), err)
	}
	credID := creds[0].ID
	i2Out := callOn(planner, "add_intent",
		`{"summary":"用 wp_user 横向到 db01","uses_loot_ids":[`+strconv.FormatInt(credID, 10)+`],`+
			`"techniques":["T1021.004","t1021.004"]}`)
	if !strings.HasPrefix(i2Out, "intent created: ") {
		t.Fatalf("第二步意图没建上：%s", i2Out)
	}
	i2 := nodeID(i2Out)
	callOn(worker, "report_foothold",
		`{"summary":"在 db01 上拿到 root 的 shell","access":"SSH","privilege":"root",`+
			`"evidence":"ssh root@db01 成功","confidence":"high","intent_id":`+strconv.FormatInt(i2, 10)+`}`)

	md := e.report(task)
	e.mustContain(md, "## 攻击路径（从已得的访问展开）", "攻击路径段")
	e.mustContain(md, "起点 据点「在 web01 上拿到 root 的反弹 shell」", "路径起点")
	e.mustContain(md, fmt.Sprintf("intent-%d「读 wp-config 找数据库口令」 [T1552.001] → 得到：凭据「wp_user 口令」", i1), "第一跳（含技术标注）")
	e.mustContain(md, fmt.Sprintf("intent-%d「用 wp_user 横向到 db01」 [T1021.004] → 得到：据点「在 db01 上拿到 root 的 shell」", i2), "第二跳（含技术标注）")

	// ATT&CK：交付物按技术组织，客户要的是「你打了哪些 TTP」。
	e.mustContain(md, "## 技术覆盖（ATT&CK）", "技术覆盖段")
	e.mustContain(md, "- T1552.001 —— 1 条意图", "凭据文件技术")
	e.mustContain(md, "- T1021.004 —— 1 条意图", "远程服务技术（大小写重复项该被归一成一条）")
}

// accP31 检测验证：worker 记下一次「这一手有没有被检出」→ 概览分态计数 → 报告成段。
//
// 钉住的是三态**不能合成一个「检出率」**：把 unknown 混进分母，就是把「我们没看到」读成
// 「它没拦住」——那会给出一个比不给更危险的结论。这也是本仓证据门一贯的口径。
func accP31(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p31", "检测验证")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)

	check := workflowTool(t, tsx.WorkerTools(), "report_detection_check")
	for _, c := range []struct{ result, summary string }{
		{"triggered", "编码命令被 EDR 抓到"},
		{"not_triggered", "读 /etc/shadow 无任何告警"},
		{"unknown", "日志服务当晚挂了，无从判定"},
	} {
		res, err := check.Call(context.Background(), json.RawMessage(
			`{"summary":"`+c.summary+`","technique":"T1059.001","result":"`+c.result+`",`+
				`"evidence":"查了 sysmon","confidence":"high"}`), nil)
		if err != nil || !strings.HasPrefix(res.Flatten(), "detection check recorded: ") {
			t.Fatalf("%s 没记上：%v %s", c.result, err, res.Flatten())
		}
	}

	// ① 概览：三态分别计数，不合成检出率。
	res, err := workflowTool(t, tsx.PlannerTools(), "graph_overview").Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("graph_overview: %v", err)
	}
	flat := res.Flatten()
	e.mustContain(flat, `"detections_total":3`, "检测记录总数")
	e.mustContain(flat, `"triggered":1`, "已检出计数")
	e.mustContain(flat, `"not_triggered":1`, "未检出计数")
	e.mustContain(flat, `"unknown":1`, "无法判定计数")

	// ② 报告：三态各自成词 + 那条防误读的脚注。
	md := e.report(task)
	e.mustContain(md, "## 检测验证（这一手有没有被检出）", "检测验证段")
	e.mustContain(md, "—— 已检出：编码命令被 EDR 抓到", "已检出条")
	e.mustContain(md, "—— 未检出：读 /etc/shadow 无任何告警", "未检出条")
	e.mustContain(md, "—— 无法判定：日志服务当晚挂了，无从判定", "无法判定条")
	e.mustContain(md, "不能读成防御没拦住", "防误读脚注")
}

// accP32 授权范围设置（页面可配）：空间门模式 + 作业时间窗经 API 存取，写错要当场退回。
//
// 特别钉住「写错不改动已存的值」：若先落库再校验，一条解析不了的窗口会留在库里，之后
// **每一次**工具调用都会落到人工确认上（CheckScope 对无法解析的窗口如实 ask），而用户
// 多半要等任务跑不动了才发现。
func accP32(t *testing.T, e *accEnv) {
	// 两个键可能本就有值（别的用例设过），跑完还原。
	for _, key := range []string{intercept.SettingScopeMode, intercept.SettingScopeHours} {
		old, had, err := e.m.pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if had {
				_ = e.m.pg.SetSetting(key, old)
				return
			}
			_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
		})
	}

	// ① 正常写入并读回。
	rec := e.request(http.MethodPut, "/api/settings",
		`{"intercept_scope_mode":"deny","intercept_scope_hours":"09:00-18:00"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存授权范围 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), `"intercept_scope_hours":"09:00-18:00"`, "读回的作业时间窗")
	e.mustContain(rec.Body.String(), `"intercept_scope_mode":"deny"`, "读回的越界处置")

	// ② 写错的窗口要被当场退回，且**不改动已存的值**。
	for _, bad := range []string{"9点到6点", "09:00", "09:00-09:00"} {
		rec = e.request(http.MethodPut, "/api/settings", `{"intercept_scope_hours":"`+bad+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("窗口 %q 该被拒（400），得到 %d：%s", bad, rec.Code, rec.Body.String())
		}
		got, _, _ := e.m.pg.GetSetting(intercept.SettingScopeHours)
		if got != "09:00-18:00" {
			t.Fatalf("写 %q 被拒后，已存的窗口被改动了：%q", bad, got)
		}
	}

	// ③ 模式取值也要挡（否则拦截层会静默回落到默认 ask，用户以为设成了 deny）。
	rec = e.request(http.MethodPut, "/api/settings", `{"intercept_scope_mode":"block"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法模式该被拒（400），得到 %d：%s", rec.Code, rec.Body.String())
	}
	rec = e.request(http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("读取设置 status=%d", rec.Code)
	}
	e.mustContain(rec.Body.String(), `"intercept_scope_hours":"09:00-18:00"`, "拒绝非法模式后窗口仍在")

	// ④ 清空窗口 = 不限时（合法的"关掉时间维度"写法）。
	rec = e.request(http.MethodPut, "/api/settings", `{"intercept_scope_hours":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("清空窗口该被接受，得到 %d：%s", rec.Code, rec.Body.String())
	}
}

// accP33 战役层：任务之上的归属与聚合。
//
// 单个任务只管自己打穿了什么；一次作业要看的是"这轮总共拿到了什么"。这里钉住跨任务归属与
// 数字，以及**删战役只解绑、不删任务**——任务是被归档与交付的单位，不该因为"把作业归错了组"消失。
func accP33(t *testing.T, e *accEnv) {
	_, tid1 := e.newTask("acceptance-p33-a", "战役里的第一个任务")
	_, tid2 := e.newTask("acceptance-p33-b", "战役里的第二个任务")
	sid := func(id int64) string { return strconv.FormatInt(id, 10) }

	rec := e.request(http.MethodPost, "/api/campaigns", `{"name":"acc-p33 作业","note":"验收用"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("建战役 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var c struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || c.ID == 0 {
		t.Fatalf("建战役回执解析失败：%v %s", err, rec.Body.String())
	}
	t.Cleanup(func() { _, _ = e.m.pg.Exec(`DELETE FROM campaigns WHERE id=$1`, c.ID) })

	// ① 判重看归一化后的名字：多打空格、换大小写都算同一个作业。
	rec = e.request(http.MethodPost, "/api/campaigns", `{"name":"  ACC-P33   作业 "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("同名（归一化后）战役该被拒 400，得到 %d：%s", rec.Code, rec.Body.String())
	}

	// ② 声明式归属：一次给出应包含的全部任务。
	rec = e.request(http.MethodPut, "/api/campaigns/"+sid(c.ID)+"/tasks",
		`{"task_ids":[`+sid(tid1)+`,`+sid(tid2)+`]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("归属 status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = e.request(http.MethodGet, "/api/campaigns/"+sid(c.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("汇总 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), `"tasks":2`, "汇总里的任务总数")

	// ③ 多退少补：只留一个，另一个自动解绑。
	rec = e.request(http.MethodPut, "/api/campaigns/"+sid(c.ID)+"/tasks", `{"task_ids":[`+sid(tid1)+`]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("重新归属 status=%d body=%s", rec.Code, rec.Body.String())
	}
	// removed 在 PUT 的回执里（不是汇总里）。
	e.mustContain(rec.Body.String(), `"removed":[`+sid(tid2)+`]`, "被解绑的那个任务")
	rec = e.request(http.MethodGet, "/api/campaigns/"+sid(c.ID), "")
	e.mustContain(rec.Body.String(), `"tasks":1`, "多退少补后只剩一个")

	// ④ 删战役：只解绑，任务本身还在。
	rec = e.request(http.MethodDelete, "/api/campaigns/"+sid(c.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("删战役 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), `"tasks_unbound":1`, "解绑数")
	if task, err := e.m.pg.GetTask(tid1); err != nil || task == nil {
		t.Fatalf("删战役把任务一起删了（任务是被归档与交付的单位，不该消失）：%v err=%v", task, err)
	}
}

// accP34 AD 攻击路径导入（BloodHound/SharpHound → 图 → 路径）：
// 预览一个字节都不写、应用后路径真能读出来（含「机器上有人家的会话」那一跳）、
// 重导只合并不新增、坏输入当场退回。
//
// 这条验收钉的是「域内路径不是另写的一段文案」：页面读的 /api/tasks/{id}/ad-paths 与报告里的
// AD 段出自同一份实现，所以只要图对了，两边必然一致。
func accP34(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p34", "AD 攻击路径")

	// 一套最小的域：普通账号 jdoe 是 IT Support 成员；IT Support 是 WS01 的本机管理员；
	// DA 在 WS01 上有会话；DA 属于 Domain Admins。这正是红队最常走的那条路。
	users := `[{
	  "SID":"S-1-5-21-77-88-99-1105","ObjectIdentifier":"S-1-5-21-77-88-99-1105",
	  "MemberOf":["CN=IT Support,CN=Users,DC=lab,DC=local"],
	  "Aces":[{"PrincipalSID":"S-1-5-21-77-88-99-512","PrincipalType":"Group","RightName":"ReadProperty"}],
	  "Properties":{"name":"JDOE@LAB.LOCAL","samaccountname":"jdoe","domain":"LAB.LOCAL",
	    "distinguishedname":"CN=John Doe,CN=Users,DC=lab,DC=local","enabled":true,"hasspn":true}
	},{
	  "SID":"S-1-5-21-77-88-99-1106","ObjectIdentifier":"S-1-5-21-77-88-99-1106",
	  "Properties":{"name":"DA@LAB.LOCAL","samaccountname":"da","domain":"LAB.LOCAL",
	    "distinguishedname":"CN=Domain Admin,CN=Users,DC=lab,DC=local","enabled":true,"admincount":true}
	}]`
	groups := `[{
	  "SID":"S-1-5-21-77-88-99-2001","ObjectIdentifier":"S-1-5-21-77-88-99-2001",
	  "Members":[{"ObjectIdentifier":"S-1-5-21-77-88-99-1105","ObjectType":"User"}],
	  "Properties":{"name":"IT SUPPORT@LAB.LOCAL","samaccountname":"it support","domain":"LAB.LOCAL",
	    "distinguishedname":"CN=IT Support,CN=Users,DC=lab,DC=local"}
	},{
	  "SID":"S-1-5-21-77-88-99-512","ObjectIdentifier":"S-1-5-21-77-88-99-512",
	  "Members":[{"ObjectIdentifier":"S-1-5-21-77-88-99-1106","ObjectType":"User"}],
	  "Properties":{"name":"DOMAIN ADMINS@LAB.LOCAL","samaccountname":"domain admins","domain":"LAB.LOCAL",
	    "distinguishedname":"CN=Domain Admins,CN=Users,DC=lab,DC=local","admincount":true}
	}]`
	computers := `[{
	  "SID":"S-1-5-21-77-88-99-3001","ObjectIdentifier":"S-1-5-21-77-88-99-3001",
	  "Sessions":{"S-1-5-21-77-88-99-1106":["WS01"]},
	  "LocalAdmins":{"Collected":true,"Results":[{"ObjectIdentifier":"S-1-5-21-77-88-99-2001","ObjectType":"Group"}]},
	  "RemoteDesktopUsers":{"Collected":true,"Results":[{"ObjectIdentifier":"S-1-5-21-77-88-99-2001","ObjectType":"Group"}]},
	  "Properties":{"name":"WS01.LAB.LOCAL","samaccountname":"WS01$","operatingsystem":"Windows 10",
	    "domain":"LAB.LOCAL","distinguishedname":"CN=WS01,CN=Computers,DC=lab,DC=local","enabled":true}
	}]`
	domains := `{"meta":{"type":"domains"},"data":[{
	  "SID":"S-1-5-21-77-88-99","ObjectIdentifier":"S-1-5-21-77-88-99",
	  "Trusts":[{"TargetDomainSid":"S-1-5-21-900-900-900","TargetDomainName":"OTHER.LOCAL",
	             "TrustDirection":"Bidirectional","TrustType":"External","IsTransitive":true}],
	  "Properties":{"name":"LAB.LOCAL","domain":"LAB.LOCAL","distinguishedname":"DC=lab,DC=local","highvalue":true}
	}]`

	files := func() string {
		b, _ := json.Marshal(map[string]any{"task_id": tid, "files": []map[string]string{
			{"name": "users.json", "json": users},
			{"name": "groups.json", "json": groups},
			{"name": "computers.json", "json": computers},
			{"name": "domains.json", "json": domains},
		}})
		return string(b)
	}

	// ① 预览：只读，且把「会导入什么」说清楚（对象/边分类计数、高价值清单、跳过的权限）。
	rec := e.request(http.MethodPost, "/api/ad/import/preview", files())
	if rec.Code != http.StatusOK {
		t.Fatalf("预览 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Objects   map[string]int `json:"objects"`
		Edges     map[string]int `json:"edges"`
		NodesNew  int            `json:"nodes_new"`
		HighValue []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"highvalue"`
		SkippedRights map[string]int `json:"skipped_rights"`
		Warnings      []string       `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("预览解码失败：%v raw=%s", err, rec.Body.String())
	}
	if preview.NodesNew == 0 {
		t.Fatalf("预览该报出会新增节点：%s", rec.Body.String())
	}
	if preview.Objects["user"] != 2 || preview.Objects["group"] != 2 || preview.Objects["computer"] != 1 {
		t.Errorf("对象分类计数不对：%v", preview.Objects)
	}
	if preview.Edges["member_of"] == 0 || preview.Edges["admin_to"] == 0 || preview.Edges["session_on"] == 0 {
		t.Errorf("三类关系都该有边：%v", preview.Edges)
	}
	if preview.SkippedRights["ReadProperty"] != 1 {
		t.Errorf("白名单外的 ACE 该如实报出：%v", preview.SkippedRights)
	}
	foundDA := false
	for _, h := range preview.HighValue {
		if strings.Contains(h.Name, "DOMAIN ADMINS") {
			foundDA = true
		}
	}
	if !foundDA {
		t.Errorf("高价值清单里该有 Domain Admins：%v", preview.HighValue)
	}

	// ② 预览必须只读。
	if nodes, err := task.Store.ListByKind(db.KindADObject, 10); err != nil || len(nodes) != 0 {
		t.Fatalf("预览写了库：%d 个 AD 节点 (err=%v)", len(nodes), err)
	}

	// ③ 应用。
	rec = e.request(http.MethodPost, "/api/ad/import", files())
	if rec.Code != http.StatusOK {
		t.Fatalf("应用 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var applied struct {
		NodesCreated int `json:"nodes_created"`
		NodesUpdated int `json:"nodes_updated"`
		EdgesCreated int `json:"edges_created"`
		Failures     int `json:"failures"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil {
		t.Fatalf("应用解码失败：%v raw=%s", err, rec.Body.String())
	}
	if applied.NodesCreated == 0 || applied.EdgesCreated == 0 || applied.Failures != 0 {
		t.Fatalf("应用结果不对：%s", rec.Body.String())
	}

	// ④ 路径：页面读的那条接口。必须能读出 jdoe → … → Domain Admins，
	//    且中间那一跳是**机器上的会话**（反着走的那条）。
	rec = e.request(http.MethodGet, "/api/tasks/"+task.ID+"/ad-paths", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("路径 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var pathsResp struct {
		Paths []struct {
			Start  string `json:"start"`
			Target string `json:"target"`
			Steps  []struct {
				From string `json:"from"`
				Rel  string `json:"rel"`
				To   string `json:"to"`
				Note string `json:"note"`
			} `json:"steps"`
		} `json:"paths"`
		Stats map[string]any `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pathsResp); err != nil {
		t.Fatalf("路径解码失败：%v raw=%s", err, rec.Body.String())
	}
	var daPath []string
	for _, p := range pathsResp.Paths {
		if strings.Contains(p.Target, "DOMAIN ADMINS") {
			for _, s := range p.Steps {
				daPath = append(daPath, s.Rel)
			}
			if !strings.Contains(p.Start, "JDOE") {
				t.Errorf("到 DA 的路该从普通账号 JDOE 出发，实际 %q", p.Start)
			}
		}
	}
	if got := strings.Join(daPath, ">"); got != "member_of>admin_to>session_on>member_of" {
		t.Fatalf("到 Domain Admins 的跳序不对：%q（路径=%s）", got, rec.Body.String())
	}

	// ⑤ 报告：AD 段与页面同源，报告里必须有，且写清「存在不等于已实施」。
	md := e.report(task)
	e.mustContain(md, "攻击路径（域内/云上可达", "报告该有攻击路径段")
	e.mustContain(md, "不代表已实施", "报告该说明这些路是推导出来的")

	// ⑥ 幂等：同一份采集再导一次，只合并、不新增。
	rec = e.request(http.MethodPost, "/api/ad/import", files())
	if rec.Code != http.StatusOK {
		t.Fatalf("重导 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var again struct {
		NodesCreated int `json:"nodes_created"`
		NodesUpdated int `json:"nodes_updated"`
		EdgesCreated int `json:"edges_created"`
		EdgesMerged  int `json:"edges_merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatalf("重导解码失败：%v", err)
	}
	if again.NodesCreated != 0 || again.EdgesCreated != 0 {
		t.Fatalf("重导该只合并不新增：%s", rec.Body.String())
	}
	if again.NodesUpdated == 0 || again.EdgesMerged == 0 {
		t.Errorf("重导该报出合并数：%s", rec.Body.String())
	}

	// ⑦ 坏输入当场退回：不是采集结果 / 没给任务 / 任务不存在。
	bad, _ := json.Marshal(map[string]any{"task_id": tid, "files": []map[string]string{{"name": "x.json", "json": "not json"}}})
	if rec := e.request(http.MethodPost, "/api/ad/import/preview", string(bad)); rec.Code != http.StatusOK {
		t.Fatalf("坏文件该以 200 返回「解析失败」的预览（文件级错误不炸整个请求）：%d %s", rec.Code, rec.Body.String())
	}
	noTask, _ := json.Marshal(map[string]any{"files": []map[string]string{{"name": "u.json", "json": users}}})
	if rec := e.request(http.MethodPost, "/api/ad/import", string(noTask)); rec.Code != 400 {
		t.Errorf("没给 task_id 该 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	ghost, _ := json.Marshal(map[string]any{"task_id": tid + 999999, "files": []map[string]string{{"name": "u.json", "json": users}}})
	if rec := e.request(http.MethodPost, "/api/ad/import", string(ghost)); rec.Code != 404 {
		t.Errorf("任务不存在该 404，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// taskCount 数当前库里的任务条数（不含已删除）——用于断言"这一 tick 有没有起新工作"。
func (e *accEnv) taskCount() int {
	e.t.Helper()
	var n int
	if err := e.m.pg.QueryRow(`SELECT count(*) FROM tasks WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		e.t.Fatalf("count tasks: %v", err)
	}
	return n
}

// accP35 停机开关与作业时间窗的**执行侧**：到点/停机时把整个平台停下来。
//
// 钉住的是"三层一起才算停住"：任务层（暂停 + 取消在跑的 run）、入口层（不补位）、
// 调用层（动作类工具被拒）。以及两条刻意的行为选择：
//
//	· 窗口与 scope_mode 解耦——off 只关空间判定，不关时间；
//	· 恢复一律手动——解除停机不会顺手把任务放起来。
func accP35(t *testing.T, e *accEnv) {
	for _, key := range []string{
		intercept.SettingKillSwitch, intercept.SettingScopeHours, settingScopeWindowState,
	} {
		old, had, err := e.m.pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if had {
				_ = e.m.pg.SetSetting(key, old)
				return
			}
			_, _ = e.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
		})
	}

	task, tid := e.newTask("acceptance-p35", "停机开关与作业时间窗")
	taskID := task.ID

	pausedNow := func() bool {
		cur, ok := e.m.Task(taskID)
		return ok && cur.lifecycleSnapshot().Paused
	}

	// ① 作业时间窗：先设成"现在在窗口内"跑一遍（让状态进入 in_window），
	//    再设成"现在不在"的时段 → 执行侧到点就把在跑的任务停下来。
	now := time.Now()
	open := fmt.Sprintf("%02d:%02d-%02d:%02d",
		(now.Hour()+23)%24, now.Minute(), (now.Hour()+1)%24, now.Minute())
	closed := fmt.Sprintf("%02d:%02d-%02d:%02d",
		(now.Hour()+2)%24, now.Minute(), (now.Hour()+3)%24, now.Minute())
	if err := e.m.pg.SetSetting(intercept.SettingScopeHours, open); err != nil {
		t.Fatal(err)
	}
	e.s.enforceScopeEnforcement()
	raw0, _, _ := e.m.pg.GetSetting(settingScopeWindowState)
	if !strings.Contains(raw0, `"in_window":true`) {
		t.Fatalf("窗口内该记为 in_window：%q", raw0)
	}
	if err := e.m.pg.SetSetting(intercept.SettingScopeHours, closed); err != nil {
		t.Fatal(err)
	}
	e.s.enforceScopeEnforcement()
	if !pausedNow() {
		t.Fatalf("窗口外该把任务停下来（窗口 %s，本机 %s）", closed, now.Format("15:04"))
	}
	var st scopeWindowState
	raw, _, _ := e.m.pg.GetSetting(settingScopeWindowState)
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("窗口状态该落库：%v raw=%q", err, raw)
	}
	if st.InWindow || st.LastClosedAt == "" || st.PausedNow == 0 {
		t.Errorf("窗口状态不对：%+v", st)
	}
	// 任务时间线上要有一句"为什么停的"——恢复后回头看，这是唯一能解释中断的东西。
	acts, _, err := task.Store.ActivityList(nil, 0, 200)
	if err != nil {
		t.Fatalf("读时间线失败：%v", err)
	}
	foundReason := false
	for _, a := range acts {
		if strings.Contains(a.Summary, "作业时间窗") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("暂停时该在任务时间线留一句原因：%+v", acts)
	}

	// ② 窗口清空（=不限时）+ 手动继续 → 恢复；**不自动恢复**是刻意的。
	if err := e.m.pg.SetSetting(intercept.SettingScopeHours, ""); err != nil {
		t.Fatal(err)
	}
	e.s.enforceScopeEnforcement()
	if !pausedNow() {
		t.Error("窗口重开不该自动恢复任务（恢复要人点一下）")
	}
	rec := e.request(http.MethodPost, "/api/scope/resume-all", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("继续全部 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if pausedNow() {
		t.Error("手动继续后任务该恢复")
	}

	// ③ 停机开关：一条 API 就把平台停下来——立刻（不等 tick）、有回执、动作被拒。
	rec = e.request(http.MethodPost, "/api/scope/kill-switch", `{"on":true,"reason":"验收：现场叫停"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("停机 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), "现场叫停", "回执里的停机理由")
	e.mustContain(rec.Body.String(), `"tasks_paused":1`, "回执里被暂停的任务数")
	if !pausedNow() {
		t.Fatal("停机该立刻暂停在跑的任务")
	}
	if !e.s.safetyAdmissionBlocked() {
		t.Error("停机期间入口层该拦住新任务补位")
	}
	// 调用层：动作类工具被硬拒（scope_mode 是 off 也拦——停机不是授权范围的一档）。
	dec, hit := e.m.interceptor.CheckScope(context.Background(), tid, "Bash",
		json.RawMessage(`{"command":"nmap 10.0.0.1"}`))
	if !hit || dec.Action != "deny" || !strings.Contains(dec.RuleName, "停机开关") {
		t.Errorf("停机期间动作该被硬拒：hit=%v action=%q rule=%q", hit, dec.Action, dec.RuleName)
	}

	// ④ 停止状态下"继续全部"要被挡住并说清为什么（否则用户会以为按钮坏了）。
	rec = e.request(http.MethodPost, "/api/scope/resume-all", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("停机期间继续全部该 409，得到 %d：%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), "停机", "409 的理由")

	// ⑤ 解除：回执历史两条（开、关），**不自动恢复**任务。
	rec = e.request(http.MethodPost, "/api/scope/kill-switch", `{"on":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("解除 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !pausedNow() {
		t.Error("解除停机不该自动恢复任务（恢复要人点一下）")
	}
	var status scopeEnforcementResponse
	rec = e.request(http.MethodGet, "/api/scope/enforcement", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("状态解码失败：%v", err)
	}
	if status.KillSwitch.On {
		t.Error("解除后开关该是关的")
	}
	if len(status.KillSwitch.History) != 2 || !status.KillSwitch.History[0].On == false {
		t.Errorf("回执历史该有开、关两条（新的在前）：%+v", status.KillSwitch.History)
	}
	if status.KillSwitch.History[0].By == "" {
		t.Error("回执该记下是谁发起的")
	}
	rec = e.request(http.MethodPost, "/api/scope/resume-all", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("解除后继续全部 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), `"resumed":1`, "恢复的任务数")
	if pausedNow() {
		t.Error("继续全部之后任务该在跑")
	}

	// ⑥ 幂等：重复开/关要被拒（400），不产生一条假回执。
	if rec := e.request(http.MethodPost, "/api/scope/kill-switch", `{"on":false}`); rec.Code != http.StatusBadRequest {
		t.Errorf("开关本来就是关的，再关该 400，得到 %d：%s", rec.Code, rec.Body.String())
	}

	// ⑦ 停止状态下**不起任何新工作**：到点的计划任务不跑（否则一个 5 分钟一次的计划
	//    在停机一小时里会堆出十二条跑不动的任务）。解除后这一 tick 立刻补上——
	//    水位不推进，所以是"晚触发"而不是"丢触发"。
	if _, err := e.m.pg.Exec(`DELETE FROM task_schedules WHERE name LIKE 'acceptance-p35%'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 计划任务跑出来的任务是它自己建的，不在 newTask 的收尾里——顺手带走。
		rows, err := e.m.pg.Query(`SELECT last_task_id FROM task_schedules WHERE name LIKE 'acceptance-p35%' AND last_task_id > 0`)
		if err == nil {
			var ids []int64
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
			for _, id := range ids {
				_, _ = e.m.pg.Exec(`DELETE FROM findings WHERE task_id=$1`, id)
				_ = e.m.pg.DeleteTask(id)
			}
		}
		_, _ = e.m.pg.Exec(`DELETE FROM task_schedules WHERE name LIKE 'acceptance-p35%'`)
	})
	rec = e.request(http.MethodPost, "/api/task-schedules", `{
		"name":"acceptance-p35-sched",
		"frequency":{"kind":"every_hours","every_hours":1},
		"task_spec":{"description":"acceptance-p35 停机守卫","goal":"验证停机期间不起新工作"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("建计划任务 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var schedDTO struct {
		ID        int64 `json:"id"`
		LastRunID int64 `json:"last_task_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &schedDTO); err != nil {
		t.Fatalf("计划任务解码失败：%v raw=%s", err, rec.Body.String())
	}
	before := e.taskCount()
	if _, err := e.m.pg.Exec(`UPDATE task_schedules SET next_run_at=now()-interval '1 minute' WHERE id=$1`, schedDTO.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.m.pg.SetSetting(intercept.SettingKillSwitch,
		`{"on":true,"at":"`+time.Now().Format(time.RFC3339)+`","reason":"验收：停机守卫"}`); err != nil {
		t.Fatal(err)
	}
	e.s.sched.tickOnce(e.ctx)
	if got := e.taskCount(); got != before {
		t.Errorf("停机期间到点的计划任务不该建任务：%d → %d", before, got)
	}
	// 解除后同一个 tick 立刻补上（水位没推进）。
	rec = e.request(http.MethodPost, "/api/scope/kill-switch", `{"on":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("解除停机 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.s.sched.tickOnce(e.ctx)
	if got := e.taskCount(); got != before+1 {
		t.Errorf("解除后到点的计划任务该补跑一次：%d → %d", before, got)
	}
	// 停机期间我们自己的任务也被停了（这正是"全停"的意思）——恢复它，好让 ⑧ 只看窗口那件事。
	if rec := e.request(http.MethodPost, "/api/scope/resume-all", ""); rec.Code != http.StatusOK {
		t.Fatalf("继续全部 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if pausedNow() {
		t.Fatal("恢复后任务该在跑")
	}

	// ⑧ 窗口写错（绕过接口校验直接写库）时**不因配置错误让全平台停工**，
	//    但要在状态里醒目地说出来——拦截层已经对每次调用转人工，那一堆待审批很难查。
	if err := e.m.pg.SetSetting(intercept.SettingScopeHours, "9 点到 6 点"); err != nil {
		t.Fatal(err)
	}
	e.s.enforceScopeEnforcement()
	if pausedNow() {
		t.Error("窗口格式错时不该把任务全停了（那不是授权判定，是配置写错）")
	}
	raw, _, _ = e.m.pg.GetSetting(settingScopeWindowState)
	if !strings.Contains(raw, "parse_error") {
		t.Errorf("窗口解析失败该在状态里说出来：%q", raw)
	}
}

// accP36 凭据可利用面：把 credential 节点从「在手/掉了」做成能据此行动的东西——
// 能登哪、验证过没有、以及**明文只在受控入口出且留痕**。
func accP36(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p36", "凭据可利用面")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)
	worker := tsx.WorkerTools()
	planner := tsx.PlannerTools()

	nodeID := func(out string) int64 {
		t.Helper()
		line, _, _ := strings.Cut(out, "\n")
		_, raw, _ := strings.Cut(line, ": ")
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			t.Fatalf("回执里没有节点 id：%q", out)
		}
		return id
	}
	callOn := func(face []actool.CoreTool, toolName, in string) string {
		t.Helper()
		res, err := workflowTool(t, face, toolName).Call(context.Background(), json.RawMessage(in), nil)
		if err != nil {
			t.Fatalf("%s: %v", toolName, err)
		}
		return res.Flatten()
	}

	intentOut := callOn(planner, "add_intent", `{"summary":"读 wp-config 找数据库口令"}`)
	if !strings.HasPrefix(intentOut, "intent created: ") {
		t.Fatalf("意图没建上：%s", intentOut)
	}
	intentID := nodeID(intentOut)

	// ① 声称验证过却没有证据 → 工具拒收（与战果同一道证据门）。
	out := callOn(worker, "report_credential",
		`{"summary":"读到 db 口令","kind":"password","username":"wp_user","secret":"acc-p36-secret",`+
			`"evidence":"grep DB_PASSWORD wp-config.php","confidence":"high","intent_id":`+strconv.FormatInt(intentID, 10)+`,`+
			`"targets":[{"host":"acc-p36-db01","port":3306,"service":"mysql","verified":true}]}`)
	if !strings.Contains(out, "没有 evidence") {
		t.Fatalf("没证据的 verified 该被拒收：%s", out)
	}

	// ② 正常记一条：一条已验证 + 一条待验证。
	out = callOn(worker, "report_credential",
		`{"summary":"读到 db 口令","kind":"password","username":"wp_user","secret":"acc-p36-secret",`+
			`"evidence":"grep DB_PASSWORD wp-config.php","confidence":"high","intent_id":`+strconv.FormatInt(intentID, 10)+`,`+
			`"targets":[{"host":"acc-p36-db01","port":3306,"service":"mysql","verified":true,`+
			`"evidence":"mysql -h acc-p36-db01 -u wp_user → 登录成功"},{"host":"acc-p36-db02","service":"mysql"}]}`)
	if !strings.HasPrefix(out, "credential recorded: ") {
		t.Fatalf("凭据没记上：%s", out)
	}
	credID := nodeID(out)

	// ③ 报告：有指纹、有可登目标、**没有明文**。报告是要交出去的文档，还会被打进批量交付包。
	md := e.report(task)
	fp := db.CredentialFingerprint("password", "wp_user", "acc-p36-secret")
	e.mustContain(md, "指纹："+fp, "报告里的凭据指纹")
	e.mustContain(md, "可登目标：acc-p36-db01:3306（mysql）（已验证可登）、acc-p36-db02（mysql）（待验证）", "可登目标与验证状态")
	if strings.Contains(md, "acc-p36-secret") {
		t.Fatalf("凭据明文进了报告：\n%s", md)
	}

	// ④ 图形接口给指纹（界面默认脱敏靠它指认），同样不含明文。
	rec := e.request(http.MethodGet, "/api/exploration/graph?task="+task.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("graph status=%d", rec.Code)
	}
	e.mustContain(rec.Body.String(), `"fingerprint":"`+fp+`"`, "图形接口的凭据指纹")
	// 明文**不随节点接口下发**（服务端把 payload 里的 secret 摘掉、换成 secret_redacted 标记）：
	// 报告、概览、提示词三处不印明文，界面这一处也不该拿到它——"明文只在受控入口出"要真的是
	// 只有那一个入口，而不是"界面上有、只是没人细看"。
	if strings.Contains(rec.Body.String(), "acc-p36-secret") {
		t.Fatalf("凭据明文随节点接口下发了：\n%s", rec.Body.String())
	}
	// payload 是以 JSON 字符串下发的，里面的引号被转义过——断言标记名就够了。
	e.mustContain(rec.Body.String(), "secret_redacted", "脱敏标记（界面据此显示可查看入口）")

	// ⑤ 明文受控入口：POST 取回原文，且**那一次留痕**（任务时间线里能查到谁什么时候看过）。
	rec = e.request(http.MethodPost, "/api/credentials/"+strconv.FormatInt(credID, 10)+"/reveal?task="+task.ID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("取明文 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), "acc-p36-secret", "受控入口返回的明文")
	e.mustContain(rec.Body.String(), `"fingerprint":"`+fp+`"`, "受控入口返回的指纹")
	acts, _, err := task.Store.ActivityList(nil, 0, 200)
	if err != nil {
		t.Fatalf("读时间线失败：%v", err)
	}
	seen := false
	for _, a := range acts {
		if strings.Contains(a.Summary, "查看凭据明文") && strings.Contains(a.Summary, fp) {
			seen = true
		}
	}
	if !seen {
		t.Errorf("查看明文该在任务时间线留一条痕：%+v", acts)
	}

	// ⑥ 受控入口只认凭据、且只认本任务的：指向意图节点 → 404；指向别的任务的凭据 → 404。
	if rec := e.request(http.MethodPost,
		"/api/credentials/"+strconv.FormatInt(intentID, 10)+"/reveal?task="+task.ID, `{}`); rec.Code != 404 {
		t.Errorf("非凭据节点该 404，得到 %d：%s", rec.Code, rec.Body.String())
	}
	if rec := e.request(http.MethodPost,
		"/api/credentials/"+strconv.FormatInt(credID, 10)+"/reveal?task=999999", `{}`); rec.Code != 404 {
		t.Errorf("别的任务/不存在的任务该 404，得到 %d：%s", rec.Code, rec.Body.String())
	}

	// ⑦ verify_credential：后来真的登成功了，补上验证（不新记一条凭据）。
	out = callOn(worker, "verify_credential",
		`{"credential_id":`+strconv.FormatInt(credID, 10)+`,"host":"acc-p36-db02","service":"mysql",`+
			`"evidence":"mysql -h acc-p36-db02 -u wp_user → 登录成功"}`)
	if !strings.Contains(out, "verified on") {
		t.Fatalf("验证没记上：%s", out)
	}
	md = e.report(task)
	e.mustContain(md, "acc-p36-db02（mysql）（已验证可登）", "补上的验证进了报告")
	if strings.Contains(md, "acc-p36-secret") {
		t.Fatalf("补验证后明文进了报告：\n%s", md)
	}
}

// accP37 作业时间线：把台账 / 审批 / 发现 / 战果 / 检测验证串成一条**可交付**的线，
// 且页面读的与导出的**同一份装配**。
//
// 台账那一格直接插行而不是真跑一次执行：执行本身的行为由 accP13/24 覆盖，这里考的是
// 时间线怎么读它（尤其是把 cancel_note/error 一并带出来）。
func accP37(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p37", "作业时间线")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)
	worker := tsx.WorkerTools()
	planner := tsx.PlannerTools()

	callOn := func(face []actool.CoreTool, toolName, in string) string {
		t.Helper()
		res, err := workflowTool(t, face, toolName).Call(context.Background(), json.RawMessage(in), nil)
		if err != nil {
			t.Fatalf("%s: %v", toolName, err)
		}
		return res.Flatten()
	}

	intentOut := callOn(planner, "add_intent", `{"summary":"读配置找凭据","techniques":["T1552.001"]}`)
	if !strings.HasPrefix(intentOut, "intent created: ") {
		t.Fatalf("意图没建上：%s", intentOut)
	}

	// ① 五路各来一条：执行台账（直接插行）/ 审批 / 发现 / 战果（共 2 条）/ 检测验证。
	if _, err := e.m.pg.Exec(`
INSERT INTO tool_executions(task_id, worker, tool, command, description, status, exit_code,
                           output_path, cancel_note, started_at, ended_at)
VALUES($1,'work#1','run_execution','nmap -sV acc-p37.example','验收：时间线里的执行台账','cancelled',-1,
       '/tmp/acc-p37.log','用户/模型取消了这条长任务执行', now()-interval '30 minutes', now()-interval '28 minutes')`,
		task.ID); err != nil {
		t.Fatalf("插执行台账失败：%v", err)
	}
	var expID int64
	if err := e.m.pg.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, tid).Scan(&expID); err != nil {
		t.Fatalf("取 exploration_id 失败：%v", err)
	}
	// 系统留言（默认进时间线）与原始步骤（只随 kinds=all 进）：两档的分界正是时间线该守的那条线。
	if _, err := e.m.pg.Exec(`
INSERT INTO activity(exploration_id, worker, kind, summary, created_at)
VALUES($1,'system','text','停机开关已启用：验收', now()-interval '10 minutes')`, expID); err != nil {
		t.Fatalf("插系统留言失败：%v", err)
	}
	if _, err := e.m.pg.Exec(`
INSERT INTO activity(exploration_id, worker, kind, tool, summary, created_at)
VALUES($1,'work#1','tool_use','Bash','curl -s http://acc-p37.example/robots.txt', now()-interval '9 minutes')`, expID); err != nil {
		t.Fatalf("插原始步骤失败：%v", err)
	}
	if _, err := e.m.pg.Exec(`
INSERT INTO intercept_pending(task_id, agent_name, tool_name, tool_input, status, reason, decision_source, created_at, decided_at)
VALUES($1,'work#1','Bash','{"command":"nmap acc-p37.example"}','denied',
       '越出任务范围：acc-p37.example 不在 task_scope 内','rule', now()-interval '25 minutes', now()-interval '25 minutes')`,
		task.ID); err != nil {
		t.Fatalf("插审批记录失败：%v", err)
	}
	e.newFinding(task, tid, `{"vulnclass":"weak_password","severity":"high","summary":"后台弱口令","evidence":"admin/admin 登录成功","confidence":"high"}`)
	callOn(worker, "report_foothold",
		`{"summary":"在 acc-p37-web01 上拿到 root 的 shell","access":"反弹 shell","privilege":"root",`+
			`"evidence":"nc -lvnp 4444 → connect","confidence":"high"}`)
	callOn(worker, "report_credential",
		`{"summary":"读到 acc-p37 数据库口令","kind":"password","username":"wp_user","secret":"acc-p37-secret",`+
			`"evidence":"grep DB_PASSWORD wp-config.php","confidence":"high",`+
			`"targets":[{"host":"acc-p37-db01","port":3306,"service":"mysql","verified":true,"evidence":"mysql 登录成功"}]}`)
	callOn(worker, "report_detection_check",
		`{"result":"not_triggered","summary":"读 /etc/shadow 无任何告警","technique":"T1003.008",`+
			`"evidence":"查询 EDR 控制台该时段无记录","confidence":"high"}`)

	// ② 页面读的那条接口：五类里程碑各在，且**按时间正序**。
	rec := e.request(http.MethodGet, "/api/tasks/"+task.ID+"/timeline", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("时间线 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var tl struct {
		Events []TimelineEvent `json:"events"`
		Counts map[string]int  `json:"counts"`
		From   string          `json:"from"`
		To     string          `json:"to"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tl); err != nil {
		t.Fatalf("解码失败：%v raw=%s", err, rec.Body.String())
	}
	for _, kind := range []string{"execution", "approval", "finding", "loot", "detection"} {
		if tl.Counts[kind] == 0 {
			t.Errorf("时间线缺少 %s 这一类：%v\n%s", kind, tl.Counts, rec.Body.String())
		}
	}
	if tl.Counts["loot"] != 2 {
		t.Errorf("战果该有两条（据点 + 凭据）：%v", tl.Counts)
	}
	ordered := true
	for i := 1; i < len(tl.Events); i++ {
		if tl.Events[i-1].At > tl.Events[i].At {
			ordered = false
		}
	}
	if !ordered {
		t.Errorf("时间线该按时间正序：%+v", tl.Events)
	}
	if tl.From == "" || tl.To == "" {
		t.Errorf("时间范围该报出来：from=%q to=%q", tl.From, tl.To)
	}
	// 执行那一格要带出**为什么收场**（cancel_note）——复盘时这正是要看的东西。
	foundCancel := false
	for _, ev := range tl.Events {
		if ev.Kind == "execution" {
			if ev.State != "cancelled" || !strings.Contains(ev.Detail, "取消了这条长任务执行") {
				t.Errorf("执行那一格该带状态与取消理由：%+v", ev)
			}
			foundCancel = true
		}
	}
	if !foundCancel {
		t.Error("时间线里没有执行那一格")
	}
	// 审批那一格要带判定来源与理由——那是"授权边界真的生效过"的证据。
	foundApproval := false
	for _, ev := range tl.Events {
		if ev.Kind == "approval" {
			if ev.State != "denied" || !strings.Contains(ev.Detail, "不在 task_scope 内") {
				t.Errorf("审批那一格该带结论与理由：%+v", ev)
			}
			foundApproval = true
		}
	}
	if !foundApproval {
		t.Error("时间线里没有审批那一格")
	}

	// ③ 默认**不含**原始步骤；kinds=all 才带上（时间线是交付物，默认不塞过程流水）。
	if tl.Counts["step"] != 0 {
		t.Errorf("默认不该带原始步骤：%v", tl.Counts)
	}
	if tl.Counts["note"] != 1 {
		t.Errorf("系统留言默认该在时间线里：%v", tl.Counts)
	}
	rec = e.request(http.MethodGet, "/api/tasks/"+task.ID+"/timeline?kinds=all", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("kinds=all status=%d", rec.Code)
	}
	var all struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if all.Counts["step"] != 1 {
		t.Errorf("kinds=all 该带上原始步骤：%v", all.Counts)
	}

	// ④ 导出：与页面同一份装配，且**凭据只出指纹不出明文**（交付件是要发给客户的）。
	rec = e.request(http.MethodGet, "/api/tasks/"+task.ID+"/timeline.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("导出 status=%d body=%s", rec.Code, rec.Body.String())
	}
	md := rec.Body.String()
	e.mustContain(md, "# 作业时间线：acceptance-p37", "导出件的标题")
	e.mustContain(md, "| 时间 | 类别 | 事件 | 收场 | 依据 |", "导出件的表头")
	e.mustContain(md, "验收：时间线里的执行台账", "导出件里的执行那一格")
	e.mustContain(md, "不在 task_scope 内", "导出件里的审批理由")
	fp := db.CredentialFingerprint("password", "wp_user", "acc-p37-secret")
	e.mustContain(md, "指纹："+fp, "导出件里的凭据指纹")
	if strings.Contains(md, "acc-p37-secret") {
		t.Fatalf("凭据明文进了交付件：\n%s", md)
	}
	if strings.Contains(md, "| 步骤 |") {
		t.Errorf("导出默认不该含原始步骤：\n%s", md)
	}

	// ⑤ 空任务：如实说没有，而不是给一份空表。
	empty, emptyTid := e.newTask("acceptance-p37-empty", "空时间线")
	_ = emptyTid
	rec = e.request(http.MethodGet, "/api/tasks/"+empty.ID+"/timeline.md", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("空任务导出 status=%d", rec.Code)
	}
	e.mustContain(rec.Body.String(), "没有可列入时间线的事件", "空任务如实交代")
}

// accP38 事前可检出性：动手前就知道"这一手会留下什么"，而且它落在**人要决定的那个地方**
// （审批卡上），不是躺在某个日志里。
//
// 与 P31 的分界同样是这一条验收要钉的：这里给的是**预期**，P31 给的是**实测**——两者混起来
// 就是把"预计会留痕"读成"确实被检出"，或者反过来把"没收录"读成"没有日志"。
func accP38(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p38", "事前可检出性")
	tsx := agent.NewToolSet(task.Store, "work#1")
	tsx.SetTaskID(tid)

	callTool := func(name, in string) string {
		t.Helper()
		res, err := workflowTool(t, tsx.WorkerTools(), name).Call(context.Background(), json.RawMessage(in), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Flatten()
	}

	// ① 工具：命中知识库给笔记（留在哪 / 信号 / 可见度 / 说明）。
	out := callTool("opsec_notes", `{"command":"procdump -ma lsass.exe lsass.dmp"}`)
	var res struct {
		Notes []struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Where    []string `json:"where"`
			Signals  []string `json:"signals"`
			Exposure string   `json:"exposure"`
			Note     string   `json:"note"`
		} `json:"notes"`
		Uncovered bool   `json:"uncovered"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("工具输出不是 JSON：%v raw=%s", err, out)
	}
	if res.Uncovered || len(res.Notes) == 0 {
		t.Fatalf("该命中 LSASS 那一类：%s", out)
	}
	hit := false
	for _, n := range res.Notes {
		if n.ID == "lsass-access" {
			hit = true
			if n.Exposure == "" || len(n.Where) == 0 || len(n.Signals) == 0 || n.Note == "" {
				t.Errorf("笔记该说清留在哪、有哪些信号、可见度多少：%+v", n)
			}
		}
	}
	if !hit {
		t.Errorf("该命中 lsass-access：%s", out)
	}

	// ② 没收录时**明说**，且话术里点明"不代表没有日志"——这是这个功能唯一的失败方式。
	out = callTool("opsec_notes", `{"command":"echo hello"}`)
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Uncovered || len(res.Notes) != 0 {
		t.Fatalf("未收录该显式说明：%s", out)
	}
	if !strings.Contains(res.Message, "不代表没有日志") {
		t.Errorf("话术该点明「没收录 ≠ 没有日志」：%s", res.Message)
	}

	// ③ 落在审批卡上：判定时就算好、随审计落库（事后补算没有意义）。
	ctx := intercept.WithTaskContext(context.Background(), task.ID, "work#1", nil)
	dec := intercept.Decision{Action: "ask", RuleName: "验收规则", Message: "验收：需要人工审批"}
	e.m.interceptor.Log(ctx, 0, dec, "Bash", json.RawMessage(`{"command":"secretsdump.py corp/admin@10.0.0.5"}`), "allowed")

	var auditRaw []byte
	var pendingID int64
	if err := e.m.pg.QueryRow(`
SELECT id, COALESCE(audit::text,'') FROM intercept_pending
WHERE task_id=$1 ORDER BY id DESC LIMIT 1`, task.ID).Scan(&pendingID, &auditRaw); err != nil {
		t.Fatalf("取审批记录失败：%v", err)
	}
	if pendingID == 0 {
		t.Fatal("审批记录没落库")
	}
	var audit struct {
		OpsecNotes []struct {
			ID string `json:"id"`
		} `json:"opsec_notes"`
		OpsecUncovered bool   `json:"opsec_uncovered"`
		OpsecCommand   string `json:"opsec_command"`
	}
	if err := json.Unmarshal(auditRaw, &audit); err != nil {
		t.Fatalf("审计不是 JSON：%v raw=%s", err, auditRaw)
	}
	if len(audit.OpsecNotes) == 0 {
		t.Fatalf("审批记录该带上可检出性笔记：%s", auditRaw)
	}
	found := false
	for _, n := range audit.OpsecNotes {
		if n.ID == "dcsync" {
			found = true
		}
	}
	if !found {
		t.Errorf("该命中 dcsync：%s", auditRaw)
	}
	if audit.OpsecCommand == "" {
		t.Error("该记下判定针对的命令原文（复盘时要知道算的是哪条）")
	}

	// ④ 接出来的详情接口把它带给审批卡（人做决定的地方）。详情走 history 那条路由
	// （pending/{id} 只给待批行本身，不带审计）。
	rec := e.request(http.MethodGet, "/api/intercept/history/"+strconv.FormatInt(pendingID, 10), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("审批详情 status=%d body=%s", rec.Code, rec.Body.String())
	}
	e.mustContain(rec.Body.String(), `"opsec_notes"`, "详情接口里的可检出性笔记")
	e.mustContain(rec.Body.String(), "dcsync", "命中的那一条笔记")

	// ⑤ 只读一览：团队要能看见这份清单覆盖到哪、边界在哪（不知道边界，就会把"没提示"当"没日志"）。
	rec = e.request(http.MethodGet, "/api/opsec/knowledge", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("知识库一览 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var kb struct {
		Entries []struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Where    []string `json:"where"`
			Signals  []string `json:"signals"`
			Exposure string   `json:"exposure"`
			Note     string   `json:"note"`
		} `json:"entries"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &kb); err != nil {
		t.Fatalf("一览解码失败：%v", err)
	}
	if len(kb.Entries) < 10 {
		t.Errorf("一览该列出知识库条目：%d 条", len(kb.Entries))
	}
	for _, en := range kb.Entries {
		if en.ID == "" || en.Title == "" || len(en.Where) == 0 || len(en.Signals) == 0 ||
			en.Exposure == "" || en.Note == "" {
			t.Errorf("条目该自证（留在哪/信号/可见度/说明）：%+v", en)
		}
	}
	// 口径要写在这一页上：预计 ≠ 实测；没收录 ≠ 没有日志。
	e.mustContain(kb.Note, "预计", "一览的口径说明")
	e.mustContain(kb.Note, "不代表没有日志", "没收录时的口径")

	// ⑥ 非动作类工具不标注：谈可检出性没有意义，也不该在审计里多一段空话。
	e.m.interceptor.Log(ctx, 0, dec, "Read", json.RawMessage(`{"file_path":"/etc/hosts"}`), "allowed")
	var auditRaw2 []byte
	if err := e.m.pg.QueryRow(`SELECT COALESCE(audit::text,'') FROM intercept_pending WHERE task_id=$1 ORDER BY id DESC LIMIT 1`, task.ID).Scan(&auditRaw2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(auditRaw2), "opsec_") {
		t.Errorf("非动作工具不该带可检出性标注：%s", auditRaw2)
	}
}

// accP39 云上枚举结果导入（G 项）：AWS 授权明细 + K8s RBAC 进同一张图，与域内共用同一条路径算法。
//
// 这一条验收要钉住的是"云上不是另一个应用"：导入进去的云对象，攻击路径视图零改动就能读出
// 「谁能拿到 admin 策略 / 集群管理员」，因为两者共用同一套边与同一个 BFS。
func accP39(t *testing.T, e *accEnv) {
	task, tid := e.newTask("acceptance-p39", "云上枚举导入")

	awsDoc := `{
	  "UserDetailList":[
	    {"UserName":"acc-p39-alice","Arn":"arn:aws:iam::210987654321:user/acc-p39-alice",
	     "GroupList":["acc-p39-admins"],
	     "AttachedManagedPolicies":[{"PolicyName":"AdministratorAccess","PolicyArn":"arn:aws:iam::aws:policy/AdministratorAccess"}]},
	    {"UserName":"acc-p39-bob","Arn":"arn:aws:iam::210987654321:user/acc-p39-bob","GroupList":[]}
	  ],
	  "GroupDetailList":[
	    {"GroupName":"acc-p39-admins","Arn":"arn:aws:iam::210987654321:group/acc-p39-admins"}
	  ],
	  "RoleDetailList":[
	    {"RoleName":"acc-p39-app","Arn":"arn:aws:iam::210987654321:role/acc-p39-app",
	     "AssumeRolePolicyDocument":{"Statement":[
	       {"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::210987654321:user/acc-p39-alice"},"Action":"sts:AssumeRole"},
	       {"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}}
	  ],
	  "Policies":[
	    {"PolicyName":"AdministratorAccess","Arn":"arn:aws:iam::aws:policy/AdministratorAccess",
	     "PolicyVersionList":[{"IsDefaultVersion":true,"Document":{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}}]}
	  ]
	}`
	k8sDoc := `{"kind":"List","items":[
	  {"kind":"ClusterRole","metadata":{"name":"acc-p39-cluster-admin"},
	   "rules":[{"apiGroups":["*"],"resources":["*"],"verbs":["*"]}]},
	  {"kind":"ClusterRoleBinding","metadata":{"name":"acc-p39-bind"},
	   "roleRef":{"kind":"ClusterRole","name":"acc-p39-cluster-admin"},
	   "subjects":[{"kind":"ServiceAccount","name":"system:serviceaccount:acc39:runner","namespace":"acc39"}]},
	  {"kind":"ServiceAccount","metadata":{"name":"runner","namespace":"acc39"}},
	  {"kind":"Namespace","metadata":{"name":"acc39"}}
	]}`
	body := func() string {
		b, _ := json.Marshal(map[string]any{"task_id": tid, "files": []map[string]string{
			{"name": "aws-authz.json", "json": awsDoc},
			{"name": "k8s-rbac.json", "json": k8sDoc},
		}})
		return string(b)
	}

	// ① 预览：只读，且把"会导入什么、认出了什么高价值目标"说清楚。
	rec := e.request(http.MethodPost, "/api/cloud/import/preview", body())
	if rec.Code != http.StatusOK {
		t.Fatalf("预览 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Objects   map[string]int `json:"objects"`
		NodesNew  int            `json:"nodes_new"`
		HighValue []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"highvalue"`
		Warnings []string       `json:"warnings"`
		Skipped  map[string]int `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("预览解码失败：%v raw=%s", err, rec.Body.String())
	}
	if preview.NodesNew == 0 || preview.Objects["user"] != 2 || preview.Objects["policy"] == 0 {
		t.Fatalf("预览口径不对：%+v", preview)
	}
	hv := map[string]bool{}
	for _, h := range preview.HighValue {
		hv[h.Name] = true
	}
	if !hv["AdministratorAccess"] || !hv["acc-p39-cluster-admin"] {
		t.Errorf("高价值目标该认出 admin 策略与集群管理员：%+v", preview.HighValue)
	}
	if len(preview.Skipped) == 0 {
		t.Errorf("未建模的东西该如实计数（服务主体/Namespace）：%s", rec.Body.String())
	}

	// ② 预览只读。
	if nodes, err := task.Store.ListByKind(db.KindCloudObject, 10); err != nil || len(nodes) != 0 {
		t.Fatalf("预览写了库：%d 个云对象 (err=%v)", len(nodes), err)
	}

	// ③ 应用。
	rec = e.request(http.MethodPost, "/api/cloud/import", body())
	if rec.Code != http.StatusOK {
		t.Fatalf("应用 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var applied struct {
		NodesCreated int `json:"nodes_created"`
		EdgesCreated int `json:"edges_created"`
		Failures     int `json:"failures"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.NodesCreated == 0 || applied.EdgesCreated == 0 || applied.Failures != 0 {
		t.Fatalf("应用结果不对：%s", rec.Body.String())
	}

	// ④ 攻击路径：与域内**同一个视图、同一条算法**，读得出"谁能到管理员"。
	rec = e.request(http.MethodGet, "/api/tasks/"+task.ID+"/ad-paths", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("路径 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var pathsResp struct {
		Paths []struct {
			Start  string `json:"start"`
			Target string `json:"target"`
			Steps  []struct {
				From   string   `json:"from"`
				Rel    string   `json:"rel"`
				To     string   `json:"to"`
				Rights []string `json:"rights"`
				Note   string   `json:"note"`
			} `json:"steps"`
		} `json:"paths"`
		Stats map[string]any `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pathsResp); err != nil {
		t.Fatalf("路径解码失败：%v", err)
	}
	byTarget := map[string][]string{}
	for _, p := range pathsResp.Paths {
		for _, s := range p.Steps {
			byTarget[p.Target] = append(byTarget[p.Target], s.Rel)
		}
	}
	if _, ok := byTarget["AdministratorAccess"]; !ok {
		t.Errorf("该读出到 admin 策略的路径：%s", rec.Body.String())
	}
	if _, ok := byTarget["acc-p39-cluster-admin"]; !ok {
		t.Errorf("该读出到集群管理员的路径：%s", rec.Body.String())
	}
	// 云上那一跳要有自己的说明（不是 AD 那套"可写入该组"的话）。
	foundCloudNote := false
	for _, p := range pathsResp.Paths {
		for _, s := range p.Steps {
			if strings.Contains(s.Note, "策略") || strings.Contains(s.Note, "角色") {
				foundCloudNote = true
			}
		}
	}
	if !foundCloudNote {
		t.Errorf("云上那一跳该有对应的说明：%s", rec.Body.String())
	}

	// ⑤ 幂等：同一份枚举再导一次只合并。
	rec = e.request(http.MethodPost, "/api/cloud/import", body())
	if rec.Code != http.StatusOK {
		t.Fatalf("重导 status=%d", rec.Code)
	}
	var again struct {
		NodesCreated int `json:"nodes_created"`
		EdgesCreated int `json:"edges_created"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.NodesCreated != 0 || again.EdgesCreated != 0 {
		t.Fatalf("重导该只合并不新增：%s", rec.Body.String())
	}

	// ⑥ 坏输入：不是云上枚举结果的 JSON → 200 的预览里如实说（文件级错误不炸整个请求）；
	//    缺 task_id / 任务不存在 → 400 / 404。
	bad, _ := json.Marshal(map[string]any{"task_id": tid, "files": []map[string]string{
		{"name": "x.json", "json": `{"hello":1}`}}})
	rec = e.request(http.MethodPost, "/api/cloud/import/preview", string(bad))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "不是云上枚举结果") {
		t.Fatalf("坏文件该在预览里如实说明：%d %s", rec.Code, rec.Body.String())
	}
	noTask, _ := json.Marshal(map[string]any{"files": []map[string]string{{"name": "a.json", "json": awsDoc}}})
	if rec := e.request(http.MethodPost, "/api/cloud/import", string(noTask)); rec.Code != 400 {
		t.Errorf("缺 task_id 该 400，得到 %d", rec.Code)
	}
	ghost, _ := json.Marshal(map[string]any{"task_id": tid + 999999, "files": []map[string]string{{"name": "a.json", "json": awsDoc}}})
	if rec := e.request(http.MethodPost, "/api/cloud/import", string(ghost)); rec.Code != 404 {
		t.Errorf("任务不存在该 404，得到 %d", rec.Code)
	}
}
