import type { MCPGuardStatus } from "./types";

export type MCPTransport = "stdio" | "http" | "sse";

/** Parses a shell-like CLI argument string supporting single/double quotes and backslash escapes. */
export function parseShellArgs(input: string): string[] {
  const args: string[] = [];
  let cur = "";
  let inToken = false;
  let quote: "'" | '"' | null = null;

  for (let i = 0; i < input.length; i++) {
    const ch = input[i];
    if (quote) {
      if (ch === quote) {
        quote = null;
        continue;
      }
      if (quote === '"' && ch === "\\" && i + 1 < input.length) {
        const next = input[i + 1];
        if (next === '"' || next === "\\" || next === "$") {
          cur += next;
          i++;
          continue;
        }
      }
      cur += ch;
      continue;
    }

    if (/\s/.test(ch)) {
      if (inToken) {
        args.push(cur);
        cur = "";
        inToken = false;
      }
      continue;
    }
    inToken = true;
    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }
    if (ch === "\\" && i + 1 < input.length) {
      cur += input[++i];
      continue;
    }
    cur += ch;
  }

  if (inToken) {
    args.push(cur);
  }
  return args;
}

/** Formats CLI arguments into an editable shell string, quoting tokens with spaces or quotes. */
export function formatShellArgs(args: readonly string[] | undefined): string {
  if (!args || args.length === 0) return "";
  return args
    .map((a) => {
      if (a === "") return '""';
      if (!/[\s"'\\]/.test(a)) return a;
      return `"${a.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
    })
    .join(" ");
}

/** Returns human-readable transport description distinguishing stdio, http, and legacy sse. */
export function transportLabel(transport: MCPTransport | string): string {
  switch (transport) {
    case "stdio":
      return "（本地起进程）";
    case "sse":
      return "（远程 Legacy SSE）";
    case "http":
      return "（远程 Streamable HTTP）";
    default:
      return "";
  }
}

/** Validates MCP add/edit form fields mirroring server-side constraints. */
export function validateMcpForm(form: {
  name: string;
  transport: MCPTransport | string;
  command: string;
  url: string;
}): string | null {
  const name = form.name.trim();
  if (!name) return "请填写名称";
  if ([...name].length > 48) return "名称不能超过 48 个字符";
  if (name.includes("__")) return "名称不能包含双下划线 __";
  if (/[\s/\\,]/.test(name)) return "名称不能包含空格、斜杠或逗号";

  if (form.transport === "stdio") {
    if (!form.command.trim()) return "请填写命令";
    return null;
  }

  const rawURL = form.url.trim();
  if (!rawURL) return "请填写远程 URL";
  try {
    const parsed = new URL(rawURL);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "远程 URL 仅支持 http:// 或 https://";
    }
    if (!parsed.host) return "请填写完整的远程 URL";
  } catch {
    return "远程 URL 格式不合法";
  }
  return null;
}

/** Summarizes runtime guard status consistently across stdio, http, and sse servers. */
export function mcpGuardSummary(g?: MCPGuardStatus): string {
  if (!g) return "暂无运行记录（首次调用时建立守卫状态）";
  const cap = g.max_concurrent === 0 ? "并发不限" : `并发上限 ${g.max_concurrent}（进行中 ${g.inflight}）`;
  if (g.open) {
    return `${cap} · 熔断已开闸（剩余 ${g.open_remaining_sec ?? 0}s）`;
  }
  return `${cap} · 连续失败 ${g.consecutive_failures} 次`;
}
