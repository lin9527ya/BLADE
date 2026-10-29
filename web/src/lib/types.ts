// BLADE domain model — types used across the UI.
// Derived from the functional spec (section 7: 关键数据形状).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout" | "policy_blocked"; // P20：探索策略内已无可行方向而收尾（不是超时——改策略/加目标即可复活）
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // 可选任务名称;空/缺省=未命名,展示时回退到描述
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // 已登记漏洞数(按严重度分档)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // 资产覆盖度功能开关(创建时定,默认开)；false=不计算/不展示覆盖度
  // P20 探索策略限制摘要（列表角标用）；明细走 GET /api/tasks/{id}/policy。
  policy_techniques?: string[] | null;
  policy_writes?: string[] | null;
  policy_custom?: number;
  policy_max_intents?: number;
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // 预设分类；null/缺省=无
  intercept_rules?: AssetInterceptRuleInput[]; // 预设的任务级拦截/允许规则
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // 归属公司资产 id；空=未归属
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// 力导向「资产覆盖图」的一个节点。key 唯一：资产="a:<id>"、公司="c:<id>"、
// 无资产行的根域名="r:<domain>"。in_scope=false 的是仅用于连线的灰色上下文节点。
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// 某资产在本任务探索图里关联到的意图/事实/发现（覆盖图节点抽屉用）。
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// 任务测试范围的一条（覆盖度分母 + 授权边界）。
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // 后端 JOIN companies 解析，仅 kind=company 有值
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// 新增企业时提交的结构化资产范围规则。
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// 资产范围写入的结果。errors 是本次提交里不合法的行；warnings 是与本次提交无关、
// 但会让归属结果不符合预期的既有数据问题（如 ip 字段存了主机名的资产）。
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// 公司资产范围规则的一条（归属唯一真值来源）。
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // kind=domain 时有值
  net?: string; // kind=ip|cidr 时有值
  value?: string; // kind=icp|keyword 时可能由后端直接返回
  raw: string; // 原始用户输入，用于显示和回填
  reason?: string;
}

// 企业：type=company 的资产节点 + 图标 + 资产计数 + 资产范围规则。
export interface Company {
  id: number;
  name: string;
  logo?: string; // 远程图标 URL；空则前端用名称首字母
  asset_count: number;
  scope?: ScopeRow[];
}

// CSV 资产导入。预览与应用共用同一份解析器（server/asset_import.go）——预览里的
// new/existing 与应用后的 created/merged 同出一源，所以两边数字必然对得上。
export interface AssetImportNote {
  line: number;
  value: string;
  reason: string;
}

export interface AssetImportSample {
  line: number;
  value: string;
  type: string; // root_domain | subdomain | ip | service
  existing: boolean;
}

export interface AssetImportPreview {
  total: number;
  by_type: Record<string, number>;
  new: number;
  existing: number;
  duplicates: number;
  unrecognized: AssetImportNote[]; // 进不去的（含行号与原因）
  warnings: AssetImportNote[]; // 进去了但有折扣的（如 host:port 的端口未登记）
  samples: AssetImportSample[];
  samples_truncated?: number;
  parse_error?: string;
  task_id: number;
}

export interface AssetImportResult {
  created: number;
  merged: number;
  by_type: Record<string, number>;
  unrecognized: AssetImportNote[];
  warnings: AssetImportNote[];
  errors?: { line: number; value: string; error: string }[];
  parse_error?: string;
  task_id: number;
}

// ---- Exploration graph (per task) ----
export type ExploreKind =
  | "task"
  | "begin"
  | "goal"
  | "intent"
  | "fact"
  | "finding"
  | "hint"
  | "digest"
  // 战果：据点（已攻陷的通路）与凭据（读到的口令/哈希/密钥/令牌）。
  | "foothold"
  | "credential"
  // 检测验证：一次进攻动作有没有被检出。
  | "detection"
  // 遗留物（P43）：我们**留在目标上**的东西——webshell、上传的文件、建的账号、计划任务。
  // 它跟其它战果是反的：那些记"我们拿到了什么"，它记"我们给客户留下了什么"。
  | "artifact"
  // 目标侧痕迹：测试动作可能在目标上留下的可检出信号（日志/EDR/网络/应用/本机记录）。
  | "footprint"
  // AD 对象：BloodHound/SharpHound 采集回来的域内对象（用户/组/机器/域/OU/GPO），
  // 靠 payload.category 分类——一种 kind 装六类对象，理由见 server/ad_import.go 的开头。
  | "ad_object"
  // 云对象：AWS IAM / K8s RBAC 枚举结果（账号/用户/角色/策略/服务账号…），
  // 同样靠 payload.category 分类；与 AD 共用攻击路径算法。
  | "cloud_object";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState =
  | "open"
  | "running"
  | "paused"
  | "done"
  | "negative"
  | "out-of-scope"
  | "blocked"
  | "exhausted"
  | "no_output"
  | "stopped"
  | "deleted"
  | "idle"; // 固定槽位空闲：等待下一个意图
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel =
  | "spawns"
  | "derived_from"
  | "yields"
  | "proves"
  | "covers"
  | "uses"
  // AD 域内拓扑（P32）：成员 / 本机管理员 / 有会话 / 权限（具体权限名在边的 attrs.rights 里）。
  | "member_of"
  | "admin_to"
  | "session_on"
  | "can"
  // 串行链：后一条意图要等前一条完成/否定后才能领。
  | "depends_on"
  // 凭据/据点能到达的主机（report_credential 建立）。
  | "reaches";

// ---- AD 攻击路径导入（P32） ----

export interface ADImportFile {
  name: string;
  json: string;
}

export interface ADImportPreview {
  task_id: number;
  objects: Record<string, number>;
  edges: Record<string, number>;
  nodes_new: number;
  nodes_existing: number;
  edges_new: number;
  edges_existing: number;
  highvalue: { name: string; category: string; stub?: boolean; tags?: string[] }[];
  warnings: string[];
  stubs: { name: string; category: string; why: string }[];
  skipped_rights?: Record<string, number>;
  unresolved_edges?: number;
  unknown_objects?: number;
  /** 云上导入才有：未建模的东西（服务主体、Namespace…）如实计数。 */
  skipped?: Record<string, number>;
}

