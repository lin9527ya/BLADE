import {
  Activity,
  BellRing,
  Blocks,
  Bot,
  Brain,
  Bug,
  FolderKanban,
  FolderOpen,
  FolderSync,
  HardDrive,
  LayoutDashboard,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radio,
  ScrollText,
  Settings2,
  ShieldAlert,
  Sparkles,
  Target,
  Terminal,
  Wrench,
} from "lucide-react";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
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
  /** liveBadge：需要实时计数的角标（目前只有拦截待审批）。声明式的好处是"审批记录"改名/降级成
   *  子项时红点不会丢——此前它被硬编码挂在 item.id === "approvals" 上。 */
  liveBadge?: "intercept_pending";
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
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

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: "功能",
    items: [
      { id: "dashboard", title: "仪表盘", url: "/dashboard", icon: LayoutDashboard },
      { id: "chat", title: "对话", url: "/chat", icon: MessageSquare },
      { id: "tasks", title: "任务", url: "/function/tasks", icon: Target },
      { id: "campaigns", title: "战役", url: "/function/campaigns", icon: FolderKanban },
      { id: "findings", title: "发现", url: "/function/findings", icon: Bug },
      { id: "traffic", title: "流量", url: "/function/traffic", icon: Activity },
      { id: "commands", title: "工具执行", url: "/function/commands", icon: Terminal },
      { id: "llm-records", title: "LLM 录制", url: "/function/llm-records", icon: Radio },
      { id: "assets", title: "资产", url: "/function/assets", icon: Network },
      { id: "sync", title: "资产同步", url: "/function/sync", icon: FolderSync },
      { id: "workspace", title: "工作空间", url: "/function/workspace", icon: FolderOpen },
    ],
  },
  {
    id: 2,
    label: "系统",
    items: [
      // 能力面：给 Agent 什么工具/技能/外部服务。三者互相引用（技能可点名 MCP），
      // 但各是一套存储与工作台——合并成一个可折叠父项，而不是塞进同一页。
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
      // 模型与 Agent：LLM 是"端点资源"（profile/健康/轮询/重试），Agent 只是覆盖链的第二层，
      // 两者刻意保持两页、只归到一个父项下。
      {
        id: "models-agents",
        title: "模型与 Agent",
        icon: Bot,
        subItems: [
          { id: "llm", title: "LLM 配置", url: "/system/llm", icon: Brain },
          { id: "agents", title: "Agent 管理", url: "/system/agents", icon: Bot },
        ],
      },
      // 拦截三面（命令拦截 / 资产黑名单 / 审批记录）合并成一页四分区；
      // 待审批红点跟着这一项走（声明式 liveBadge）。
      {
        id: "intercept",
        title: "拦截与审批",
        url: "/system/intercept",
        icon: ShieldAlert,
        liveBadge: "intercept_pending",
      },
      { id: "notify", title: "通知推送", url: "/system/notify", icon: BellRing },
      { id: "logs", title: "日志", url: "/system/logs", icon: ScrollText },
      // P28：存储体检与清理（试算 → 执行，默认保留天数全 0 = 什么都不删）。
      { id: "storage", title: "存储", url: "/system/storage", icon: HardDrive },
      { id: "settings", title: "系统配置", url: "/system/settings", icon: Settings2 },
    ],
  },
];
