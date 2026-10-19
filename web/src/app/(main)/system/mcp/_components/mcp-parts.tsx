"use client";

import type * as React from "react";

import type { MCPToolSchema, MCPToolSchemaProp } from "@/lib/types";

// MCP 页的类型与展示基件：传输方式、表单状态、参数行解析与信息行。

export type Transport = "stdio" | "http" | "sse";
export type FormState = {
  name: string;
  transport: Transport;
  command: string;
  args: string;
  url: string;
  env: string;
  insecure: boolean;
};
export const emptyForm: FormState = {
  name: "",
  transport: "stdio",
  command: "",
  args: "",
  url: "",
  env: "",
  insecure: false,
};

// 工具行展开的参数表：inputSchema 的 properties 平铺成行；array<object> 再把
// items.properties 作为子行带进来（与工具页参数表同一读法，这里只读）。
export type SchemaRow = {
  name: string;
  type: string;
  required: boolean;
  desc: string;
  enumText: string;
  child: boolean;
};

export function schemaRows(schema?: MCPToolSchema): SchemaRow[] {
  const rows: SchemaRow[] = [];
  const push = (name: string, p: MCPToolSchemaProp | undefined, required: boolean, child: boolean) => {
    rows.push({
      name,
      type: p?.type ?? "",
      required,
      desc: p?.description ?? "",
      enumText: p?.enum?.length ? ` · ${p.enum.map((v) => String(v)).join("|")}` : "",
      child,
    });
  };
  const required = new Set(schema?.required ?? []);
  for (const [name, p] of Object.entries(schema?.properties ?? {})) {
    push(name, p, required.has(name), false);
    const sub = p?.items?.properties;
    if (p?.type === "array" && sub && Object.keys(sub).length > 0) {
      const subReq = new Set(p.items?.required ?? []);
      for (const [sn, sp] of Object.entries(sub)) {
        push(sn, sp, subReq.has(sn), true);
      }
    }
  }
  return rows;
}

// ToolParams：工具行展开后的只读参数表（inputSchema 子集）。
export function ToolParams({ schema }: { schema?: MCPToolSchema }) {
  const rows = schemaRows(schema);
  if (rows.length === 0) {
    return (
      <p className="text-muted-foreground mt-2 ml-5 text-xs">
        该工具没有声明参数（或这次发现没取到 schema——点「刷新」重新发现）。
      </p>
    );
  }
  return (
    <div className="bg-muted/30 mt-2 ml-5 overflow-x-auto rounded-md border p-2">
      <table className="w-full text-xs">
        <thead>
          <tr className="text-muted-foreground text-left">
            <th className="py-0.5 pr-3 font-normal">参数</th>
            <th className="py-0.5 pr-3 font-normal">类型</th>
            <th className="py-0.5 pr-3 font-normal">必填</th>
            <th className="py-0.5 font-normal">说明</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={(r.child ? "·" : "") + r.name} className="align-top">
              <td className="py-0.5 pr-3 font-mono">{r.child ? `↳ ${r.name}` : r.name}</td>
              <td className="text-muted-foreground py-0.5 pr-3 font-mono">
                {r.type || "?"}
                {r.enumText}
              </td>
              <td className="text-muted-foreground py-0.5 pr-3">{r.required ? "是" : ""}</td>
              <td className="text-muted-foreground py-0.5">{r.desc}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// bareToolName：缓存里的工具名是全名 mcp__<server>__<tool>（mcphttp.ToolName）。右栏里
// server 已是页面主体，展示时去掉前缀；开关发的仍是全名（后端按缓存名匹配）。
export function bareToolName(server: string, full: string): string {
  const prefix = `mcp__${server}__`;
  return full.startsWith(prefix) ? full.slice(prefix.length) : full;
}

// InfoRow：明细行——左标签右值，值等宽可换行（URL/命令/错误都长）。
export function InfoRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[8rem_1fr] sm:items-baseline sm:gap-3">
      <span className="text-muted-foreground text-xs">{label}</span>
      <div className="min-w-0 text-sm break-all">{children}</div>
    </div>
  );
}

// MCP 管理页照 CyberStrikeAI 的布局：满幅主从两栏——左栏服务器列表（只读行 + 选中态），
// 右栏是选中 server 的完整明细（连接信息 / 熔断与并发 / 工具列表 / 可见性），动作都在右栏头部。
// 添加与编辑仍走居中弹窗（7 个字段的表单，与工具页编辑器同一容器风格）。