export interface ADImportResult {
  task_id: number;
  nodes_created: number;
  nodes_updated: number;
  edges_created: number;
  edges_merged: number;
  objects: Record<string, number>;
  edges: Record<string, number>;
  warnings: string[];
  edges_unresolved?: number;
  skipped_rights?: Record<string, number>;
  skipped?: Record<string, number>;
  failures?: number;
  errors?: string[];
}

// ---- 停机开关与作业时间窗的执行状态（P35） ----

export interface KillSwitchEvent {
  on: boolean;
  at: string;
  by?: string;
  reason?: string;
  tasks_paused?: number;
}

export interface KillSwitchReceipt {
  on: boolean;
  at?: string;
  by?: string;
  reason?: string;
  tasks_paused?: number;
  history?: KillSwitchEvent[];
}

export interface ScopeWindowState {
  in_window: boolean;
  window: string;
  at: string;
  paused_now: number;
  paused_total: number;
  last_closed_at?: string;
  last_opened_at?: string;
  parse_error?: string;
}

export interface ScopeEnforcement {
  kill_switch: KillSwitchReceipt;
  kill_switch_forced_by_env: boolean;
  window: ScopeWindowState;
  paused_tasks: number;
}

// ---- 事前可检出性知识库（P38，只读一览） ----

export interface OpsecKnowledgeEntry {
  id: string;
  title: string;
  where: string[];
  signals?: string[];
  exposure: string;
  note: string;
}

export interface OpsecKnowledgeResponse {
  entries: OpsecKnowledgeEntry[];
  note: string;
}

// ---- 作业时间线（P37） ----

export type TimelineKind = "execution" | "approval" | "finding" | "loot" | "artifact" | "footprint" | "detection" | "note" | "step";

export interface TimelineEvent {
  at: string;
  kind: TimelineKind;
  title: string;
  detail?: string;
  state?: string;
  /** state 的人话（服务端一起下发：交付件与界面不该各自再翻译一遍）。 */
  state_label?: string;
  ref_type?: string;
  ref_id?: number;
}

export interface TimelineResponse {
  task_id: string;
  from?: string;
  to?: string;
  kinds: TimelineKind[];
  events: TimelineEvent[];
  /** 被截掉的更早事件数（如实报出："只显示了一部分"要让人看得见）。 */
  truncated?: number;
  counts: Record<string, number>;
  /** 分页与排序（界面用；导出仍取完整时间线）。 */
  page?: number;
  size?: number;
  total?: number;
  order?: "asc" | "desc";
  has_more?: boolean;
}

/** 凭据明文的受控入口返回（P36）：明文只在人主动点开时出，且服务端会为此留痕。 */
export interface CredentialReveal {
  id: number;
  kind: string;
  username: string;
  secret: string;
  fingerprint: string;
  summary: string;
  revealed_by: string;
  note?: string;
}

export interface ADHop {
  from: string;
  rel: string;
  to: string;
  rights?: string[];
  note?: string;
}

export interface ADPath {
  start: string;
  target: string;
  target_tags?: string[];
  steps: ADHop[];
}

export interface ADPathsResponse {
  paths: ADPath[];
  stats: {
    objects?: Record<string, number>;
    edges?: Record<string, number>;
    nodes?: number;
    highvalue?: number;
  };
  starts: { id: number; name: string; owned?: boolean }[];
  start_id?: number;
}

export interface TaskNode {
  /** 凭据节点才有：明文不进界面的前提下，用指纹指认一条凭据（P36）。 */
  fingerprint?: string;
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // 意图假删除(state='deleted')时的删除原因
  // 意图在本任务内的创建序号（1-based）。意图 id 是全局序列（任务 2 会从 21 继续），
  // 界面上的 "Worker #N" 用这个序号，而不是 id。
  worker_seq?: number;
  // 当前绑定的固定执行槽位（owner=work#N）；0/缺省=未绑定。
  slot_no?: number;
}

// GET /api/tasks/{id}/worker-slots — fixed N execution slots (sessions reused
// across intents). state is running|paused|open when an intent is bound, idle otherwise.
export interface WorkerSlot {
  slot: number;
  name: string;
  session: string; // "slot:<n>"
  state: string;
  intent_id?: string;
  summary?: string;
  started_at?: string;
  last_activity?: string;
}

// 播报板一页:按创建顺序分页的节点 + 这一页涉及的边 + 边另一端的节点(refs,按 id 索引),
// 这样每条播报都能说清「从哪来、产出了什么」,而不用把整张图拉下来。
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // 节点 id → 该节点锚定的资产(播报板展开时顺带展示,含本页节点与其邻居)。
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// 目标管理卡片用的目标(后端已把 payload 拆成 text/vulnclass)。
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// 战略提示卡片用的一条提示。active=待采纳，consumed=规划者已采纳。
export interface TaskHint {
  id: string;
  text: string;
  state: "active" | "consumed" | string;
  origin?: string;
  ts: string;
  /** planner 采纳/判断不适用时写下的理由（consume_note），仅 consumed 时有。 */
  note?: string;
  /** 这条提示锚定的资产（覆盖缺口一键补测会带上）。 */
  asset_ids?: number[];
}

/** 任务总览「遗留物」卡片用的一条台账（我们留在目标上的东西）。 */
export interface TaskArtifact {
  id: string;
  state: string;
  state_label: string;
  kind?: string;
  kind_label?: string;
  summary: string;
  detail?: string;
  host?: string;
  locator?: string;
  url?: string;
  cleanup?: string;
  evidence?: string;
  confidence?: string;
  removed_evidence?: string;
  removed_note?: string;
  ts: string;
}

/** 目标侧痕迹：可能在目标上留下的可检出信号（日志/EDR/网络/应用/本机记录）。 */
export interface TaskFootprint {
  id: string;
  state: "open" | "unknown" | "accepted" | "cleaned" | "not_found";
  state_label: string;
  source?: "predicted" | "observed";
  source_label?: string;
  category?: string;
  category_label?: string;
  summary: string;
  detail?: string;
  where?: string[];
  signals?: string[];
  exposure?: "low" | "medium" | "high";
  exposure_label?: string;
  host?: string;
  locator?: string;
  cleanup?: string;
  evidence?: string;
  confidence?: string;
  opsec_note_id?: string;
  occurrences?: number;
  cleanup_intent_id?: number;
  removed_evidence?: string;
  removed_note?: string;
  accepted_reason?: string;
  not_found_evidence?: string;
  not_found_note?: string;
  ts: string;
}

