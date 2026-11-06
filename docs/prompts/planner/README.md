# planner 提示词备份（0.9.2 之前 / 重写前）

本目录是**重写前** planner 提示词的逐字备份，用于改崩后恢复。生成方式：
`agent/zz_dump_prompts_test.go`（临时用例）在经典/增强两种约束配置下装配后落盘。

| 文件 | 内容 |
|---|---|
| `classic.assembled.md` | 经典模式（约束全关）下 planner 收到的完整 system prompt |
| `enhanced.assembled.md` | 增强模式（约束全开）下同上 |
| `default.body.md` | 可编辑正文 `plannerDefaultTmpl` 原文（不含代码持有块） |
| `domain-*.md` | 7 份领域模板在 `planner_templates` 里落库的完整正文 |

## 恢复方法

1. **全局 planner 正文**：后台 → Agent → planner → 提示词编辑，把 `default.body.md` 全文粘回并保存
   （会生成一个新版本，历史版本仍可在版本列表里回滚）。
2. **领域模板**：后台 → 规划模板，把对应 `domain-<名称>.md` 的正文粘回保存。
3. **代码持有块**（`ledgerDisciplineBlock` / `chainDisciplineBlock` / `endgameDoctrineBlock` 等）
   不在本目录：它们随二进制发布，恢复对应版本的程序即可。
4. 若数据库里本就是旧版正文、且被启动迁移覆盖：用第 1/2 步粘回本目录内容即可恢复。

> 这些文件是**文本备份**，不参与编译；`docs/` 已在版本控制中。
