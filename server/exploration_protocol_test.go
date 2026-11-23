package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

func TestExplorationPromptMigrationKeepsCustomizations(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	for _, key := range []string{"planner", "worker"} {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil {
			t.Fatalf("agent %s: %v", key, err)
		}
		original, _ := m.pg.CurrentPrompt(a.ID)
		extra, _ := m.pg.CurrentPromptAppend(a.ID)
		t.Cleanup(func() { _, _ = m.pg.SavePromptWithAppend(a.ID, original, extra, "restore fixture", "test") })
		old, err := os.ReadFile("testdata/prompts/" + key + "-v0.9.19.body.md")
		if err != nil {
			t.Fatal(err)
		}
		for _, custom := range []bool{false, true} {
			body := string(old)
			if custom {
				body += "\n操作者的额外规则"
			}
			if _, err := m.pg.SavePromptWithAppend(a.ID, body, "保留我的追加要求", "test", "test"); err != nil {
				t.Fatal(err)
			}
			migrateExplorationPrompts(m.pg)
			got, _ := m.pg.CurrentPrompt(a.ID)
			want := agent.BuiltinPromptSeeds()[key]
			if custom {
				want = body
			}
			if got != want {
				t.Fatalf("%s custom=%v: wrong migrated body", key, custom)
			}
			appendText, _ := m.pg.CurrentPromptAppend(a.ID)
			if appendText != "保留我的追加要求" {
				t.Fatal("append lost")
			}
			versions, _ := m.pg.ListPromptVersions(a.ID)
			migrateExplorationPrompts(m.pg)
			after, _ := m.pg.ListPromptVersions(a.ID)
			if len(versions) != len(after) {
				t.Fatal("repeat startup created duplicate versions")
			}
		}
	}
}

func TestFactDescriptionMigrationPreservesToolPolicy(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	var seed agent.ToolSeed
	for _, s := range agent.BuiltinToolSeeds() {
		if s.Key == "record_fact" {
			seed = s
		}
	}
	old, err := os.ReadFile("testdata/prompts/record-fact-v0.9.22.description.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Ensure a row exists without resetting a row other tests deliberately edited.
	if err := m.pg.SeedTool(seed.Key, seed.Desc, nil, nil); err != nil {
		t.Fatal(err)
	}
	row, err := m.pg.GetTool(seed.Key)
	if err != nil {
		t.Fatal(err)
	}
	originalAgents, _ := json.Marshal(row.Agents)
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`UPDATE tools SET description=$2,schema=$3,enabled=$4,agents=$5,deferred=$6 WHERE key=$1`, row.Key, row.Description, row.Schema, row.Enabled, originalAgents, row.Deferred)
	})
	if _, err := m.pg.Exec(`UPDATE tools SET description=$1,enabled=false,agents='["worker"]',schema='{"type":"object","properties":{"summary":{"type":"string","default":"mine"}}}' WHERE key='record_fact'`, string(old)); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	got, err := m.pg.GetTool("record_fact")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != seed.Desc || got.Enabled || len(got.Agents) != 1 || got.Agents[0] != "worker" {
		t.Fatalf("policy reset: %+v", got)
	}
	if err := m.pg.UpdateToolDescriptionIfUnchanged(seed.Key, seed.Desc, "my custom description"); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	got, _ = m.pg.GetTool(seed.Key)
	if got.Description != "my custom description" {
		t.Fatal("custom tool description overwritten")
	}
}

func TestFactBatchRetainsIdentityAndPriority(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	for i := 0; i < 3; i++ {
		task.NotifyFact("fact", int64(10+i), int64(20+i), "独立观察", []int64{int64(i + 1)})
	}
	ev := task.drainTriggers()[0]
	if len(ev.Facts) != 3 || ev.Facts[0].FactID != 20 || ev.Facts[2].IntentID != 12 {
		t.Fatalf("batch lost branches: %+v", ev)
	}
	task.NotifyFinding(1, "must retain")
	for i := 0; i < 100; i++ {
		task.NotifyAsset("ordinary asset")
		task.flushLowPriority()
	}
	found := false
	for _, event := range task.drainTriggers() {
		if event.Kind == "finding" {
			found = true
		}
	}
	if !found {
		t.Fatal("ordinary batches displaced a finding")
	}
}