// 约束管理卡片用的操作约束(allow=允许 / deny=禁止)。
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
  /** P18：这条约束当前编译出几条任务级拦截规则（0/缺省 = 尚未生效，仍只是提示词约束）。 */
  compiled_rules?: number;
}

// ---- P20 探索策略 ----

/**
 * 自定义禁止条目（策略的逃生舱）：词表覆盖不到时操作者手写一条。
 * Regex=false 走关键词（ASCII 词边界 / 中文子串，与词表同款否定语境豁免）；
 * Regex=true 按正则原文匹配（不套否定豁免）。Layer="command" 时只编译进命令门，不判意图措辞。
 */
export interface CustomRule {
  label?: string;
  pattern: string;
  regex?: boolean;
  layer?: "both" | "command";
}

/** 一份解析完成的策略（执行点读的就是它）。0 = 不限。 */
export interface ExplorationPolicyEffective {
  version: number;
  source: string; // global_default | task
  techniques: string[];
  forbidden_writes: string[];
  custom?: CustomRule[] | null;
  max_intents_per_round: number;
  max_open_intents: number;
  max_intents_total: number;
}

/** 任务自己的覆盖层：字段缺省 = 继承，显式空数组 = 本任务不禁该项，0 = 不限。 */
export interface ExplorationPolicyOverride {
  techniques?: string[];
  forbidden_writes?: string[];
  custom?: CustomRule[];
  max_intents_per_round?: number;
  max_open_intents?: number;
  max_intents_total?: number;
}

/** 词表项（设置页/策略卡渲染"禁了什么、为什么"）。 */
export interface PolicyTechnique {
  id: string;
  label: string;
  hint: string;
  words: string[];
}
export interface PolicyWriteItem {
  id: string;
  label: string;
  hint: string;
  words: string[];
  patterns: string[];
}

/** 策略编译出的规则草案（命令门；落库前）。 */
export interface PolicyRuleDraft {
  name: string;
  match_target: string;
  match_type: string;
  pattern: string;
  action: string;
  message: string;
  priority: number;
  item_id: string;
  item_label: string;
}

/** 近期被拒的一条意图（模型想做什么、被哪条策略拦）。 */
export interface PolicyRejectNote {
  summary: string;
  item_label: string;
  item_id: string;
  matched: string;
}

export interface TaskPolicy {
  effective: ExplorationPolicyEffective;
  override: ExplorationPolicyOverride;
  global: ExplorationPolicyEffective;
  global_version: number;
  custom: boolean; // 本任务自己配过（false = 创建时抄的全局默认快照）
  intents_used: number;
  intents_total: number;
  rule_count: number; // 命令门当前落库的规则数
  custom_limit: number; // 自定义条目的条数上限（UI 据此禁用"添加"）
  recent_rejects: PolicyRejectNote[] | null;
}

export interface ExplorationPolicyDefault {
  effective: ExplorationPolicyEffective;
  version: number;
  override: ExplorationPolicyOverride;
  techniques: PolicyTechnique[];
  writes: PolicyWriteItem[];
  custom_limit: number;
  custom_pattern_limit: number;
}

// ---- P21 情报/测绘 ----

/** 一次情报查询的台账行（审计 + 用量）。 */
export interface IntelQueryRow {
  id: number;
  provider: string;
  expr: string;
  total_hits: number;
  returned: number;
  task_id?: string;
  intent_id?: number;
  worker?: string;
  error?: string;
  created_at: string;
}

export interface IntelQueryList {
  queries: IntelQueryRow[] | null;
  used_today: number;
  daily_quota: number;
}

/** 「测试连接」的应答（ok=false 时 error 是人话；ok=true 时 info 是源自己的账号信息）。 */
export interface IntelTestResult {
  ok: boolean;
  error?: string;
  info?: Record<string, unknown>;
  used_today?: number;
  daily_quota?: number;
}

// ---- P23 公司级知识 ----

/** 一条公司级知识（跨任务只读背景；来源可回溯，废弃保留痕迹）。 */
export interface CompanyKnowledge {
  id: number;
  company_id: number;
  company_name?: string;
  kind: "fact" | "conclusion" | "asset_hint" | "playbook_hint";
  summary: string;
  detail?: string;
  source_task_id?: string;
  source_node_id?: number;
  confidence?: "high" | "medium" | "low" | "";
  status: "active" | "deprecated";
  created_at: string;
  deprecated_at?: string;
  deprecated_reason?: string;
}

// ---- 指纹打法库（全局）----

/** 指纹：技术栈 / favicon / 服务端口 / 页面标题。匹配是启发式打分，不是精确相等。 */
export interface PlaybookFingerprint {
  technologies?: string[];
  favicon_mmh3?: string;
  service_name?: string;
  port?: number;
  page_title?: string;
}

/** 一张「目标指纹 → 攻击手法」卡片。 */
export interface Playbook {
  id: number;
  fingerprint: PlaybookFingerprint;
  fingerprint_key: string;
  technique_key: string;
  title: string;
  summary: string;
  steps: string;
  evidence: string;
  confidence: "high" | "medium" | "low" | "";
  source_task_id?: string;
  source_node_id?: number;
  source_company?: string;
  status: "active" | "deprecated";
  deprecated_reason?: string;
  revision: number;
  uses: number;
  successes: number;
  failures: number;
  last_outcome?: string;
  last_note?: string;
  last_used_at?: string;
  last_verified_at?: string;
  created_at: string;
  updated_at: string;
}

/** 全局 CVE 库条目：人工录入的漏洞详情 + 指纹/产品/版本 + 可选利用脚本。 */
export interface CVE {
  id: number;
  cve_id: string;
  title: string;
  severity: Severity | "";
  cvss_score: number;
  vendor?: string;
  description?: string;
  affected_products: string[];
  version_constraint: string;
  fingerprint: PlaybookFingerprint;
  /** FOFA / Hunter / Shodan 等测绘语法，一行一条。 */
  recon_syntax?: string;
  probe_guide?: string;
  refs?: string[];
  exploit_script?: string;
  exploit_language?: string;
  exploit_filename?: string;
  exploit_usage?: string;
  exploit_sha256?: string;
  exploit_bytes: number;
  has_exploit: boolean;
  status: "active" | "deprecated";
  deprecated_reason?: string;
  revision: number;
  uses: number;
  executions: number;
  last_used_at?: string;
  last_executed_at?: string;
  created_at: string;
  updated_at: string;
}

