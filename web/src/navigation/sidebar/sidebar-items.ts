import {
  Activity,
  BellRing,
  Blocks,
  BookOpen,
  Bot,
  Brain,
  Bug,
  ClipboardCheck,
  FileCheck2,
  FolderKanban,
  FolderOpen,
  HardDrive,
  KeyRound,
  LayoutDashboard,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radio,
  ScrollText,
  ServerCog,
  Settings2,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Target,
  Terminal,
  Trophy,
  Wrench,
} from "lucide-react";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  matchUrls?: readonly string[];
  keywords?: readonly string[];
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  /** liveBadge：需要实时计数的角标（目前只有拦截待审批）。声明式挂载可确保折叠与改名不丢失红点。 */
  liveBadge?: "intercept_pending";
  keywords?: readonly string[];
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  matchUrls?: readonly string[];
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

export interface QuickSearchAlias {
  id: string;
  group: string;
  title: string;
  subtitle: string;
  url: string;
  keywords: readonly string[];
  icon?: LucideIcon;
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: "核心作战",
    items: [
      { id: "dashboard", title: "仪表盘", url: "/dashboard", icon: LayoutDashboard },
      { id: "chat", title: "对话", url: "/chat", icon: MessageSquare },
      {
        id: "tasks",
        title: "任务",
        url: "/function/tasks",
        matchUrls: ["/function/tasks", "/function/schedules"],
        keywords: ["计划任务", "已归档", "schedule", "cron"],
        icon: Target,
      },
      { id: "findings", title: "发现", url: "/function/findings", icon: Bug },
      {
        id: "assets",
        title: "资产",
        url: "/function/assets",
        matchUrls: ["/function/assets", "/function/companies", "/function/sync"],
        keywords: ["企业", "ScopeSentry", "资产同步", "FOFA"],
        icon: Network,
      },
      {
        id: "loot",
        title: "战果",
        url: "/function/loot",
        matchUrls: ["/function/loot", "/function/credentials", "/function/footholds"],
        keywords: ["战果台账", "凭据工作台", "据点与隧道", "账号尝试", "遗留物"],
        icon: Trophy,
      },
      { id: "traffic", title: "流量", url: "/function/traffic", keywords: ["真机", "MITM", "Pinning"], icon: Activity },
    ],
  },
  {
    id: 2,
    label: "编排与审计",
    items: [
      {
        id: "projects-delivery",
        title: "项目与交付",
        icon: FolderKanban,
        subItems: [
          { id: "campaigns", title: "战役", url: "/function/campaigns", icon: FolderKanban },
          { id: "deliverables", title: "交付物", url: "/function/deliverables", icon: FileCheck2 },
          { id: "coverage", title: "覆盖与检测", url: "/function/coverage", icon: ShieldCheck },
        ],
      },
      {
        id: "knowledge-base",
        title: "知识库",
        icon: BookOpen,
        subItems: [
          { id: "playbooks", title: "打法库", url: "/function/playbooks", icon: BookOpen },
          { id: "cves", title: "CVE 库", url: "/function/cve", icon: ShieldAlert },
        ],
      },
      {
        id: "global-audit",
        title: "全局审计",
        icon: ClipboardCheck,
        subItems: [
          { id: "broadcast", title: "全局播报", url: "/function/broadcast", icon: Radio },
          {
            id: "commands",
            title: "工具执行",
            url: "/function/commands",
            matchUrls: ["/function/commands", "/function/verification-ledger"],
            keywords: ["验证台账", "命令历史", "verification"],
            icon: Terminal,
          },
          { id: "workspace", title: "工作空间", url: "/function/workspace", icon: FolderOpen },
        ],
      },
    ],
  },
  {
    id: 3,
    label: "系统与运维",
    items: [
      {
        id: "capabilities",
        title: "能力与扩展",
        icon: Blocks,
        subItems: [
          { id: "tools", title: "工具", url: "/system/tools", icon: Wrench },
          { id: "skills", title: "技能", url: "/system/skills", icon: Sparkles },
          { id: "mcp", title: "MCP", url: "/system/mcp", icon: Plug },
        ],
      },
      {
        id: "models-agents",
        title: "模型与 Agent",
        icon: Bot,
        subItems: [
          { id: "llm", title: "LLM 配置", url: "/system/llm", icon: Brain },
          { id: "agents", title: "Agent 管理", url: "/system/agents", icon: Bot },
        ],
      },
      {
        id: "intercept",
        title: "拦截与审批",
        url: "/system/intercept",
        matchUrls: ["/system/intercept"],
        keywords: ["审批记录", "资产黑名单", "命令拦截"],
        icon: ShieldAlert,
        liveBadge: "intercept_pending",
      },
      {
        id: "channels-notify",
        title: "通知与 IM",
        icon: BellRing,
        subItems: [
          { id: "notify", title: "通知推送", url: "/system/notify", icon: BellRing },
          { id: "im", title: "IM 控制", url: "/system/im", icon: MessageSquare },
        ],
      },
      {
        id: "ops-observability",
        title: "运维与观测",
        icon: ServerCog,
        subItems: [
          { id: "diagnostics", title: "运行体检", url: "/system/diagnostics", icon: Activity },
          { id: "logs", title: "实时日志", url: "/system/logs", icon: ScrollText },
          { id: "storage", title: "存储管理", url: "/system/storage", icon: HardDrive },
          {
            id: "llm-records",
            title: "LLM 录制",
            url: "/function/llm-records",
            keywords: ["抓包", "raw", "SSE", "调试"],
            icon: Radio,
          },
        ],
      },
      {
        id: "settings",
        title: "系统配置",
        url: "/system/settings",
        matchUrls: ["/system/settings", "/system/api-tokens"],
        keywords: ["API 令牌", "审计", "代理", "搜索", "策略"],
        icon: Settings2,
      },
    ],
  },
];