func TestHistoricalBuiltinPromptFingerprints(t *testing.T) {
	for _, version := range []string{"v0.9.19", "v0.9.22"} {
		for _, key := range []string{"planner", "worker"} {
			body, err := os.ReadFile("testdata/prompts/" + key + "-" + version + ".body.md")
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(strings.TrimSpace(string(body))))
			hashes := plannerBuiltinPromptSHA256
			if key == "worker" {
				hashes = workerBuiltinPromptSHA256
			}
			if !hashes[fmt.Sprintf("%x", sum[:])] {
				t.Fatalf("lost builtin fingerprint %s %s", key, version)
			}
		}
	}
	// v0.9.23 planner snapshot must stay recognized so upgrades migrate smoothly.
	v0923Body, err := os.ReadFile("testdata/prompts/planner-v0.9.23.body.md")
	if err != nil {
		t.Fatal(err)
	}
	sum0923 := sha256.Sum256([]byte(strings.TrimSpace(string(v0923Body))))
	if !plannerBuiltinPromptSHA256[fmt.Sprintf("%x", sum0923[:])] {
		t.Fatalf("lost builtin fingerprint planner v0.9.23")
	}
	// v0.9.25 is the body current installs hold right before the
	// evidence-conditioned re-probe rewrite; keep it recognized so upgrades
	// replace it instead of silently leaving the mechanical rules in place.
	v0925Body, err := os.ReadFile("testdata/prompts/planner-v0.9.25.body.md")
	if err != nil {
		t.Fatal(err)
	}
	sum0925 := sha256.Sum256([]byte(strings.TrimSpace(string(v0925Body))))
	if !plannerBuiltinPromptSHA256[fmt.Sprintf("%x", sum0925[:])] {
		t.Fatalf("lost builtin fingerprint planner v0.9.25")
	}
	if body := agent.PlannerPromptBody(); !strings.Contains(body, "no_output") {
		t.Fatalf("builtin planner prompt missing no_output guidance")
	}
}