/** 新建/编辑 CVE 时提交的字段。 */
export interface CVEInput {
  cve_id: string;
  title: string;
  severity: Severity | "";
  cvss_score: number;
  vendor?: string;
  description?: string;
  affected_products: string[];
  version_constraint: string;
  fingerprint: PlaybookFingerprint;
  /** FOFA / Hunter / Shodan 等测绘语法，一行一条。 */
  recon_syntax?: string;
  probe_guide?: string;
  refs?: string[];
  exploit_script?: string;
  exploit_language?: string;
  exploit_filename?: string;
  exploit_usage?: string;
}

export interface CVEListResponse {
  cves: CVE[];
  total: number;
}

/** 报告导入预览：draft 与手工录入同一形状，保存时仍走 CVEInput。 */
export interface CVEImportSkipped {
  cve_id?: string;
  title?: string;
  reason: string;
}

export interface CVEImportPreview {
  drafts: CVEInput[];
  skipped: CVEImportSkipped[];
  warnings: string[];
  count: number;
  note?: string;
}

// ---- P22 计划任务 ----

/** 结构化频率（刻意不是 cron 字符串：覆盖巡检需要，且没有方言/边界歧义）。 */
export interface ScheduleFrequency {
  kind: "every_minutes" | "every_hours" | "daily_at" | "weekly_at";
  every_minutes?: number;
  every_hours?: number;
  at?: string; // "HH:MM"
  weekday?: number; // 0=周日 … 6=周六
}

/** 到点要建的任务规格（与建任务表单同形状的子集；计划任务存整份，不引用模板）。 */
export interface ScheduleTaskSpec {
  name?: string;
  description: string;
  goal: string;
  category_id?: number;
  template_id?: number;
  llm_profile_ids?: number[];
  company_ids?: number[];
  timeout_seconds?: number;
  plan_heartbeat_seconds?: number;
  coverage_enabled?: boolean;
  seed_first_intent?: boolean;
}

export interface TaskSchedule {
  id: number;
  name: string;
  enabled: boolean;
  task_spec: ScheduleTaskSpec;
  frequency: ScheduleFrequency;
  frequency_text: string;
  next_run_at?: string;
  last_run_at?: string;
  last_task_id?: string;
  last_error?: string;
  created_at: string;
}

/** P19 批量交付计划：一个任务全部发现的清单 + 证据门 + 疑似重复分组。 */
export interface TaskSrcBatchEntry {
  id: number;
  title: string;
  severity: string;
  severity_cn: string;
  vuln_class: string;
  target: string;
  status: string;
  steps: number;
  endpoints: number;
  warnings: string[] | null;
  fingerprint?: string;
  duplicate_group?: number;
  included: boolean;
  /** 未进包的原因（状态不符 / 同指纹只交代表项）；进包的条目为空。 */
  skip_reason?: string;
}

export interface TaskSrcBatchPlan {
  task_id: string;
  task_name: string;
  goal: string;
  generated_at: string;
  entries: TaskSrcBatchEntry[] | null;
  duplicate_groups: number[][] | null;
  warnings: string[] | null;
  included: number;
  skipped: number;
}

/** P18 约束编译：一条候选拦截规则 + 它在最近真实调用里的命中预览。 */
export interface ConstraintRuleDraft {
  name: string;
  match_target: string;
  match_type: string;
  pattern: string;
  action: string;
  message: string;
  priority: number;
  surface: string;
}

export interface ConstraintScopeDraft {
  kind: string;
  value: string;
  note: string;
}

export interface ConstraintCompilePreview {
  pattern: string;
  matched: number;
  total: number;
  samples: string[];
}

