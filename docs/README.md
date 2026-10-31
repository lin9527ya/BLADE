# 设计文档索引

本目录只放**会随代码演进维护**的设计说明。历史上代码里引用过十来份设计文档
（`任务级超时与收尾设计.md`、`LLM重试设计.md`、`探索策略.md`……），它们**从未进入仓库**
（`docs/` 曾在 `.gitignore` 里），所以那些引用现在统一指向本文件。

每一节写清楚：这个主题的权威说明在哪里（以**代码与 CHANGELOG** 为准，本文件只做索引）。

## 任务级超时与收尾

- 判定与状态机：`server/engine_timeout.go`（deadline 协调器、`settleTask`）
- 生命周期列与终态集合：`db/tasks.go`（`TerminalStatuses` / `TerminalStatusSQL`）
- 收尾词与轮数：`agent/wrapup.go`、`server/llm_api.go`（`WrapupTaskTimeout*`）
- 用户可见的语义：CHANGELOG「任务级超时」相关条目

## LLM 重试

五层重试（建连 / 空响应 / 同 provider 安全窗口 / 轮询熔断 / 意图重跑）的**次数与间隔**
解析在 `server/llmretry.go`、`db/llmretry.go`；各层的实现分别在 Norma 的 `llm/retry.go`、
`server/task_llm.go`、`llmpool/`。UI 对应「LLM → 重试与退避」标签页。

## LLM 轮询（故障转移）

`server/llmpool.go`（服务端接线）+ `llmpool/`（链、健康状态、冷却）。

## 交互式 shell

持久 PTY 会话的工具族与 Bash 提示词联动：`server/assembly.go`（工具注入）、
Norma 的 `tool/shell_session.go`（PTY 生命周期）。

## 自定义工具

`server/customtool.go`（四种 kind：command / script / http / shell 的执行器与校验）。

## 探索策略

`policy/`（词汇表与校验）、`server/policy_compile.go`（编译成任务级规则）、
`server/engine_timeout.go`（停摆判定）。`policy_blocked` 是它的终态。

## 情报与计划任务

`server/intel_tools.go`（情报工具的三条边界）、`server/schedules.go`（计划任务的
CAS 防重与水位语义）、`server/scheduler.go`（触发器调度）。

## 资产模型与自动关联

`db/assets.go` + `db/asset_dsl.go`（资产 DSL 与查询），`enrich/`（异步补全工作池），
`db/companies.go`（公司级归属）。

## planner 触发

`db/tasks.go`（`plan_heartbeat_seconds` 的下限归一）、`server/engine.go`
（`pendingTriggers` 的 debounce 与合并）。

## SDK 维护线

`go.mod` 里 `github.com/lin9527ya/Norma` 的说明注释；差异账本在 Norma 仓库自己的
`FORK.md`（不在本仓库）。

## 开发与排障

见根目录 `DEVELOPMENT.md`（构建、测试隔离、CI 门禁）与 `deploy/README.md`
（部署、信任模型、备份恢复、排障）。
