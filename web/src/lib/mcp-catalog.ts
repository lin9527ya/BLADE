// 推荐 MCP 目录（P28 续）：**只做预填，不自动安装**——用户点哪一条才把那条的命令/参数
// 填进「添加 MCP」弹窗，仍然由人过一眼再保存。新增推荐项只影响这个列表，不动任何已有配置。
//
// 收录标准：与 ARTEX 既有能力**互补**，不重复它已经有的东西。
// 刻意不收：filesystem（已有 Read/Write/Bash + 工作区）、memory / knowledge-graph（已有公司知识层
// 与图谱）、搜索类（平台内建搜索后端）、puppeteer（与 playwright 重复）、直连平台自己数据库的。

export type MCPCatalogItem = {
  /** 服务器名（预填进表单的 name） */
  name: string;
  /** 中文标题 */
  title: string;
  /** 一句话：它补的是哪块能力 */
  desc: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  /** 空格分隔的参数（与表单一致） */
  args?: string;
  url?: string;
  /** 环境变量/请求头预填（每行 KEY=VALUE；密钥留空待人填） */
  env?: string;
  /** 前置条件（key、常驻服务等） */
  needs?: string;
  /** 注意事项 */
  note?: string;
};

export const MCP_CATALOG: MCPCatalogItem[] = [
  {
    name: "chrome-devtools",
    title: "Chrome DevTools MCP",
    desc: "浏览器深度调试：控制台报错、网络请求、DOM/存储。SRC 前端面（DOM XSS、未授权接口、越权参数）验证比 playwright 更贴行为。",
    transport: "stdio",
    command: "npx",
    args: "-y chrome-devtools-mcp@latest",
    note: "与已启用的 playwright MCP 互补，不重复：它给的是 DevTools 视角（网络/控制台），不是自动化脚本。",
  },
  {
    name: "github",
    title: "GitHub MCP",
    desc: "代码搜索、仓库历史与 **补丁差分**——白盒找 0day 的高效路径（看上游什么时候、怎么修的）。",
    transport: "stdio",
    command: "npx",
    args: "-y @modelcontextprotocol/server-github",
    env: "GITHUB_PERSONAL_ACCESS_TOKEN=",
    needs: "GitHub PAT（fine-grained 即可，按需给 repo / public_repo 读权限）",
    note: "token 填在环境变量里；建议只给只读权限的 PAT。",
  },
  {
    name: "semgrep",
    title: "Semgrep MCP",
    desc: "规则化静态分析 + 语义匹配，直接服务白盒审计：把「可疑点」从几万行里筛出来再深读。",
    transport: "stdio",
    command: "uvx",
    args: "semgrep-mcp",
    needs: "本机有 uv/uvx（没有的话用 pip 装 semgrep-mcp 后把命令换成 semgrep-mcp）",
  },
  {
    name: "burp",
    title: "Burp Suite MCP",
    desc: "把 Burp 的 sitemap / 主动扫描 / Repeater 交给 agent。ARTEX 自带 MITM 记录与拦截，Burp 的扫描器与生态是差异面。",
    transport: "sse",
    url: "http://127.0.0.1:9876/sse",
    needs: "Burp 常驻，并安装官方 MCP 扩展（扩展里会给出实际的 host/port）",
    note: "端口以 Burp 扩展里显示的为准；不同版本可能是 http 而非 sse，按实际改。",
  },
  {
    name: "shodan",
    title: "Shodan MCP",
    desc: "与 FOFA 互补的测绘源：`vulns:` 过滤、历史 banner、组织维度，是 FOFA 没有的差异面。",
    transport: "stdio",
    command: "npx",
    args: "-y shodan-mcp",
    env: "SHODAN_API_KEY=",
    needs: "Shodan API key（付费账号才有完整过滤与历史）",
  },
  {
    name: "context7",
    title: "Context7 MCP",
    desc: "按版本取库/框架文档，写代码与写规则时不再凭记忆——减少 agent 自己造错 API 的概率。",
    transport: "stdio",
    command: "npx",
    args: "-y @upstash/context7-mcp",
  },
];