export interface ConstraintCompileResult {
  constraint_id: number;
  kind: ConstraintKind;
  text: string;
  rules: ConstraintRuleDraft[] | null;
  scope: ConstraintScopeDraft[] | null;
  note: string;
  preview: ConstraintCompilePreview[] | null;
  scopes?: { id: number; kind: string; domain?: string; value?: string; source: string; reason: string }[] | null;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// 漏洞处置状态:待处理 / 处理中 / 已确认 / 已处理 / 已修复 / 误报 / 忽略 / 重复 / 风险接受 / 已撤回。
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted"
  | "retracted"
  // ruled_out = 反证闭合（counterevidence 点名控制点）；open_proof_gap = 存疑待补证（明写缺口）。
  | "ruled_out"
  | "open_proof_gap";

// FindingAsset 是一个漏洞绑定的资产(已在后端预渲染 label)。
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // 独立 findings 表的行 id,状态更新的句柄(任务内旧节点可能缺失)
  vulnclass: string;
  name?: string; // 漏洞名称;为空时展示回退到 vulnclass
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // 详细报告(Markdown);仅详情接口返回,列表为空
  confidence?: string; // 置信度标定 high/medium/low;空=未标定
  counterevidence?: string; // ruled_out 的反证（点名控制点）
  proof_gap?: string; // open_proof_gap 的缺口说明
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage 是发现列表的服务端分页响应。
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // 可选任务名称;空/缺省=未命名
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats 是发现全表聚合(统计卡 + 漏洞类型下拉),服务端计算,不受分页影响。
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption 是发现页「按任务」筛选下拉的一项:有漏洞的任务(描述为空表示任务已删除,
// 前端回退展示 id)及其漏洞条数。
export interface FindingTaskOption {
  id: string | number;
  name?: string; // 可选任务名称;空/缺省=未命名
  description: string;
  count: number;
}

// FindingQuery 是发现列表分页/筛选/排序参数。
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // 任务 id;"all"/空 = 不按任务筛选
  query?: string;
  sort?: "severity" | "time";
  // 资产树节点 key;选中一个节点 = 选中它的整棵子树。空 = 不按资产筛选。
  assetScope?: string;
}

// ---- Findings by asset (资产视图) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode 是资产树的一个节点。key 形如 a:<id>(资产)、c:<id>(企业)、
// r:<domain>(库里没有资产行的根域名)、__none__(未关联资产)。
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // 直接挂在该资产上的发现数
  total: number; // 含子孙、按发现去重
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET 与后端 db.FindingUnassignedAsset 对应。
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request" // user-approval request from the intercept layer
  | "execution_pending" // P13: 收笔时托管长任务仍在跑，意图暂不判完
  | "execution_done" // P13: 托管执行收束，意图退回待领续跑
  | "policy_reject" // P20: 意图被任务探索策略硬拒（不落图，但时间线留痕）
  | "policy_update" // P20: 策略保存/清空/按新策略清理未领取意图
  | "intel_query" // P21: 情报/测绘查询（表达式 + 命中数 + 完整结果落盘路径）
  | "intercept_wait"; // 无超时审批的等待中标记（同时是意图租约的心跳，防止被看门狗回收）

// ChatAttachment 是一次上传的文件:path 相对该会话/任务工作目录(即 agent 的 CWD)。
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // 绝对路径(scope=staging 暂存上传时返回;建任务前把它写进描述)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  slot_no?: number; // fixed worker slot (absent on pre-upgrade rows / main / planner)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 调度，仅自定义 agent) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // 定时:每 N 秒(0=不定时)
  on_finding: boolean; // 任意任务发现 finding 时触发
  on_goal_met: boolean; // 任意任务达成目标时触发
  on_task_timeout: boolean; // 任意任务超时时触发
  on_tool_call: boolean; // 选中工具被调用(执行完成)时触发
  on_task_create: boolean; // 任意任务被创建时触发
  interval_message: string; // 各触发条件的独立用户消息
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // on_tool_call 选中的工具 key(至少一个)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus =
  | "running"
  | "paused"
  | "done"
  | "negative" // 已测(未发现)：探针发出且有回包、回包明确否定该假设
  | "out-of-scope" // 范围外：超出任务授权范围
  | "blocked"
  | "exhausted"
  | "no_output" // 跑完了却什么都没写回：按未覆盖处理，可重派
  | "pending"
  | "stopped"
  | "deleted"
  | "idle"; // 固定槽位空闲：等待下一个意图

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// 批量改分类的逐任务结果。失败只可能是任务已被删除，分类本身的写入是原子的。
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  worker_no?: number; // 任务内序号（"Worker #N"）；缺省时回退到 intent_id
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
  slot?: number; // fixed worker slot (worker sessions only)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  req_len?: number;
  resp_len: number;
  /** complete | partial | legacy；partial = 大正文落盘失败，只剩预览。 */
  req_state?: string;
  resp_state?: string;
  req_stored_len?: number;
  resp_stored_len?: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface ProxyEgressResult {
  proxy: string;
  ip?: string;
  leaked?: boolean;
  error?: string;
}

export interface ProxyPoolHealth {
  proxy: string;
  ok: boolean;
  latency_ms?: number;
  checked_at?: string;
  error?: string;
}

export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent 自动绑定流量证据，默认关闭；不影响人工绑定
  agent_loot_exec: boolean; // 战果直接执行（loot_exec）：用登记过的凭据连目标，默认关闭
  agent_auto_retest: boolean; // 自动复测：每条新漏洞启动一次独立复测会话，默认关闭（有 LLM 开销）
  oast_enabled: boolean; // 带外确认（OAST）总开关，默认关闭
  oast_token_set: boolean; // OAST 服务 token 是否已配置（值不回显）
  oast_token?: string; // 只写：仅在 PUT 时用于保存/清除 token
  oast_domain: string; // OAST 服务域名（默认 requestrepo.com）
  opsec_noise_level: string; // 噪声档位：normal（默认）| quiet（追加潜行纪律）
  llm_record: boolean; // LLM 录制开关（默认关）；关闭时不记录任何 LLM 调用
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // 独立出口代理(http/https/socks5)，用于访问搜索端点；与记录流量的 MITM 代理无关。空=直连。
  web_search_proxy?: string;
  // 全局出口代理（兼容旧字段：代理池第一项）。空=未配置。
  global_proxy?: string;
  // 代理池：每行一个 http/https/socks5 URL（Clash 混合端口即可）。配了候选时若全部不可用，
  // 出口会被阻断而不是直连（不泄漏真实 IP）。
  global_proxy_pool?: string;
  proxy_health_url?: string;
  proxy_health_timeout_seconds?: number;
  proxy_health_interval_seconds?: number;
  proxy_fail_threshold?: number;
  proxy_switch_cooldown_seconds?: number;
  // 运行时状态（只读）。
  proxy_active?: string;
  proxy_blocked?: boolean;
  proxy_status?: ProxyPoolHealth[];
  proxy_egress_ip_url?: string;
  // ---- P21 情报源（先接 FOFA）----
  // key 与 web search 同款：只写不读，读侧只给 fofa_key_set 布尔。
  fofa_key_set?: boolean;
  fofa_key?: string; // write-only：PUT 时传，"" = 清空
  fofa_email?: string; // FOFA 老账号需要；新账号留空
  fofa_base_url?: string; // 空 = 官方端点；企业版/自建可改
  fofa_proxy?: string; // 独立出口；空 = 跟随全局代理
  intel_daily_quota?: number; // 平台侧每日调用上限；0 = 不限
  intel_used_today?: number; // 只读回显：今日已用次数
  // ---- P24 执行资源限额（0 = 该轴不限）与外部 MCP 守卫 ----
  exec_cpu_seconds?: number;
  // P28 存储清理：每个分类的保留天数（0/未设 = 不清理）。
  storage_retention_tasks_days?: number;
  storage_retention_sessions_days?: number;
  storage_retention_drafts_days?: number;
  storage_retention_transcripts_days?: number;
  storage_retention_binary_days?: number;
  storage_retention_llm_usage_days?: number;
  storage_retention_activity_days?: number;
  storage_retention_server_logs_days?: number;
  storage_retention_llm_records_days?: number;
  storage_retention_llm_record_bodies_days?: number;
  storage_retention_traffic_days?: number;
  // 自更新用的 GitHub token（私有发布源）：只回布尔"是否已配"，值不回显。
  update_token_set?: boolean;
  update_github_token?: string;
  exec_memory_mb?: number;
  exec_max_procs?: number;
  exec_max_active?: number;
  /** 单条托管执行的最大存活分钟数（默认 120，0 = 不限）。超龄执行会被回收。 */
  exec_max_age_minutes?: number;
  mcp_max_concurrent_per_server?: number;
  mcp_breaker_threshold?: number;
  mcp_breaker_cooldown_sec?: number;
  python_interpreter?: string; // 自定义脚本工具的 python 解释器路径(空=运行时检测)
  workers?: number; // 并发工作 agent 数(默认3)；对之后启动的任务生效
  // 任务级 token 预算(输入+输出合计)：到 70/85/95% 各向 planner 投递一次收敛提示，
  // 100% 时任务进入收尾(停派新意图 → drain → 终局报告)。0 或空 = 关闭。
  task_token_budget?: number;
  // 任务并发上限:同时「运行中」的任务数上限。关闭=不限;开启后新建任务超限则排队,有空位自动启动。
  task_concurrency_enabled?: boolean; // 默认 false
  task_concurrency_limit?: number; // 开启后默认 5
  // LLM 轮询(故障转移)。默认关；开启后「未指定模型」的 agent 在当前配置不可用
  // （余额不足/key 失效/限流/服务异常）时自动切到下一个配置。
  llm_pool_enabled?: boolean; // 默认 false
  // 绑定了指定配置的 agent/任务失败时是否也回落到轮询链。默认 false = 绑定即独占。
  llm_pool_bind_fallback?: boolean;
  // 操作约束注入范围(默认都开):把任务的 allow/deny 约束拼进对应 agent 的系统提示。
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // 实验功能:noa 模型驱动上下文压缩(默认关)。开启后平台接入的四类 agent(planner/
  // worker/主 agent/对话)由 noa 接管上下文压缩,取代内置 compaction;每 run 读一次,对
  // 之后启动的 run 生效。
  noa_compaction?: boolean;
  // 授权范围（RoE）：空间门模式 + 作业时间窗（空 = 不限时）。
  // intercept_scope_mode 回的是**生效值**（env 优先于 setting）；intercept_scope_mode_env
  // 说明 env 是否把它压住了——页面据此提示，否则用户改了没反应会莫名其妙。
  intercept_scope_mode?: string;
  intercept_scope_mode_env?: boolean;
  intercept_scope_hours?: string;
  /** 面向目标的产物语言（P41）：zh 默认不约束 / en 一律 ASCII 英文。与界面语言无关。 */
  artifact_language?: "zh" | "en";
  /**
   * 凭据尝试预算（P43）：窗口（30 分钟）内对同一 (目标, 账号) 允许的尝试次数，0 = 不限。
   * 超预算后按 credential_attempt_budget_action 处置。回的是**生效值**（缺省 3）。
   *
   * 它保护的是**客户的账号**：域账号被锁会让业务中断，与内置规则拒 rm -rf / DROP TABLE
   * 同属"别把目标搞坏"。计的是**尝试次数**不是失败次数——shell 层分不出成败，宁可多问一句。
   */
  credential_attempt_budget?: number;
  /** 超预算后的裁决：allow / ask（默认）/ deny。 */
  credential_attempt_budget_action?: "allow" | "ask" | "deny";
  /** 界面显示名（P42）：空 = 内置 "BLADE"。只覆盖运行时标题；静态 HTML 的 title 是构建期常量。 */
  site_name?: string;
  /** 允许跨源访问 API 的来源白名单（P42，逗号分隔；空 = 不发 CORS 头）。"*" 才是全开放。 */
  cors_allowed_origins?: string;
  /** P14 领域压缩模板的 DB 覆盖（只读回显；设置页不暴露，写入走 API/DB）。空 = 用内置 7 段模板。 */
  compaction_prompt?: string;
  // ---- 漏洞 IM 推送（渠道本身是独立资源，见 /api/notify/*，这里只有三项全局配置）----
  notify_enabled?: boolean; // 推送总开关，默认开；用于维护期一键止血
  notify_public_base_url?: string; // 漏洞详情回链的外部访问地址；空=消息不带回链
  notify_digest_interval_min?: number; // 汇总模式周期（分钟），默认 30
}

