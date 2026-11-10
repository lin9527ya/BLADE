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
- **Docker 镜像输入**：`./build.sh --docker` 产出 `dist/<arch>/blade`，再 `docker compose up -d --build`。
- **手动**：
  ```bash
  cd web && npm run build:static && cd ..
  cp -r web/out server/webui/dist
  CGO_ENABLED=0 go build -tags embedui -o dist/blade ./cmd/blade
  ```
- **开发模式**：`./dev.sh`（后端 :9898 + 流量代理 :9899 + 前端 :5173）。

> **资源受限机器**：编译与全量测试请限优先级与并行度，避免卡死桌面：
> `nice -n 19 ionice -c3 env GOMAXPROCS=2 go build ./...`，
> 测试加 `-p 1`；前端构建设 `NODE_OPTIONS=--max-old-space-size=2048`，且不要与
> 全量 Go 测试同时跑。

## 二·五、发版

发布正文取自 `CHANGELOG.md` 里该版本的段落，**不是** GitHub 自动生成的提交列表：

```bash
# 1) 先在 CHANGELOG.md 的 [Unreleased] 之上补一段
#    ## [0.7.7] - 2026-11-01
#    - ……（一两句，写给人看：改了什么、要注意什么）
# 2) 打 tag 并推送，release workflow 会用 scripts/changelog-section.sh 抽这一段作为正文
git tag -a v0.7.7 -m "BLADE v0.7.7" && git push origin main v0.7.7
```

漏写段落时发布 **会失败**（脚本返回非零），这是有意的：宁可卡住一次，也不要悄悄
退回一份没人读过的提交列表。release 工作流为此加了一步十几秒的**正文预检**：缺段落时
在预检就失败，不会等前端与五平台编译跑完（约 9 分钟）才报错。本地可以先验：

```bash
./scripts/changelog-section.sh v0.7.7   # 打印正文；找不到时退出码 3
```

## 三、测试隔离

- 测试库必须独立：`BLADE_TEST_PG_DSN` 指向名字含 `test`/`acceptance` 的库。
  指向应用库会被 `guardTestDSN` 拒绝；确需如此要显式 `BLADE_TEST_ALLOW_UNSAFE=1`。
- 配置了数据库却连不上时测试**失败**而不是 Skip（`BLADE_REQUIRE_TESTS=1` /
  `BLADE_PG_DSN`）；验收用 `acceptance.sh`（自带 `BLADE_REQUIRE_ACCEPTANCE=1`）。
- 全量质量门禁：`go test -race -count=1 -p 1 ./...`、`go vet ./...`、
  `staticcheck ./...`、`gofmt -l .`；前端 `npx tsc --noEmit`、`npm test`、
  `npm run build:static`。CI 的 `go` job 覆盖 Go 侧，`web` job 覆盖前端
  （`tsc --noEmit`、`npm test`、`build:static`）；biome lint 仍不进门禁。
- 依赖漏洞：`govulncheck ./...`（CI 已设为门禁）；前端 `npm audit --omit=dev`
  （当前仅剩低危传递依赖）。`npm run check` 仍有很大的既有基线（correctness 一类
  就有 9.6 万条），暂不作为门禁——把它设成门禁等于要求先做一次全仓格式化，
  与功能改动混在一起反而更难审。CI 已覆盖 `tsc --noEmit`。
- Docker 交付：`./build.sh --docker` 产出 `dist/<arch>/blade`；
  `POSTGRES_PASSWORD=ci docker compose config` 校验 compose。
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
| 容器内页面打不开 | `docker-compose.yml` 端口映射 | 容器内必须监听容器网卡（`-addr :9898`），宿主只发布 `127.0.0.1:9898` |
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
- 收官审计默认 **block**（系统设置 →「执行与并发」→ 收官审计）：目标达成但有未豁免的
  覆盖缺口/未闭合通路/未闭合候选/未处理线索时，`prove_goal` 会被拒绝；`accept_completion_risk`
  写理由可放行，理由进报告数据缺项。`notice` 只提醒，`off` 恢复旧行为。