func TestReporterAndDropIntentMigrationPreservesCustomizations(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })

	// 1. reporter prompt migration: old builtin hash upgrades while custom prompt & append survive.
	rep, _ := m.pg.GetAgentByKey("reporter")
	if rep == nil {
		rep, err = m.pg.CreateAgent("reporter", "报告撰写", "test reporter")
		if err != nil {
			t.Fatal(err)
		}
	}
	origPrompt, _ := m.pg.CurrentPrompt(rep.ID)
	origExtra, _ := m.pg.CurrentPromptAppend(rep.ID)
	t.Cleanup(func() {
		_, _ = m.pg.SavePromptWithAppend(rep.ID, origPrompt, origExtra, "restore fixture", "test")
	})

	// Test upgrading from the pre-v0.9.26 reporter default (SHA-256 = 1a02c50cb6...)
	oldReporterPrompt := strings.Replace(
		agent.ReporterDefaultPrompt,
		"**可照做复现**的分步操作（请求/命令/参数/前置条件）。每一步请求/命令前用一句话写明「**目的 + 前置依赖（用了上一步哪个返回值） + 预期结果 + 提取值后续用途**」，禁止裸贴报文让人猜上下文；HTTP 类漏洞凡从 `get_finding_traffic` / `traffic_get` 或工具输出拿到完整原始报文的，**优先贴可直接粘贴进 Burp Repeater 重放的 HTTP 原始请求（请求行 + Host + 请求头 + 空行 + Body）与状态行/关键响应头**，禁止只用参数表或半截摘要替代（未采集到的字段绝不臆造）。",
		"**可照做复现**的分步操作（请求/命令/参数/前置条件），能贴报文就贴。",
		1,
	)
	oldReporterPrompt = strings.Replace(
		oldReporterPrompt,
		"证明漏洞真实存在的关键请求/响应片段（优先完整状态行与关键响应头）、命令输出、回显、日志——用代码块贴原文，不要转述或擅自裁掉关键头。",
		"证明漏洞真实存在的关键请求/响应片段、命令输出、回显、日志——用代码块贴原文，不要转述。",
		1,
	)
	// Verify fixture matches registered fingerprint before testing migration.
	oldSum := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(oldReporterPrompt))))
	if !reporterBuiltinPromptSHA256[oldSum] {
		t.Fatalf("old reporter fixture hash %s missing from reporterBuiltinPromptSHA256", oldSum)
	}
	if _, err := m.pg.SavePromptWithAppend(rep.ID, oldReporterPrompt, "保留追加规则", "test", "test"); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	gotPrompt, _ := m.pg.CurrentPrompt(rep.ID)
	if gotPrompt != agent.ReporterDefaultPrompt {
		t.Fatalf("reporter prompt did not upgrade from known builtin hash")
	}
	if extra, _ := m.pg.CurrentPromptAppend(rep.ID); extra != "保留追加规则" {
		t.Fatalf("reporter append lost: %q", extra)
	}
	customReporter := oldReporterPrompt + "\n自定义段落"
	if _, err := m.pg.SavePromptWithAppend(rep.ID, customReporter, "保留追加规则", "test", "test"); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	if gotCustom, _ := m.pg.CurrentPrompt(rep.ID); gotCustom != customReporter {
		t.Fatalf("custom reporter prompt overwritten: %q", gotCustom)
	}

	// 2. drop_intent tool description migration preserves policy and custom edits.
	var dropSeed agent.ToolSeed
	for _, s := range agent.BuiltinToolSeeds() {
		if s.Key == "drop_intent" {
			dropSeed = s
		}
	}
	if err := m.pg.SeedTool(dropSeed.Key, dropSeed.Desc, nil, nil); err != nil {
		t.Fatal(err)
	}
	row, err := m.pg.GetTool("drop_intent")
	if err != nil || row == nil {
		t.Fatalf("get drop_intent tool: %v", err)
	}
	origAgents, _ := json.Marshal(row.Agents)
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`UPDATE tools SET description=$2,schema=$3,enabled=$4,agents=$5,deferred=$6 WHERE key=$1`,
			row.Key, row.Description, row.Schema, row.Enabled, origAgents, row.Deferred)
	})
	oldDropDesc := "丢弃一条**还没被领走**的排队意图（软删除：保留节点与全部产出/血缘，理由进时间线）。" +
		"\n★ 用途：队列已满或方向已被新事实覆盖时腾位置。**先看再删**——确已无价值才删，不要为\"整齐\"删。" +
		"\n★ 只能删 open / paused；正在跑的意图先用 kill_work 停掉。" +
		"\n★ 依赖它的下游会被一并解锁并在这里列出——那些步骤需要你重新规划（补前置或改方向）。"
	if _, err := m.pg.Exec(`UPDATE tools SET description=$1,enabled=false,agents='["planner"]' WHERE key='drop_intent'`, oldDropDesc); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	gotDrop, _ := m.pg.GetTool("drop_intent")
	if gotDrop.Description != dropSeed.Desc || gotDrop.Enabled || len(gotDrop.Agents) != 1 || gotDrop.Agents[0] != "planner" {
		t.Fatalf("drop_intent migration unexpected result: %+v", gotDrop)
	}
	if err := m.pg.UpdateToolDescriptionIfUnchanged("drop_intent", dropSeed.Desc, "custom drop desc"); err != nil {
		t.Fatal(err)
	}
	migrateExplorationPrompts(m.pg)
	gotDrop, _ = m.pg.GetTool("drop_intent")
	if gotDrop.Description != "custom drop desc" {
		t.Fatalf("custom drop_intent description overwritten: %q", gotDrop.Description)
	}
}