// ---- 漏洞 IM 推送 ----

// NotificationFilter 是渠道的过滤条件，字段全部可选，缺省即不过滤。
// 后端对所有字段都不加校验：配置畸形时按「命中」处理（宁可多推不可漏推）。
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // 空=不限；非空则要求与漏洞所属任务有交集
  asset_ids?: number[]; // 空=不限；非空则要求与漏洞锚定资产有交集
  vulnclass_include?: string[]; // 空=全收；非空则要求漏洞类型命中任一关键词（大小写不敏感子串）
  vulnclass_exclude?: string[]; // 命中任一关键词即排除（排除优先于包含）
  on_status_change?: boolean; // 是否也接收漏洞处置状态变更事件
}

// NotificationChannel 是一个渠道实例。config 的字段随 kind 而异，
// 且凭据字段在读取时被替换成 "__masked__" 开头的掩码值——原样回传即表示「不改」。
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys 由后端按渠道类型给出，前端据此渲染密码框与「留空即不改」提示，
  // 不硬编码任何渠道知识。
  secret_keys: string[];
}

// NotificationKind 是 /api/notify/meta 返回的渠道类型元数据。
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery 是一条投递记录，用于投递历史与失败重发。
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // 思考开关(thinking.type): ""=不发送(默认) | "disabled"=关闭 | "enabled"=开启
  thinking_type?: string;
  // 思考强度: ""=不发送(默认) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // 轮询顺位：越大越先被选中。激活配置恒为链首，与本值无关。
  priority?: number;
  // true = 不作为故障转移目标（仍可被 agent/任务显式绑定使用）。
  pool_exclude?: boolean;
  // true（默认）= 流式(SSE) | false = 真·非流式(stream:false，一次性返回)。
  streaming?: boolean;
  // 单次回复的输出上限(token)。0 = 不发送该字段，由服务端默认值决定。
  // 注意与 context_window_k 区分：后者是模型总容量，只在本地用于压缩阈值。
  max_tokens?: number;
  // 上限用哪个请求字段名，仅 format="openai" 有意义：
  // ""=max_tokens(默认) | "max_completion_tokens"(OpenAI 推理模型只认它)
  max_tokens_field?: string;
  // 自定义会话头名：非空时每次请求带该 HTTP 头，头值=当前会话/意图的 session id。
  // ""=不发送。用于按 session-id 头做提示缓存/粘性路由的网关。
  session_header_key?: string;
  // 固定附加到每个 LLM 请求的自定义头（如 User-Agent）。空对象/缺省=不附加。
  custom_headers?: Record<string, string>;
  // 本配置对重试的覆盖（建连/空响应/同 provider 安全窗口）。留空/全 0 = 跟随全局策略。
  retry?: LLMRetryOverride;
}

