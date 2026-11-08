"use client";

import * as React from "react";

import {
  DownloadIcon,
  FileIcon,
  FolderIcon,
  FolderPlusIcon,
  HardDriveIcon,
  RefreshCwIcon,
  SaveIcon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { useUnsavedChanges } from "@/hooks/use-unsaved-changes";
import { createLatestRequest } from "@/lib/request-lifecycle";
import { workspaceDirty, workspaceExpectedVersions } from "@/lib/workspace-edit";
import { api } from "@/lib/api";
import type { WorkspaceEntry, WorkspaceFile } from "@/lib/types";
import { cn } from "@/lib/utils";

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}
function fmtTime(ms: number): string {
  return new Date(ms).toLocaleString("zh-CN", {
    year: "2-digit",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

type EditState = {
  file: WorkspaceFile;
  content: string;
  dirty: boolean;
  saving: boolean;
};

export default function WorkspacePage() {
  const [path, setPath] = React.useState("");
  const [entries, setEntries] = React.useState<WorkspaceEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [edit, setEdit] = React.useState<EditState | null>(null);
  const [mkdirOpen, setMkdirOpen] = React.useState(false);
  const [mkdirName, setMkdirName] = React.useState("");
  const uploadRef = React.useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = React.useState(false);
  const directoryRequest = React.useRef(createLatestRequest());
  const fileRequest = React.useRef(createLatestRequest());
  useUnsavedChanges(edit?.dirty ?? false);
  React.useEffect(
    () => () => {
      directoryRequest.current.cancel();
      fileRequest.current.cancel();
    },
    [],
  );
  const closeEditor = () => {
    if (edit?.saving) return;
    if (edit?.dirty && !window.confirm("有未保存的修改，确认关闭并丢弃吗？")) return;
    fileRequest.current.cancel();
    setEdit(null);
  };

  const load = React.useCallback((p: string) => {
    const request = directoryRequest.current.start();
    setLoading(true);
    api
      .workspaceList(p, request.signal)
      .then((r) => {
        if (request.alive()) {
          setEntries(r.entries);
          setPath(r.path);
        }
      })
      .catch((e) => {
        if (request.alive()) toast.error(`读取目录失败：${(e as Error).message}`);
      })
      .finally(() => {
        if (request.alive()) setLoading(false);
      });
  }, []);

  React.useEffect(() => {
    load("");
  }, [load]);

  const crumbs = React.useMemo(() => {
    const parts = path ? path.split("/") : [];
    const acc: { name: string; path: string }[] = [{ name: "工作空间", path: "" }];
    let cur = "";
    for (const part of parts) {
      cur = cur ? `${cur}/${part}` : part;
      acc.push({ name: part, path: cur });
    }
    return acc;
  }, [path]);

  const openFile = (e: WorkspaceEntry, discardConfirmed = false) => {
    if (
      edit?.saving ||
      (edit?.dirty && !discardConfirmed && !window.confirm("有未保存的修改，确认丢弃并打开其他文件吗？"))
    )
      return;
    const request = fileRequest.current.start();
    api
      .workspaceRead(e.path, request.signal)
      .then((f) => {
        if (request.alive()) setEdit({ file: f, content: f.content ?? "", dirty: false, saving: false });
      })
      .catch((err) => {
        if (request.alive()) toast.error(`打开文件失败：${(err as Error).message}`);
      });
  };

  const saveFile = async () => {
    if (!edit || edit.saving) return;
    const submitted = edit;
    if (!submitted.file.version) {
      toast.error("缺少文件版本，请重新读取后保存");
      return;
    }
    setEdit({ ...submitted, saving: true });
    try {
      const result = await api.workspaceWrite(submitted.file.path, submitted.content, submitted.file.version);
      setEdit((cur) =>
        cur?.file.path === submitted.file.path
          ? {
              ...cur,
              file: {
                ...cur.file,
                version: result.version,
                content: submitted.content,
                size: new TextEncoder().encode(submitted.content).length,
              },
              dirty: workspaceDirty(cur.content, submitted.content),
              saving: false,
            }
          : cur,
      );
      toast.success("已保存");
      load(path);
    } catch (err) {
      toast.error(`保存失败：${(err as Error).message}。草稿未丢弃，可复制后重新读取。`);
      setEdit((cur) => (cur?.file.path === submitted.file.path ? { ...cur, saving: false } : cur));
    }
  };

  const del = (e: WorkspaceEntry) => {
    if (!window.confirm(`确认删除 ${e.dir ? "目录" : "文件"} “${e.name}”？${e.dir ? "（含其下所有内容）" : ""}`))
      return;
    api
      .workspaceDelete(e.path)
      .then(() => {
        toast.success("已删除");
        load(path);
      })
      .catch((err) => toast.error(`删除失败：${(err as Error).message}`));
  };

  const doUpload = async (files: FileList | null) => {
    if (!files?.length || uploading) return;
    const selected = Array.from(files);
    setUploading(true);
    try {
      const preview = await api.workspaceUploadCheck(
        path,
        selected.map((f) => f.name),
      );
      if (
        preview.conflicts.length &&
        !window.confirm(
          `以下文件已存在，确认覆盖当前版本吗？\n${preview.conflicts
            .slice(0, 15)
            .map((item) => item.path)
            .join("\n")}${preview.conflicts.length > 15 ? `\n…共 ${preview.conflicts.length} 个` : ""}`,
        )
      )
        return;
      const result = await api.workspaceUpload(path, selected, workspaceExpectedVersions(preview.conflicts));
      toast.success(`已上传 ${result.uploaded} 个文件`);
      load(path);
    } catch (err) {
      toast.error(`上传失败：${(err as Error).message}`);
    } finally {
      setUploading(false);
      if (uploadRef.current) uploadRef.current.value = "";
    }
  };

  const doMkdir = () => {
    const name = mkdirName.trim();
    if (!name) return;
    const target = path ? `${path}/${name}` : name;
    api
      .workspaceMkdir(target)
      .then(() => {
        toast.success("已创建目录");
        setMkdirOpen(false);
        setMkdirName("");
        load(path);
      })
      .catch((err) => toast.error(`创建失败：${(err as Error).message}`));
  };

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">工作空间</h1>
        <p className="text-muted-foreground text-sm">任务产物与上传文件的浏览、编辑与下载。</p>
      </div>
      {/* 头部：面包屑 + 操作 */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-1 text-sm">
          <HardDriveIcon className="mr-1 size-4 shrink-0 text-muted-foreground" />
          {crumbs.map((c, i) => (
            <React.Fragment key={c.path}>
              {i > 0 && <span className="text-muted-foreground">/</span>}
              <button
                type="button"
                onClick={() => load(c.path)}
                className={cn(
                  "max-w-[160px] truncate rounded px-1.5 py-0.5 hover:bg-accent",
                  i === crumbs.length - 1 ? "font-medium text-foreground" : "text-muted-foreground",
                )}
              >
                {c.name}
              </button>
            </React.Fragment>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setMkdirOpen(true)}>
            <FolderPlusIcon /> 新建文件夹
          </Button>
          <Button variant="outline" size="sm" onClick={() => uploadRef.current?.click()} disabled={uploading}>
            <UploadIcon /> {uploading ? "上传中…" : "上传"}
          </Button>
          <Button variant="ghost" size="icon" className="size-8" onClick={() => load(path)} title="刷新">
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
          </Button>
          <input ref={uploadRef} type="file" multiple className="hidden" onChange={(e) => doUpload(e.target.files)} />
        </div>
      </div>

      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead className="w-28 text-right">大小</TableHead>
                <TableHead className="w-40">修改时间</TableHead>
                <TableHead className="w-24 text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4} className="py-10 text-center text-muted-foreground text-sm">
                    {loading ? "加载中…" : "空目录"}
                  </TableCell>
                </TableRow>
              )}
              {entries.map((e) => (
                <TableRow key={e.path}>
                  <TableCell>
                    <button
                      type="button"
                      onClick={() => (e.dir ? load(e.path) : openFile(e))}
                      className="flex items-center gap-2 text-left hover:underline"
                    >
                      {e.dir ? (
                        <FolderIcon className="size-4 shrink-0 text-blue-500" />
                      ) : (
                        <FileIcon className="size-4 shrink-0 text-muted-foreground" />
                      )}
                      <span className="truncate font-mono text-sm">{e.name}</span>
                    </button>
                  </TableCell>
                  <TableCell className="text-right font-mono text-muted-foreground text-xs">
                    {e.dir ? "—" : fmtSize(e.size)}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">{fmtTime(e.mtime)}</TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end gap-1">
                      {!e.dir && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-7"
                          title="下载"
                          onClick={() =>
                            api
                              .workspaceDownload(e.path)
                              .catch((err) => toast.error(`下载失败：${(err as Error).message}`))
                          }
                        >
                          <DownloadIcon className="size-3.5" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-7 text-destructive"
                        title="删除"
                        onClick={() => del(e)}
                      >
                        <Trash2Icon className="size-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {/* 文件查看 / 编辑 */}
      <Dialog open={edit !== null} onOpenChange={(o) => !o && closeEditor()}>
        <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
          {edit && (
            <>
              <DialogHeader className="border-b p-4">
                <DialogTitle className="flex items-center gap-2 truncate font-mono text-sm">
                  <FileIcon className="size-4 shrink-0" />
                  <span className="truncate" title={edit.file.path}>
                    {edit.file.path}
                  </span>
                </DialogTitle>
                <span className="text-muted-foreground text-xs">{fmtSize(edit.file.size)}</span>
              </DialogHeader>

              {edit.file.binary || edit.file.too_large ? (
                <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
                  <p className="text-muted-foreground text-sm">
                    {edit.file.too_large ? "文件过大，不支持在线预览/编辑。" : "二进制文件，不支持在线预览/编辑。"}
                  </p>
                  <Button variant="outline" onClick={() => api.workspaceDownload(edit.file.path)}>
                    <DownloadIcon /> 下载文件
                  </Button>
                </div>
              ) : (
                <>
                  <div className="min-h-0 flex-1 p-3">
                    <Textarea
                      value={edit.content}
                      onChange={(ev) =>
                        setEdit({
                          ...edit,
                          content: ev.target.value,
                          dirty: workspaceDirty(ev.target.value, edit.file.content),
                        })
                      }
                      disabled={edit.saving}
                      spellCheck={false}
                      className="h-full min-h-[50vh] resize-none font-mono text-xs leading-relaxed"
                    />
                  </div>
                  <DialogFooter className="flex-row items-center justify-between border-t p-3">
                    <span className="text-muted-foreground text-xs">{edit.dirty ? "未保存的修改" : "已同步"}</span>
                    <div className="flex gap-2">
                      <Button
                        variant="outline"
                        disabled={edit.saving}
                        onClick={() => {
                          if (edit.dirty && !window.confirm("确认丢弃当前草稿并重新读取服务器版本吗？")) return;
                          openFile(
                            { name: edit.file.path, path: edit.file.path, dir: false, size: edit.file.size, mtime: 0 },
                            true,
                          );
                        }}
                      >
                        重新读取
                      </Button>
                      <Button variant="outline" onClick={() => api.workspaceDownload(edit.file.path)}>
                        <DownloadIcon /> 下载
                      </Button>
                      <Button onClick={saveFile} disabled={!edit.dirty || edit.saving}>
                        <SaveIcon /> {edit.saving ? "保存中…" : "保存"}
                      </Button>
                    </div>
                  </DialogFooter>
                </>
              )}
            </>
          )}
        </DialogContent>
      </Dialog>

      {/* 新建文件夹 */}
      <Dialog open={mkdirOpen} onOpenChange={setMkdirOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>新建文件夹</DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            value={mkdirName}
            onChange={(e) => setMkdirName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && doMkdir()}
            placeholder="文件夹名称"
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setMkdirOpen(false)}>
              取消
            </Button>
            <Button onClick={doMkdir} disabled={!mkdirName.trim()}>
              创建
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