## 七、缓存计量排障

- 任务会话页从 `/api/exploration/tokens?task=<id>` 读取同一个数据库快照中的已完成合计、
  `snapshot_cursor`、各会话最后的 `result` 边界与最新未结算 `live` usage。SSE 仅合并快照以外的
  事件；不要把逐次 `llm_usage` 合计与整个 run 的累计 usage 再直接相加。
- `入` 已包含缓存读/写，命中率 = 缓存读 / 入。没有输入样本显示 `—`，不是 0%；缓存读大于
  输入表示计量数据异常，不应把它显示成超过 100% 的正常命中率。
- 对 Planner 全部零命中的排查，可在服务环境里设置 `BLADE_LLM_CACHE_DEBUG=1`，之后按正常部署
  流程重启（**不要另起一个连接同库的实例**）。默认关闭；不自动生成额外模型请求。
  `[cache-trace]` 只输出 trace_id（关联同次请求/响应）、session、model、system/tools/messages 的长度与 SHA256、缓存断点位置、
  `system_same/tools_same/shared_messages`，以及原始 usage 数值（含兼容网关的数值扩展字段）。
  不输出 API key、请求/响应正文、目标内容；响应扫描有固定缓冲上限，不阻塞/改写 SSE。
- 先比较同一 session 连续请求：system/tools 是否变化、断点是否存在；再比较
  `message_start` 与 `message_delta` 的 input/cache。Norma v0.5.11 起按字段合并累计快照，
  支持 start 已报 input、delta 才补 cache；不能仅凭界面的 0% 判定网关不支持缓存。
- 排障结束移除该环境变量。历史缺失用量不能凭估算回填为“上游实测缓存”。


## 八、运行体检与安全诊断

- 系统菜单的「运行体检」读取 `/api/metrics` 和 `/api/diagnostics/cache`，不会为采样发送模型或目标请求。
- 结构采样可由既有 `BLADE_LLM_CACHE_DEBUG=1` 或页面短时窗口开启。环境变量开启时页面不能关闭它；调整环境后重启。页面窗口默认 10 分钟、最多 30 分钟，不落库。
- 样本最多 256 条，仅返回近 30 分钟，每次最多 100 条；单请求 usage 最多 16 帧（保留首帧与最新帧）。只存哈希/长度/断点和白名单数值。关闭页面窗口会清空样本，计量台账不受影响。
- `llm_usage` 的逐调用 ID 与结构请求 ID 不是同一编号。原始协议用量可能是累计值或不含缓存的 input，不要直接求和或拿它当 canonical input；结构变化只是相关证据，不等于根因。
- 工作区既有文件写入要带读取返回的 `version`；同名上传通过 `/api/workspace/upload-check` 得到版本，在用户确认后随 multipart 的 `expected_versions` 提交。409 不自动强制覆盖。
- 文本使用内容哈希；超过在线编辑上限的文件使用大小/纳秒修改时间。API 写入串行化并再次复查后原子替换，但不能宣称对不遵守同一锁的外部进程提供完整跨进程 CAS。

## 黑板完整性、恢复栅栏与可选前置条件

v0.9.8 把「图上发生了什么」「这次执行是谁」「证据从哪来」三件事做成可核对的协议。

### 写协议与迁移顺序

- 运行时数据库连接由 `db/writer_protocol.go` 追加 `blade.writer_protocol=2`。事件触发器在
  缺少该 GUC 时拒绝图写入（SQLSTATE 55000）——旧二进制不能和新二进制混在同一库上跑。
  升级流程：停旧进程并等任务收尾 → 启动新进程（`OpenRuntime` 先取实例独占锁再迁移）→
  观察 `schema_fingerprint` 变化与 epoch 更新。不要并行启动新旧版本。
- `db/blackboard.sql` 负责事件表、路由列（anchors/findings/bindings 的 `exploration_id`）、
  回填和触发器；由 `schemaBatch()` 与 `schema.sql` 一起在迁移 GUC 下应用。迁移期间不捕获事件，
  结构指纹变化时 `blackboard_versions.epoch` 更新；历史只建立基线，不补造过去事件。
