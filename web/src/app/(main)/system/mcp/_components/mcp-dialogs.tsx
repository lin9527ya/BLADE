"use client";

import type * as React from "react";

import { PlusIcon } from "lucide-react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { MCP_CATALOG } from "@/lib/mcp-catalog";
import type { Agent, MCPServer } from "@/lib/types";

// MCP 页尾部的三个弹窗：推荐目录（只预填不安装）、添加/编辑服务器、删除确认。
// 抽出来是为了让页面本体只剩列表与明细两栏。

export function MCPDialogs({
  catalogOpen,
  setCatalogOpen,
  open,
  setOpen,
  editing,
  saving,
  pendingDelete,
  setPendingDelete,
  addFromCatalog,
  renderForm,
  saveForm,
  removeServer,
}: {
  catalogOpen: boolean;
  setCatalogOpen: React.Dispatch<React.SetStateAction<boolean>>;
  open: boolean;
  setOpen: React.Dispatch<React.SetStateAction<boolean>>;
  editing: MCPServer | null;
  saving: boolean;
  pendingDelete: MCPServer | null;
  setPendingDelete: React.Dispatch<React.SetStateAction<MCPServer | null>>;
  addFromCatalog: (item: (typeof MCP_CATALOG)[number]) => void;
  renderForm: () => React.ReactNode;
  saveForm: () => void;
  removeServer: (s: MCPServer) => void;
}) {
  return (
    <>
      {/* 推荐目录：只预填、不安装。收录标准见 lib/mcp-catalog.ts 顶部注释 */}
      <Dialog open={catalogOpen} onOpenChange={setCatalogOpen}>
        <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
          <DialogHeader className="border-b px-6 py-4">
            <DialogTitle>推荐 MCP</DialogTitle>
            <DialogDescription className="text-xs">
              点「预填」只会把命令、参数与环境变量填进添加弹窗，**仍需你确认保存**；不会自动安装任何东西。
              收录标准：与平台既有能力互补（不重复文件/知识/搜索类）。
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 overflow-y-auto px-6 py-4">
            <div className="flex flex-col divide-y">
              {MCP_CATALOG.map((item) => (
                <div key={item.name} className="flex items-start gap-3 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">{item.title}</span>
                      <Badge variant="outline" className="px-1.5 py-0 text-[10px] uppercase">
                        {item.transport}
                      </Badge>
                      <code className="text-muted-foreground font-mono text-[11px]">{item.name}</code>
                    </div>
                    <p className="text-muted-foreground mt-1 text-xs leading-relaxed">{item.desc}</p>
                    {(item.needs || item.note) && (
                      <p className="text-muted-foreground mt-1 text-[11px] leading-relaxed">
                        {item.needs && <>需要：{item.needs}　</>}
                        {item.note}
                      </p>
                    )}
                    <p className="text-muted-foreground mt-1 font-mono text-[11px]">
                      {item.transport === "stdio" ? `${item.command} ${item.args ?? ""}` : (item.url ?? "")}
                    </p>
                  </div>
                  <Button size="sm" variant="outline" onClick={() => addFromCatalog(item)}>
                    预填
                  </Button>
                </div>
              ))}
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* 添加 / 编辑：居中弹窗（7 个字段；工具列表已在右栏，弹窗只管配置） */}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl">
          <DialogHeader className="border-b px-4 py-3">
            <DialogTitle>{editing ? editing.name : "添加 MCP 服务器"}</DialogTitle>
            <DialogDescription>stdio（本地起进程）或 http（远程 Streamable HTTP）</DialogDescription>
          </DialogHeader>
          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">{renderForm()}</div>
          <div className="flex items-center gap-2 border-t px-4 py-3">
            <Button onClick={saveForm} disabled={saving}>
              {editing ? (
                "保存"
              ) : (
                <>
                  <PlusIcon /> 添加
                </>
              )}
            </Button>
            <Button variant="outline" onClick={() => setOpen(false)} disabled={saving}>
              取消
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!pendingDelete} onOpenChange={(o) => !o && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除「{pendingDelete?.name}」？</AlertDialogTitle>
            <AlertDialogDescription>
              删除后该服务器的工具从所有 Agent 的工具面消失，按 Agent 的可见性授权也一并清除。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const s = pendingDelete;
                setPendingDelete(null);
                if (s) void removeServer(s);
              }}
            >
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
