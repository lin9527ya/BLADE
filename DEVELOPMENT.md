# BLADE 开发与排障

本文覆盖本地开发、构建入口、测试隔离与常见故障。部署相关（systemd、隧道、防火墙）
见 [`deploy/README.md`](deploy/README.md)，VPS 勾选表见
[`deploy/VPS-CHECKLIST.md`](deploy/VPS-CHECKLIST.md)。

## 一、仓库结构

| 路径 | 作用 |
|---|---|
| `cmd/blade/` | 进程入口：参数、信号、自更新 Bootstrap、启动恢复 |
| `server/` | HTTP API、任务管理器、引擎（planner/worker 编排）、SSE |
| `agent/` | Planner / Worker / 主 Agent 的提示词、工具集、压缩器 |
| `db/` | PostgreSQL 访问层与 schema（`db/schema.sql`） |
| `traffic/` | 流量录制（SQLite + 内容寻址 blob）与证据快照 |
| `report/` | 报告渲染（Markdown 分桶、交付包） |
| `web/` | Next.js 静态导出的前端（构建产物嵌入 `server/webui/dist`） |
| `deploy/` | 部署模板、加固脚本与勾选清单 |
| `docs/` | 设计/优化文档 |

## 二、构建入口

- **一键**：`./build.sh`（前端静态导出 + Go 单二进制，带 `embedui` 标签）。
- **手动**：
  ```bash
  cd web && npm run build:static && cd ..
  cp -r web/out server/webui/dist
  CGO_ENABLED=0 go build -tags embedui -o dist/blade ./cmd/blade
  ```
- **开发模式**：`./dev.sh`（后端 :8787 + 流量代理 :8788 + 前端 :5173）。

> **资源受限机器**：编译与全量测试请限优先级与并行度，避免卡死桌面：
> `nice -n 19 ionice -c3 env GOMAXPROCS=2 go build ./...`，
> 测试加 `-p 1`；前端构建设 `NODE_OPTIONS=--max-old-space-size=2048`，且不要与
> 全量 Go 测试同时跑。

## 三、测试隔离

- 测试库必须独立：`BLADE_TEST_PG_DSN` 指向名字含 `test`/`acceptance` 的库。
  指向应用库会被 `guardTestDSN` 拒绝；确需如此要显式 `BLADE_TEST_ALLOW_UNSAFE=1`。
- 配置了数据库却连不上时测试**失败**而不是 Skip（`BLADE_REQUIRE_TESTS=1` /
  `BLADE_PG_DSN`）；验收用 `acceptance.sh`（自带 `BLADE_REQUIRE_ACCEPTANCE=1`）。
- 全量质量门禁：`go test -race -count=1 -p 1 ./...`、`go vet ./...`、
  `staticcheck ./...`、`gofmt -l .`；前端 `npx tsc --noEmit`、`npm test`、
  `npm run build:static`。CI 的 `quality` job 覆盖这些项。
- 个别用例会写共享测试库（如 playbook 提示词），已加 `t.Cleanup` 恢复；新增用例
  请沿用「自建数据 + 清理」模式，别依赖执行顺序。

## 四、启动与恢复

1. `selfupdate.Bootstrap()`：换装/回滚计数，必须在监听与恢复之前。
2. `NewManager` 连接 PG；`InstallAgentIsolation` 可选降权。
3. `Server.RestoreTaskRuntimes()`：
   - `RecoverDeleteStaging()` 按 journal 收尾上次未完成的普通删除；
   - `LoadExisting()` 读取任务；失败则**不启动任务循环**，`/api/health` 返回 503；
   - 重置残留 `running` 意图为 `open`，恢复暂停状态与 deadline 协调器。
4. 监听成功后才开始 `selfupdate.SettleDelay`（30s）计时，稳定后清除升级标记。
5. 退出：信号 → 取消引擎 → HTTP shutdown（5s）→ drain 等待 run 收尾。

## 五、常见故障

| 现象 | 先查 | 说明 |
|---|---|---|
| `/api/health` 503 | 服务端日志 `启动恢复失败` | PG 读取失败；修好数据库后重启，不会带病启动 |
| 容器内页面打不开 | `docker-compose.yml` 端口映射 | 容器内必须监听容器网卡（`-addr :8787`），宿主只发布 `127.0.0.1:8787` |
| 流量页出现「残缺」 | 日志中的 blob 落盘错误 | 正文落盘失败被标记 `partial`，该记录不能作为完整证据绑定 |
| 报告出现「数据缺项」 | 报告接口日志 | 某项读取失败（复测结论/覆盖矩阵/详细分析等），报告会在摘要声明缺项 |
| 报告出现「可能已过期」 | 该发现的证据版本 | 详细分析撰写后证据又变更，需要重新核对 |
| 意图反复 `no_output` | 意图的 `kind` | `verify` 意图只登记资产不算验证；补事实/发现或改用 `discovery` |
| MCP 调用被快速拒绝 | `GET /api/mcp` 的 `half_open`/`open` | 熔断冷却中；冷却结束只放行单探针，可用手动 reset 立即恢复 |
| 任务列表翻页后选择丢失 | — | 选择跨页保留，但只在删除成功时移除；切换筛选/排序会重置到第 1 页 |
| 升级后仍回滚 | 启动日志 + `/api/update/check` | 新版本连续启动失败会回滚；`SettleDelay` 从监听成功后计时 |

## 六、关键约定

- 报告正文只收通过确认门的发现（high / 人工确认 / 复测 reproduced|fixed）；
  其余进「待确认候选」，不静默丢弃。
- 覆盖矩阵按「资产 × 风险类」记账；verify 意图绑定多资产时只统计实际验证过的资产。
- 计划任务、自更新、MCP 守卫等运行态开关多数「每调用现读」，设置保存即生效；
  需要重建 provider 链的项在提交后统一 `applyLLM`。
- 设置保存分两阶段：先全量校验，再单事务写入，最后应用运行态；失败信息会明确
  「设置已保存，运行态未生效」。