// ---- LLM 重试策略 ----
// 一层重试的两个旋钮。两者都是「0 = 未配置」：
//   attempts    0=用默认次数 | -1=关闭该层重试 | >0=重试次数
//   interval_ms 0=用默认的指数退避 | >0=改用这个固定毫秒间隔
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// 单个 LLM 配置能覆盖的三层（都是「跟着端点走」的重试）。
export interface LLMRetryOverride {
  connect: LLMRetryRule; // 建连重试：连接重置/超时/429/5xx，流开始前
  empty: LLMRetryRule; // 空响应重试：完成但没有任何内容（仅 openai 格式）
  stream: LLMRetryRule; // 同 provider 安全窗口重试：未交付输出前的断流重放
}

// 全局策略 = 上面三层的默认值 + 两层只有全局的：
//   breaker 轮询熔断（attempts=连续几次瞬时失败熔断，interval_ms=固定冷却时长）
//   intent  意图重跑（worker 以 model_error 收场后整条意图重跑）
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM 轮询（故障转移）----
// 一个配置在轮询链中的位置与健康状态。state:
//   ok       正常
//   degraded 有连续失败但未达熔断阈值
//   tripped  已熔断，冷却期内被跳过（cooldown_secs 为剩余秒数）
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // 是否为当前激活配置（恒为链首）
  excluded: boolean; // pool_exclude：不参与轮询
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // 内置为 goals/planner/mainagent/worker；自定义为用户自定 key
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // 绑定的 LLM 配置；null/absent = 跟随任务/会话/全局
  max_turns?: number; // 0 = 无限制
  run_seconds?: number; // worker 单次运行墙钟上限(秒)；0 = 无限制
  web_search?: boolean; // 是否启用网络搜索(受系统全局开关门控)
  interactive_shell?: boolean; // 是否启用交互式 shell(持久 PTY 会话工具族)
  // P3 触发后处理策略(仅自定义 agent 有意义)
  trigger_run_mode?: "serial" | "parallel"; // 串行排队 / 每次触发各自并发一个会话
  trigger_merge_mode?: "by_task" | "all" | "none"; // 仅 serial：同任务合并 / 全部合并 / 不合并
  trigger_max_parallel?: number; // 仅 parallel：每 agent 并发上限；0=不限
  // 绑定数量(仅列表接口返回)：可见 MCP / 可见 Skill / 绑定工具
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[]; skill_always?: string[] };
  // 可绑定的 LLM 配置候选(供「默认模型」下拉)；当前绑定见 agent.llm_profile_id
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // 已保存的收尾提示词(空=用内置默认)
  wrapup_default?: string; // 内置默认收尾提示词(占位/恢复默认)
  wrapup_max_turns?: number; // 已保存的收尾轮数(0=用内置默认)
  wrapup_max_turns_default?: number; // 内置默认收尾轮数(供 "0=默认N" 提示)
  // 任务级超时收尾词(仅 worker/planner，task_timeout_wrapup_supported=true 时才显示该分区)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
  tools_disabled?: number; // P25：其中被工具级开关关掉的数量
}

/** P24：外部 MCP 的并发/熔断快照（mcphttp 按 server 名维护）。 */
export interface MCPGuardStatus {
  server: string;
  max_concurrent: number; // 0 = 不限
  inflight: number;
  consecutive_failures: number;
  open: boolean;
  open_remaining_sec?: number;
  last_error?: string;
}

export interface MCPTool {
  name: string;
  description: string;
  /** P25 工具级开关：false = 该工具不注入任何 Agent 的工具面（全局）。 */
  enabled: boolean;
  /** 发现时缓存的 inputSchema（JSON Schema 子集），右栏工具行展开参数表用。 */
  schema?: MCPToolSchema;
}

export interface MCPToolSchema {
  type?: string;
  properties?: Record<string, MCPToolSchemaProp>;
  required?: string[];
}

export interface MCPToolSchemaProp {
  type?: string;
  description?: string;
  enum?: unknown[];
  items?: MCPToolSchema;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // 调用统计（skill_usage 账本）。从未被调用过的 skill：calls=0、last_used 缺省。
  calls: number;
  tasks: number; // 加载过它的任务数（chat 会话不计入）
  usage_agents: string[]; // 加载过它的 agent key
  last_used?: string;
  /** 前导块不合法（元数据改动会被拒，见后端 skill_frontmatter.go）。 */
  meta_error?: string;
  /** 清单问题（缺 name/description 之类）。 */
  meta_warning?: string;
}

// ---- P28：FOFA 检索与存储清理 ----

/** 情报源返回的一条资产（与后端 intel.Asset 对齐；extra 装请求的其余字段）。 */
export interface IntelAsset {
  host?: string;
  ip?: string;
  port?: number;
  protocol?: string;
  domain?: string;
  title?: string;
  server?: string;
  icp?: string;
  extra?: Record<string, string>;
}

export interface IntelSearchResult {
  provider: string;
  expr: string;
  fields: string[];
  total: number;
  returned: number;
  assets: IntelAsset[];
  spool?: string;
  used_today: number;
  daily_quota: number;
  note?: string;
}

/** 存储体检里的一条可删单元。 */
export interface StorageUnit {
  name: string;
  path: string;
  bytes: number;
  mod_time: string;
  protected?: string;
}

export interface StorageCategoryReport {
  key: string;
  label: string;
  stats_only?: string;
  retention_days: number;
  units: number;
  bytes: number;
  expired_units: number;
  expired_bytes: number;
  protected_units: number;
  items?: StorageUnit[];
}

export interface StorageReport {
  dir: string;
  disk_total: number;
  disk_free: number;
  scanned_at: string;
  categories: StorageCategoryReport[];
}

export interface StorageCleanResult {
  deleted: { category: string; name: string; bytes: number; mod_time: string }[];
  deleted_bytes: number;
  failures: string[];
  report: StorageReport;
  last_clean: Record<string, unknown>;
}

// SkillCall 是一次 Skill() 调用（单个 skill 的最近调用列表）。
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = 非任务场景（对话会话）
  session_id: string;
  args_len: number;
}

// MissingSkill 是被点名但不存在的 skill —— "想用但没有"的缺口。
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (内置工具目录) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // 自定义工具类型
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // 自定义工具执行规格(kind!=builtin)
  deferred?: boolean; // schema 延迟(SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Dashboard ----
export interface DashboardTaskTokenProfile {
  profile_id: number | null;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  task_count: number;
}
export interface DashboardTaskTokenDay {
  date: string;
  profile_id: number | null;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}