- 测试里用原始 `sql.Open` 建的连接也必须走 `writerProtocolDSN(dsn)`，否则会被协议检查拒绝。

### 变更流与读取完整性

- `BlackboardVersion.Cursor()` 形如 `ev1:<exploration>:<epoch>:<revision>`。`BlackboardChanges`
  校验协议、任务与 epoch；epoch 不符、序号超前或 `blackboard_events` 存在缺段时返回
  `Resync=true`，调用方必须重新取全量快照。跨任务游标直接报错。
- `blackboard_view` 的 `change_cursor` 是真实变更游标；旧 `after/next_cursor` 只跟踪节点新增，
  不要把它当事件序号解释。`blackboard.revision` 是事件水位，`blackboard.node_revision` 是旧节点上限。
- `DB.ReadSnapshot` 是只读 repeatable-read 门面：概览、常驻索引、账本、收官审计、图体检都经它
  读取，不复制实例锁。查询/Scan/`Rows.Err` 失败会累积并在快照末尾作为 error 返回，绝不当作零。
- 每个投影节带 `read_status`（`complete/unknown/truncated`、精确 `total`、`sample_count`、
  `read_more`）。窗口截断是正常现象，失败才是 unknown；模型不得把 unknown 当「没有」。
- 账本/线索/收官审计使用无窗口查询统计精确总数，示例才截断；依赖环按循环强连通分量计数。
  异步执行/租约回收/会话等待都有具名取消原因，终态写入、验证资产与阻塞原因同事务。

### 稳定身份与写权威

- `exploration_nodes.logical_session_id` 首次领取时确定（默认沿用槽位，`BLADE_WORKER_PER_INTENT_SESSION=1`
  才每意图独立），之后不因暂停、跨槽或配置变化重算；`execution_generation` 每次真正重新领取加一。
- 服务端构造 `db.WriteAuthority`（任务/探索/意图/owner/代次/逻辑会话），Worker 的 RunInfo 与
  ToolSet 使用它；发现记录器、异步执行提交也携带它。写入、续租、语义结论、结算在事务内核对
  `state=running` 且 owner/代次/会话一致，否则 `ErrStaleExecution`。
- 托管长任务把代次与逻辑会话写进 `tool_executions`；完成回调只唤醒仍匹配当前 run 的任务。
  看门狗按 `ExpiredLease.Generation` 取消和回收；`detachWork(id, *workExecution)` 只清理自己。
- 领取代次只在真正 claim 时增加；模型重试仍是同一代。旧代的图写、结算、续租、cancel/detach
  全部拒绝且不发布成功事件。宿主的记录器与无记录器路径同样校验，管理/人工入口不因「没有
  authority」被当成任意 worker 的逃生口。

### 证据与可选前置条件

- `record_fact` 的 `evidence_meta` 是可选元信息：时间用 RFC3339；`source_refs` 只接受本任务或
  直接关联任务的 activity/execution/finding_binding id；缺失时间为未知，不补造。
- `contradicts` 与 `supersedes` 只允许本任务事实为端点；替代检查传递闭包避免成环。它们只产生
  读取标记（`EvidenceStates`），不改变节点 state、不判真假；删除关系时写 `blackboard_operations`
  因果记录（`operation_id`/`reason_code`），完整理由留在库内供审计，不复制进模型上下文。
- `add_intent.requires[]` 为 AND，类型限 fact/capability/credential，可引用节点或 claim_key。
  判定 SQL 与 Frontier/计数/领取共用（`blackboard_intent_ready`/`blackboard_requirement_issues`），
  领取事务先按 id 升序 `FOR SHARE` 锁相关节点再重查，故并发失效的竞争更新会让领取失败而不是
  按旧快照放行。缺条件是「暂时不可领」，不是负面结论；不填字段的旧意图完全沿用旧规则。
- 凭据目标可带 `observed_at`/`valid_until`/`validation_status`；历史验证保留，未知时间不冒充
  新鲜；要求实测的条件必须指定目标，且按目标的时间判定。
