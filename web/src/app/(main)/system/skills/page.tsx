"use client";

import * as React from "react";
import { SkillsOverview } from "./_components/skills-overview";

import { AlertTriangleIcon, ChevronRightIcon, EyeIcon, FilePlusIcon, FileTextIcon, FolderIcon, FolderOpenIcon, FolderPlusIcon, PlusIcon, SearchIcon, Trash2Icon, UploadIcon } from "lucide-react";

import { Markdown } from "@/components/markdown";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";

import { Agent } from "@/lib/types";
import { cn } from "@/lib/utils";

import { buildTree, fmtTime } from "./_components/skills-helpers";
import { useSkillsState } from "./_components/use-skills-state";

export default function SkillsPage() {
  const {
    skills,
    setSkills,
    agents,
    setAgents,
    selected,
    setSelected,
    visibility,
    setVisibility,
    expanded,
    setExpanded,
    fileContent,
    setFileContent,
    fileLoading,
    setFileLoading,
    dirty,
    setDirty,
    saving,
    setSaving,
    creating,
    setCreating,
    newEntryName,
    setNewEntryName,
    inlineRef,
    cancelRef,
    newOpen,
    setNewOpen,
    newName,
    setNewName,
    newDesc,
    setNewDesc,
    newLicense,
    setNewLicense,
    newCompat,
    setNewCompat,
    newInst,
    setNewInst,
    newMcps,
    setNewMcps,
    newVisibility,
    setNewVisibility,
    mcpOptions,
    setMcpOptions,
    creatingSkill,
    setCreatingSkill,
    uploading,
    setUploading,
    uploadRef,
    detailMcps,
    setDetailMcps,
    pendingDelete,
    setPendingDelete,
    deleting,
    setDeleting,
    usageCalls,
    setUsageCalls,
    usageLoading,
    setUsageLoading,
    missing,
    setMissing,
    skillQuery,
    setSkillQuery,
    filteredSkills,
    load,
    onUploadPick,
    toggleExpanded,
    ensureExpanded,
    startCreate,
    creatingRef,
    newEntryRef,
    cancelCreate,
    inlineInputJSX,
    renderTree,
    selectedSkill,
    uploadZip,
    commitCreate,
    deletePath,
    deleteSkill,
    runPendingDelete,
    saveFile,
    toggleSkillMcp,
    toggleVisibility,
    createNewSkill,
  } = useSkillsState();

  return (
    <div data-content-padding="false" className="flex flex-1 flex-col overflow-hidden">
      <div className="flex items-center gap-3 border-b px-4 py-2.5 lg:px-6">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-sm font-semibold leading-tight">Skill</h1>
          <p className="text-muted-foreground text-xs">技能库 · agentskills.io 规范 · 按 Agent 授权可见</p>
        </div>
        {/* 缺口清单：agent 点名调用、但库里没有的 skill —— 直接是该补什么的依据。 */}
        {missing.length > 0 && (
          <Popover>
            <PopoverTrigger asChild>
              <Button size="sm" variant="outline" className="ml-auto">
                <AlertTriangleIcon className="size-3.5 text-amber-500" />
                {missing.length} 个未命中调用
              </Button>
            </PopoverTrigger>
            <PopoverContent align="end" className="w-80">
              <p className="mb-2 text-xs text-muted-foreground">
                Agent 点名调用、但技能库里不存在的 skill。按被点名次数排序。
              </p>
              <div className="space-y-1">
                {missing.map((m) => (
                  <div key={m.skill} className="flex items-center gap-2 text-sm">
                    <code className="min-w-0 flex-1 truncate font-mono text-xs" title={m.skill}>
                      {m.skill}
                    </code>
                    <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{m.calls} 次</span>
                    <span className="shrink-0 text-xs text-muted-foreground">{fmtTime(m.last_used)}</span>
                  </div>
                ))}
              </div>
            </PopoverContent>
          </Popover>
        )}
      </div>
      <div className="flex flex-1 overflow-hidden">
        {/* ── 左侧文件树 ── */}
        <div className="flex w-64 shrink-0 flex-col border-r">
          <div className="flex flex-col gap-2 border-b p-2">
            <Button size="sm" variant="outline" className="w-full" onClick={() => setNewOpen(true)}>
              <PlusIcon className="size-3.5" />
              新建 Skill
            </Button>
            <input
              ref={uploadRef}
              type="file"
              accept=".zip,application/zip"
              className="hidden"
              onChange={onUploadPick}
            />
            <Button
              size="sm"
              variant="outline"
              className="w-full"
              disabled={uploading}
              onClick={() => uploadRef.current?.click()}
              title="上传包含 SKILL.md 的 .zip 压缩包"
            >
              <UploadIcon className="size-3.5" />
              {uploading ? "上传中…" : "上传压缩包"}
            </Button>
            <div className="relative">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
              <Input
                className="h-8 pl-7 text-xs"
                placeholder="搜索技能 / 描述 / 文件名"
                value={skillQuery}
                onChange={(e) => setSkillQuery(e.target.value)}
              />
            </div>
            {skillQuery.trim() !== "" && (
              <p className="text-muted-foreground text-[11px]">
                {filteredSkills.length} / {skills.length} 个技能命中
              </p>
            )}
          </div>
          {/* Radix viewport wraps children in a display:table div that grows with
              content — force it to block so long names truncate instead of
              widening the rows past the sidebar. */}
          <ScrollArea className="flex-1 [&>[data-slot=scroll-area-viewport]>div]:!block">
            <div className="p-1">
              {filteredSkills.map((s) => {
                const isOpen = expanded.has(s.name);
                const tree = buildTree(s.files);
                const isSkillSelected = selected?.skill === s.name && selected.path === null;
                const granted = (visibility[s.name] ?? []).length;
                return (
                  <div key={s.name}>
                    {/* skill 根节点 */}
                    {/* biome-ignore lint/a11y/useSemanticElements: 节点内含自己的操作按钮，用原生 <button> 会造成按钮嵌套（非法 HTML） */}
                    <div
                      role="button"
                      tabIndex={0}
                      title={[
                        s.meta_error ? `⚠ frontmatter 无效：${s.meta_error}` : "",
                        s.meta_warning ? `⚠ ${s.meta_warning}` : "",
                        s.description || "（无描述）",
                        `${s.files.length} 个文件`,
                        granted > 0 ? `${granted} 个 Agent 可见` : "未授权给任何 Agent——谁都调不到它",
                      ]
                        .filter(Boolean)
                        .join("\n")}
                      className={cn(
                        "group relative flex cursor-pointer select-none items-center gap-1 rounded px-2 py-1 text-sm",
                        isSkillSelected ? "bg-accent text-accent-foreground" : "hover:bg-muted",
                      )}
                      onClick={() => {
                        toggleExpanded(s.name);
                        setSelected({ skill: s.name, path: null });
                      }}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          toggleExpanded(s.name);
                          setSelected({ skill: s.name, path: null });
                        }
                      }}
                    >
                      <ChevronRightIcon
                        className={cn("size-3.5 shrink-0 transition-transform", isOpen && "rotate-90")}
                      />
                      {isOpen ? (
                        <FolderOpenIcon className="size-3.5 shrink-0 text-blue-500" />
                      ) : (
                        <FolderIcon className="size-3.5 shrink-0 text-blue-500" />
                      )}
                      <span className="min-w-0 flex-1 truncate font-semibold" title={s.name}>
                        {s.name}
                      </span>
                      {s.meta_error && (
                        <span
                          className="border-destructive/50 text-destructive shrink-0 rounded border px-1 text-[10px]"
                          title={`frontmatter 无效：${s.meta_error}`}
                        >
                          frontmatter 无效
                        </span>
                      )}
                      {granted === 0 && (
                        <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground">未授权</span>
                      )}
                      {s.calls > 0 && (
                        <span
                          className="shrink-0 rounded bg-muted px-1 text-[10px] tabular-nums text-muted-foreground"
                          title={`被调用 ${s.calls} 次 · 最近 ${fmtTime(s.last_used)}`}
                        >
                          {s.calls}
                        </span>
                      )}
                      <span
                        className={cn(
                          "absolute inset-y-0 right-1 hidden items-center gap-0.5 rounded pl-1 group-hover:flex",
                          isSkillSelected ? "bg-accent" : "bg-muted",
                        )}
                      >
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-5"
                          title="新建文件"
                          onClick={(e) => {
                            e.stopPropagation();
                            startCreate(s.name, "", "file");
                          }}
                        >
                          <FilePlusIcon className="size-3 text-muted-foreground" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-5"
                          title="新建文件夹"
                          onClick={(e) => {
                            e.stopPropagation();
                            startCreate(s.name, "", "dir");
                          }}
                        >
                          <FolderPlusIcon className="size-3 text-muted-foreground" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-5"
                          title="删除 Skill"
                          onClick={(e) => {
                            e.stopPropagation();
                            setPendingDelete({ kind: "skill", skill: s.name });
                          }}
                        >
                          <Trash2Icon className="size-3 text-destructive" />
                        </Button>
                      </span>
                    </div>

                    {/* 展开：递归文件树 */}
                    {isOpen && (
                      <>
                        {renderTree(tree, s.name, 0)}
                        {creating?.skill === s.name && creating.inDir === "" && inlineInputJSX(22)}
                      </>
                    )}
                  </div>
                );
              })}

              {skills.length === 0 && <p className="p-3 text-xs text-muted-foreground">暂无 Skill，点击「新建」开始</p>}
              {skills.length > 0 && filteredSkills.length === 0 && (
                <p className="p-3 text-xs text-muted-foreground">没有匹配「{skillQuery}」的技能。</p>
              )}
            </div>
          </ScrollArea>
        </div>

        {/* ── 右侧面板 ── */}
        <div className="flex flex-1 flex-col overflow-auto p-4">
          {!selected && (
            <SkillsOverview
              skills={skills}
              missing={missing}
              onSelect={(name) => setSelected({ skill: name, path: null })}
            />
          )}

          {selected && selected.path === null && selectedSkill && (
            <div className="max-w-5xl space-y-5">
              <div>
                <h2 className="font-mono text-base font-semibold">{selectedSkill.name}</h2>
                {selectedSkill.description && (
                  <p className="mt-1 text-sm text-muted-foreground">{selectedSkill.description}</p>
                )}
                <div className="mt-2 flex flex-wrap gap-2">
                  {selectedSkill.license && (
                    <Badge variant="outline" className="text-xs font-normal">
                      License: {selectedSkill.license}
                    </Badge>
                  )}
                  {selectedSkill.compatibility && (
                    <Badge variant="secondary" className="text-xs font-normal">
                      {selectedSkill.compatibility}
                    </Badge>
                  )}
                </div>
              </div>

              {/* frontmatter 体检：硬错误（写入会被拒）红、软警告（缺字段）琥珀。
                  拒绝不等于死路——文件编辑器是逐字节保存的，给个直达按钮。 */}
              {(selectedSkill.meta_error || selectedSkill.meta_warning) && (
                <div
                  className={cn(
                    "flex flex-wrap items-start gap-2 rounded-md border px-3 py-2 text-xs leading-relaxed",
                    selectedSkill.meta_error
                      ? "border-destructive/50 bg-destructive/5 text-destructive"
                      : "border-amber-500/50 bg-amber-500/5 text-amber-600 dark:text-amber-400",
                  )}
                >
                  <span className="min-w-0 flex-1">
                    <b>{selectedSkill.meta_error ? "frontmatter 无效：" : "清单提示："}</b>
                    {selectedSkill.meta_error ?? selectedSkill.meta_warning}
                    {selectedSkill.meta_error && "　元数据改动（关联 MCP / 描述等）会被拒绝并说明原因。"}
                  </span>
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-6 shrink-0 px-2 text-xs"
                    onClick={() => setSelected({ skill: selectedSkill.name, path: "SKILL.md" })}
                  >
                    <FileTextIcon className="size-3" /> 打开 SKILL.md
                  </Button>
                </div>
              )}

              {/* 左右分栏：配置（MCP/可见性）在左为主，调用统计在右为辅。
                  lg 以下放不下时用 flex-row-reverse 回落到单列——统计因 DOM 顺序在前，
                  窄屏时自然落到配置上方（与改版前的上下顺序一致）。 */}
              <div className="flex flex-col gap-6 lg:flex-row-reverse lg:items-start">
                {/* ── 右侧：调用统计 ── */}
                <div className="space-y-2 lg:w-80 lg:shrink-0">
                  <Label className="text-xs text-muted-foreground">调用统计</Label>
                  <div className="grid grid-cols-3 gap-2">
                    <div className="rounded-md border p-2">
                      <p className="text-lg font-semibold tabular-nums">{selectedSkill.calls}</p>
                      <p className="text-xs text-muted-foreground">总调用次数</p>
                    </div>
                    <div className="rounded-md border p-2">
                      <p className="text-lg font-semibold tabular-nums">{selectedSkill.tasks}</p>
                      <p className="text-xs text-muted-foreground">覆盖任务数</p>
                    </div>
                    <div className="rounded-md border p-2">
                      <p className="truncate text-sm font-medium" title={fmtTime(selectedSkill.last_used)}>
                        {fmtTime(selectedSkill.last_used)}
                      </p>
                      <p className="text-xs text-muted-foreground">最近调用</p>
                    </div>
                  </div>
                  {selectedSkill.usage_agents.length > 0 && (
                    <div className="flex flex-wrap items-center gap-1">
                      <span className="text-xs text-muted-foreground">调用方：</span>
                      {selectedSkill.usage_agents.map((k) => (
                        <Badge key={k} variant="secondary" className="text-xs font-normal">
                          {k}
                        </Badge>
                      ))}
                    </div>
                  )}
                  {usageLoading ? (
                    <p className="text-xs text-muted-foreground">加载调用明细…</p>
                  ) : usageCalls.length > 0 ? (
                    <div className="rounded-md border">
                      <div className="border-b px-2 py-1 text-xs text-muted-foreground">
                        最近 {usageCalls.length} 次调用
                      </div>
                      <div className="max-h-56 overflow-y-auto">
                        {usageCalls.map((c, i) => (
                          <div
                            key={`${c.ts}-${i}`}
                            className="flex items-center gap-2 border-b px-2 py-1 text-xs last:border-b-0"
                          >
                            <span className="tabular-nums text-muted-foreground">{fmtTime(c.ts)}</span>
                            <Badge variant="outline" className="font-normal">
                              {c.agent_key || "—"}
                            </Badge>
                            <span className="ml-auto text-muted-foreground">
                              {c.task_id > 0 ? `任务 #${c.task_id}` : c.session_id ? "对话会话" : "—"}
                            </span>
                          </div>
                        ))}
                      </div>
                    </div>
                  ) : (
                    <p className="text-xs text-muted-foreground">还没有调用记录。</p>
                  )}
                </div>

                {/* ── 左侧：关联 MCP + 可见性 ── */}
                <div className="space-y-5 lg:min-w-0 lg:flex-1">
                  <div className="space-y-2">
                    <Label className="text-xs text-muted-foreground">
                      关联 MCP
                      <span className="ml-1 font-normal">（加载 Skill 时才披露/解锁其工具）</span>
                    </Label>
                    {mcpOptions.length === 0 ? (
                      <p className="text-xs text-muted-foreground">暂无 MCP，可在「MCP」页添加。</p>
                    ) : (
                      <div className="flex flex-wrap gap-x-4 gap-y-2">
                        {mcpOptions.map((m) => (
                          <label key={m.id} className="flex cursor-pointer items-center gap-2 text-sm">
                            <Checkbox
                              checked={detailMcps.includes(m.name)}
                              onCheckedChange={(on) => toggleSkillMcp(selectedSkill.name, m.name, !!on)}
                            />
                            {m.name}
                          </label>
                        ))}
                      </div>
                    )}
                  </div>

                  <div className="space-y-2">
                    <Label className="text-xs text-muted-foreground">可见性（按 Agent 授权）</Label>
                    <div className="space-y-2">
                      {agents.map((a) => (
                        <label key={a.key} className="flex cursor-pointer items-center gap-2 text-sm">
                          <Checkbox
                            checked={(visibility[selectedSkill.name] ?? []).includes(a.id)}
                            onCheckedChange={() => toggleVisibility(selectedSkill.name, a.id, a.name)}
                          />
                          {a.name}
                        </label>
                      ))}
                      {agents.length === 0 && <span className="text-xs text-muted-foreground">（暂无 Agent）</span>}
                    </div>
                  </div>
                </div>
              </div>
            </div>
          )}

          {selected && selected.path !== null && (
            <div className="flex h-full flex-col gap-2">
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <span className="font-medium text-foreground">{selected.skill}</span>
                <span>/</span>
                <span className="font-mono">{selected.path}</span>
                {/* markdown 文件给个渲染预览（照 agent-editor 的现成模式）：渲染的是编辑器里的
                    当前内容，含尚未保存的改动——文本本来就在 state 里，不需要后端预览接口。 */}
                {selected.path.toLowerCase().endsWith(".md") && (
                  <Dialog>
                    <DialogTrigger asChild>
                      <Button size="sm" variant="outline" className="ml-auto">
                        <EyeIcon className="size-3.5" /> 预览渲染
                      </Button>
                    </DialogTrigger>
                    <DialogContent className="sm:max-w-2xl">
                      <DialogHeader>
                        <DialogTitle>渲染预览</DialogTitle>
                        <DialogDescription>
                          {selected.skill} / {selected.path}（含尚未保存的改动）
                        </DialogDescription>
                      </DialogHeader>
                      <div className="bg-muted/30 max-h-[60vh] overflow-auto rounded-md border p-3 text-sm">
                        <Markdown text={fileContent} />
                      </div>
                    </DialogContent>
                  </Dialog>
                )}
                <Button
                  size="sm"
                  className={selected.path.toLowerCase().endsWith(".md") ? undefined : "ml-auto"}
                  onClick={saveFile}
                  disabled={!dirty || saving}
                >
                  {saving ? "保存中…" : "保存"}
                </Button>
              </div>
              {fileLoading ? (
                <p className="text-xs text-muted-foreground">加载中…</p>
              ) : (
                <Textarea
                  className="flex-1 resize-none font-mono text-xs"
                  value={fileContent}
                  onChange={(e) => {
                    setFileContent(e.target.value);
                    setDirty(true);
                  }}
                />
              )}
            </div>
          )}
        </div>
      </div>

      {/* ── 删除二次确认 ── */}
      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(o) => {
          if (!o) setPendingDelete(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingDelete?.kind === "skill" && `删除 Skill「${pendingDelete.skill}」？`}
              {pendingDelete?.kind === "dir" && `删除文件夹「${pendingDelete.path}」？`}
              {pendingDelete?.kind === "file" && `删除文件「${pendingDelete.path}」？`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete?.kind === "skill"
                ? "将删除该 Skill 的全部文件、MCP 关联与可见性配置。此操作不可撤销。"
                : pendingDelete?.kind === "dir"
                  ? "将一并删除该文件夹下的所有文件。此操作不可撤销。"
                  : "此操作不可撤销。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={deleting}
              onClick={(e) => {
                e.preventDefault();
                void runPendingDelete();
              }}
            >
              {deleting ? "删除中…" : "删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* ── 新建 Skill 对话框 ── */}
      <Dialog open={newOpen} onOpenChange={setNewOpen}>
        <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
          <DialogHeader className="border-b px-4 py-3">
            <DialogTitle>新建 Skill</DialogTitle>
          </DialogHeader>
          <Tabs defaultValue="basic" className="flex min-h-0 flex-1 flex-col">
            <TabsList className="mx-4 mt-3 shrink-0 justify-start">
              <TabsTrigger value="basic">基本信息</TabsTrigger>
              <TabsTrigger value="mcp">
                关联 MCP
                {newMcps.length > 0 && (
                  <span className="ml-1.5 rounded-full bg-primary px-1.5 py-0.5 text-[10px] leading-none text-primary-foreground">
                    {newMcps.length}
                  </span>
                )}
              </TabsTrigger>
              <TabsTrigger value="visibility">
                可见性
                {newVisibility.length > 0 && (
                  <span className="ml-1.5 rounded-full bg-primary px-1.5 py-0.5 text-[10px] leading-none text-primary-foreground">
                    {newVisibility.length}
                  </span>
                )}
              </TabsTrigger>
            </TabsList>

            {/* 基本信息 */}
            <TabsContent
              value="basic"
              className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4 pt-4 data-[state=inactive]:hidden"
            >
              <div className="grid gap-1.5">
                <Label htmlFor="sk-name">
                  名称 <span className="text-destructive">*</span>
                </Label>
                <Input
                  id="sk-name"
                  placeholder="sqli-deepdive"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                />
                <p className="text-muted-foreground text-xs">小写字母 / 数字 / 连字符，1–64 字符</p>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="sk-desc">
                  描述 <span className="text-destructive">*</span>
                </Label>
                <Textarea
                  id="sk-desc"
                  rows={2}
                  className="resize-none"
                  placeholder="这个 skill 做什么、何时使用。"
                  value={newDesc}
                  onChange={(e) => setNewDesc(e.target.value)}
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">license</Label>
                  <Input
                    placeholder="MIT / Proprietary"
                    value={newLicense}
                    onChange={(e) => setNewLicense(e.target.value)}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label className="text-muted-foreground text-xs">compatibility</Label>
                  <Input
                    placeholder="需要 sqlmap、python3"
                    value={newCompat}
                    onChange={(e) => setNewCompat(e.target.value)}
                  />
                </div>
              </div>
              <div className="flex min-h-0 flex-1 flex-col gap-1.5">
                <Label htmlFor="sk-inst">
                  正文 <span className="text-muted-foreground text-xs font-normal">（留空自动生成骨架）</span>
                </Label>
                <Textarea
                  id="sk-inst"
                  className="min-h-40 flex-1 resize-none font-mono text-sm leading-relaxed"
                  placeholder={"## 执行方法\n\n1. 先探测错误\n2. 区分盲注类型\n\n脚本放 scripts/ 目录。"}
                  value={newInst}
                  onChange={(e) => setNewInst(e.target.value)}
                />
              </div>
            </TabsContent>

            {/* 关联 MCP */}
            <TabsContent value="mcp" className="overflow-y-auto px-4 pb-4 pt-4 data-[state=inactive]:hidden">
              <p className="mb-3 text-xs text-muted-foreground">加载 Skill 时才披露并解锁所选 MCP 的工具。</p>
              {mcpOptions.length === 0 ? (
                <p className="text-xs text-muted-foreground">暂无 MCP，可在「MCP」页添加。</p>
              ) : (
                <div className="flex flex-col gap-2">
                  {mcpOptions.map((m) => (
                    <label key={m.id} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox
                        checked={newMcps.includes(m.name)}
                        onCheckedChange={(on) =>
                          setNewMcps((cur) => (on ? [...cur, m.name] : cur.filter((n) => n !== m.name)))
                        }
                      />
                      {m.name}
                    </label>
                  ))}
                </div>
              )}
            </TabsContent>

            {/* 可见性 */}
            <TabsContent value="visibility" className="overflow-y-auto px-4 pb-4 pt-4 data-[state=inactive]:hidden">
              <p className="mb-3 text-xs text-muted-foreground">选中的 Agent 创建后即可见此 Skill。</p>
              {agents.length === 0 ? (
                <p className="text-xs text-muted-foreground">（暂无 Agent）</p>
              ) : (
                <div className="flex flex-col gap-2">
                  {agents.map((a) => (
                    <label key={a.key} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox
                        checked={newVisibility.includes(a.id)}
                        onCheckedChange={(on) =>
                          setNewVisibility((cur) => (on ? [...cur, a.id] : cur.filter((id) => id !== a.id)))
                        }
                      />
                      {a.name}
                    </label>
                  ))}
                </div>
              )}
            </TabsContent>
          </Tabs>

          <DialogFooter className="flex-row justify-end gap-2 border-t px-4 py-3">
            <Button variant="outline" onClick={() => setNewOpen(false)}>
              取消
            </Button>
            <Button onClick={createNewSkill} disabled={creatingSkill}>
              {creatingSkill ? "创建中…" : "创建"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