export interface DashboardSummary {
  task_total: number;
  tasks_by_status: Record<string, number>;
  recent_tasks: Task[];
  token_by_profile: DashboardTaskTokenProfile[];
  daily_tokens: DashboardTaskTokenDay[];
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
  /** 带 task 时的探索节点计数（按 kind），来自 COUNT(*)，不是列表窗口长度。 */
  exploration?: Record<string, number>;
  /** 带 task 时的意图分状态精确计数。 */
  intent_states?: Record<string, number>;
  /** 当前可被 worker 领取（前置已满足）的意图数。 */
  claimable_intents?: number;
  /**
   * 遗留物（P43）：我们留在目标上的东西。`uncleaned` 与 `unknown` **分开给**——
   * unknown 是"没去确认还在不在"，并进未清理会把"我们不知道"说成"我们确认它还留着"。
   */
  artifacts_total?: number;
  artifacts_uncleaned?: number;
  artifacts_unknown?: number;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

// InterceptRuleTestResult 是规则 dry-run 的返回（P12）：命中面、渲染后的文案与
// 展开出的候选面（规则实际逐条看过的文本）。
export interface InterceptRuleTestResult {
  matched: boolean;
  matched_text?: string;
  matched_surface?: string;
  rendered_message?: string;
  candidates?: { text: string; surface: string }[];
  candidates_checked?: number;
}

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules（资产拦截：全局黑名单） ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action 仅用于任务级规则：block=拦截(禁止测试) allow=允许(白名单)。
export type AssetInterceptAction = "block" | "allow";

// 任务级资产拦截/允许规则的录入项（创建任务、任务详情编辑使用）。
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // 全局规则不带此字段（恒为拦截）；任务级规则区分 block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // 规则 message 或模型判定理由(模型判定带 [模型] 前缀)
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: 模型兜底审批(仅当没有任何拦截规则命中时由模型判断)的全局配置。
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = 跟随激活/默认配置
  prompt: string; // 判定提示词;GET 未设置时后端回填内置模板全文
  timeout_seconds: number; // 模型调用超时
  fail_action: "allow" | "ask" | "deny"; // 模型出错/超时/不可解析时的回退
  ask_timeout_seconds: number; // 模型判 ask 转人工后的审批等待超时
  ask_timeout_action: "allow" | "deny"; // 审批超时后的默认动作
  structured: boolean; // P15 结构化裁决:模型给各轴把握+建议,最终动作由代码阈值决定
}

// JudgeUsage: 模型兜底审批(judge 通道)的累计 token 用量 + 近 N 天每日序列。
export interface JudgeDayUsage {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
  input_tokens: number;
  output_tokens: number;
}
export interface JudgeUsage {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── 资产同步 (ScopeSentry 数据源) ──────────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

/** 长任务执行台账（tool_executions）— 时间线 execution 事件的详情。 */
export interface ToolExecution {
  id: number;
  task_id: string;
  intent_id?: number;
  worker: string;
  tool: string;
  command: string;
  description?: string;
  status: string;
  exit_code?: number;
  output_path?: string;
  output_bytes?: number;
  partial_tail?: string;
  cancel_note?: string;
  error?: string;
  started_at?: string;
  ended_at?: string;
  created_at: string;
  limits?: unknown;
}

// 单个工具的调用统计（/commands/stats）；errors 为其中失败的次数。
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // provider 实际收发的 HTTP 原文：请求为 buildBody() 发出的完整 body（含工具
  // schema），响应为原始 SSE 帧。上面的 request_body/response_body 是归一化视图，
  // 丢弃了工具 schema 与 tool_use 块。旧记录为空。
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
  /** 事前可检出性（P38）：这一手**预计**会留下什么。与 P31 的实测（detection）刻意分开。 */
  opsec_notes?: OpsecNote[];
  /** 知识库没收录这条命令——**不代表没有日志**，只代表我们没有依据。 */
  opsec_uncovered?: boolean;
  opsec_command?: string;
}

export interface OpsecNote {
  id: string;
  title: string;
  /** 非空 = 这条由代码判定（如 language 按汉字字符集），不是关键词命中。 */
  detector?: string;
  where: string[];
  signals?: string[];
  /** 低 / 中 / 高 */
  exposure: string;
  note: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check —— 当前版本与 GitHub 最新正式版的比较结果。 */
export interface UpdateCheck {
  /** 当前运行的版本；开发构建为 "dev" 或 git describe 的带后缀形式。 */
  current: string;
  /** 运行形态。docker 下换装只作用于容器可写层，重建容器会退回镜像版本。 */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  /** 发布源标识；为避免暴露项目地址，服务端默认不返回。 */
  repo?: string;
  /** 是否存在可回滚的上一版本（blade.old）。 */
  has_backup: boolean;
  /** 本次启动时自更新自举的结论（换装失败 / 已回滚等），无事发生时为空。 */
  boot_notice?: string;
  rolled_back?: boolean;
  /** 查询 GitHub 失败时给出原因，此时下面的字段都不会有。 */
  error?: string;
  latest?: string;
  /** Release 说明与页面地址；同样默认不返回（更新日志入口隐藏时）。 */
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** 当前平台对应的发布包名，以及该 Release 是否真的带了它。 */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** 双方版本号是否可比较；开发构建为 false，此时禁用一键更新。 */
  comparable?: boolean;
  /** comparable 为 false 时的说明。 */
  reason?: string;
}

/** /api/update/stream 推送的一条更新进度。 */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** 仅下载阶段有意义（0-100）；其余阶段为 -1。 */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}

// 战役：任务之上的一层（一个客户、若干目标、一份交付物）。
export interface Campaign {
  id: number;
  name: string;
  company_id?: number;
  note: string;
  created_at: string;
  updated_at: string;
  task_count?: number;
}

// 汇总里每个任务的一行。
export interface CampaignTaskBrief {
  id: number;
  name: string;
  status: string;
  findings: number;
  footholds: number;
  credentials: number;
  detections: number;
}

// GET /api/campaigns/{id}：归属 + 跨任务聚合。techniques / detection_results 无数据时缺省。
export interface CampaignSummary {
  campaign: Campaign;
  tasks: CampaignTaskBrief[];
  totals: { tasks: number; findings: number; footholds: number; credentials: number; detections: number };
  techniques?: Record<string, number>;
  detection_results?: Record<string, number>;
}