// 收编进 Tabs / PageShell 的高频直达别名，保证 Cmd/Ctrl + J 仍可一键搜达。
export const quickSearchAliases: readonly QuickSearchAlias[] = [
  {
    id: "schedules",
    group: "核心作战",
    title: "计划任务",
    subtitle: "任务 → 计划任务（Cron 定时建任务）",
    url: "/function/tasks?tab=schedules",
    keywords: ["计划任务", "定时", "schedule", "cron"],
    icon: Target,
  },
  {
    id: "sync",
    group: "核心作战",
    title: "资产同步",
    subtitle: "资产 → ScopeSentry 外部同步",
    url: "/function/assets?action=sync",
    keywords: ["资产同步", "scopesentry", "sync", "外部数据源"],
    icon: Network,
  },
  {
    id: "credentials",
    group: "核心作战",
    title: "凭据工作台",
    subtitle: "战果 → 凭据工作台（核验 / 标失效）",
    url: "/function/loot?tab=credentials",
    keywords: ["凭据", "credential", "口令", "密码", "密钥"],
    icon: KeyRound,
  },
  {
    id: "footholds",
    group: "核心作战",
    title: "据点与隧道",
    subtitle: "战果 → 据点与隧道（存活检查 / SOCKS）",
    url: "/function/loot?tab=footholds",
    keywords: ["据点", "隧道", "foothold", "tunnel", "socks", "webshell"],
    icon: Terminal,
  },
  {
    id: "verification-ledger",
    group: "全局审计",
    title: "验证台账",
    subtitle: "工具执行 → 验证视角（状态与退出码汇总）",
    url: "/function/commands?view=verification",
    keywords: ["验证台账", "verification", "退出码"],
    icon: ShieldCheck,
  },
  {
    id: "api-tokens",
    group: "系统与运维",
    title: "API 令牌",
    subtitle: "系统配置 → API 令牌与操作审计",
    url: "/system/settings?section=tokens",
    keywords: ["api", "token", "令牌", "审计", "prometheus"],
    icon: KeyRound,
  },
];
