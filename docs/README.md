# 设计文档索引

本目录只放**会随代码演进维护**的设计说明。历史上代码里引用过十来份设计文档
（`任务级超时与收尾设计.md`、`LLM重试设计.md`、`探索策略.md`……），它们**从未进入仓库**
（`docs/` 曾在 `.gitignore` 里），所以那些引用现在统一指向本文件。

每一节写清楚：这个主题的权威说明在哪里（以**代码与 CHANGELOG** 为准，本文件只做索引）。

## 任务级超时与收尾

- 判定与状态机：`server/engine_timeout.go`（deadline 协调器、`settleTask`）
- 生命周期列与状态集合：`db/tasks.go`（`TerminalStatuses` / `TerminalStatusSQL`）、
  `db/task_status.go`（`PersistedTaskStatuses` / `DerivedTaskStatuses` / `DeriveTaskStatus`）
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

## 功能拓展路线图

`docs/功能拓展路线图.md`：交付/报告、资产/情报、自动化/运维四个方向的候选、排序与验收口径。
本轮只做本地闭环；黑板与探索意图流程已冻结，不再优化。

## 探索策略（已下线）

P20 探索策略已整体删除（`policy/`、策略端点、`exploration_policy` 读写、planner/worker
策略段）；`policy_blocked` 仅作为历史终态兼容旧任务。详见 `CHANGELOG.md`。

## 情报与计划任务

`server/intel_tools.go`（情报工具的三条边界）、`server/schedules.go`（计划任务的
CAS 防重与水位语义、create/start/pause/resume 动作、固定时区墙钟计算）、`sched/freq.go`
（结构化频率：间隔/每天/每周/指定日期一次性）、`server/scheduler.go`（触发器调度）。
计划任务默认按 `Asia/Shanghai`（北京时间）解释 daily/weekly/once 的 `HH:MM`，可在每条
计划上切换 IANA 时区；一次性计划执行后自动停用。`create` 动作可设置可选的
`auto_pause_date` + `auto_pause_at`，任务创建后到点会派生一条一次性 `pause` 计划，
自动暂停该任务；日期留空时按时间字段计算运行后的下一次。

## 资产模型与自动关联

`db/assets.go` + `db/asset_dsl.go`（资产 DSL 与查询），`enrich/`（异步补全工作池），
`db/companies.go`（公司级归属）。

## planner 触发

`db/tasks.go`（`plan_heartbeat_seconds` 的下限归一）、`server/engine.go`
（`pendingTriggers` 的 debounce 与合并）。

## 内置技能与冷咖啡

- 文件：`skills/coldcoffee/SKILL.md`（激活契约、激活页、统一工单、路由表）与 `references/`
  （6 个父路由、80 个叶子、normalizer、索引）。这是生成产物，来源与再生成见
  `skills/coldcoffee/SOURCE.md`；生成器在 gpt6-Astra `desktop/scripts/export-blade-skill.cjs`。
- 默认可见性：`db/db.go` 的 `builtinSkillVisibility`。种子每次启动 insert-if-absent，
  默认绑定 `auto` / `pentest` / `mainagent`，不覆盖用户的关闭；任意 agent 的开关仍在
  agent 编辑器。
- 激活提示：`server/coldcoffee.go` 的代码级 cue，由 `server/server.go` 的 `PromptAppend`
  接入；不写 `agent_prompts`，重置提示词不会丢。
- 技能加载与分层注入：`server/assembly.go`；prompt 追加钩子：`agent/prompt.go`。

## SDK 维护线

`go.mod` 里 `github.com/lin9527ya/Norma` 的说明注释；差异账本在 Norma 仓库自己的
`FORK.md`（不在本仓库）。

## IM 控制（钉钉 / 飞书）

面向使用者的接入与操作说明：`docs/IM控制使用说明.md`（应用创建、长连接、允许用户、命令、自然语言、安全边界与排障）。
实现入口：`im/`（平台适配）、`db/im.go`（数据层）、`server/im_service.go` / `server/im_commands.go`（服务与命令）、
`server/im_api.go`（管理 API）、`server/principal.go` / `server/assembly.go`（工具面收紧）。

## 移动 APP 与微信小程序安全测试

使用与架构总览：`docs/移动APP安全测试使用与架构说明.md`（Android/iOS 上传与原生 SAST/ELF/Mach-O/Hermes 画像、USB `adb reverse` / LAN 鉴权代理 / Root `iptables` 透明代理、10 套 Frida 模板与 `http_request` RPC 自动签名联动、IPC Fuzz / UI 状态机爬虫 / Root 沙箱与 Logcat 审计）。
APP 全流程调研与实现蓝图：`docs/APP渗透全流程调研与实现方案-2026-11-28.md`（对标最新版 `Pojun` 文档体例与架构特性，覆盖 Android / iOS / 跨平台混合 / 双身份 API 主战场的 10 阶段闭环方法论、MASVS/SRC 证据与驳回防错、BLADE `file:line` 断层勘察及 P0/P1/P2 工程落地方案）。
微信小程序渗透方案：`docs/微信小程序渗透全流程调研与实现方案-2026-11-28.md`（纯 Go `V1MMWX` 解密与 `.wxapkg`/`.zip` 防穿越提取、`miniapp_audit` + `skills/mini-program/` + 独立 `task_playbooks_miniapp_v1` 播种、前后端 AppID/路由/CloudBase/持久化审计回显、以及 `KillWxapkg` MIT 工具链隔离）。
实现入口：`mobile/`（APK/IPA/ELF/Mach-O 与 `wxapkg.go` 解包/SAST、设备与 Frida/审计控制）、`traffic/mobile_*.go`（`MobileProxy`、`MobileTransparentBridge`、CA 分发与 `mobile_hook` 合成流记录）、`db/mobile.go`、`toolpolicy/toolpolicy.go`（`task_mobile_device` / `task_mobile_app` 门控）、`agent/mobile.go` / `agent/tools_miniapp.go` / `agent/tools_http.go`、`server/app_upload.go` / `server/mobile_api.go`。

## 开发与排障

见根目录 `DEVELOPMENT.md`（构建、测试隔离、CI 门禁）与 `deploy/README.md`
（部署、信任模型、备份恢复、排障）。
