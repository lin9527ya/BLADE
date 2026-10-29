"use client";

import * as React from "react";

import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { PlannerTemplate } from "@/lib/types";
import { cn } from "@/lib/utils";

// planner 模板库：多套 planner 正文（外网/内网/代码审计），任务创建时按 id 快照。
// 正文变量：{{.Goal}} {{.Scope}} {{.AssetSummary}} {{.DataDir}} {{.Now}}。
const VARS_HINT = "{{.Goal}} {{.Scope}} {{.AssetSummary}} {{.DataDir}} {{.Now}}";

export function PlannerTemplateManager({ onChanged }: { onChanged?: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [templates, setTemplates] = React.useState<PlannerTemplate[]>([]);
  const [selectedID, setSelectedID] = React.useState<number | "new" | null>(null);
  const [name, setName] = React.useState("");
  const [body, setBody] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    return api
      .plannerTemplates()
      .then((rows) => {
        setTemplates(rows);
        if (selectedID === null && rows.length > 0) {
          setSelectedID(rows[0].id);
          setName(rows[0].name);
          setBody(rows[0].body);
        }
      })
      .catch(() => setTemplates([]));
  }, [selectedID]);

  React.useEffect(() => {
    if (!open) return;
    void load();
  }, [open, load]);

  const selected = typeof selectedID === "number" ? templates.find((t) => t.id === selectedID) : undefined;

  function selectTemplate(tpl: PlannerTemplate) {
    setSelectedID(tpl.id);
    setName(tpl.name);
    setBody(tpl.body);
  }

  function startNew() {
    setSelectedID("new");
    setName("");
    setBody("");
  }

  async function save() {
    if (busy) return;
    const trimmed = name.trim();
    if (!trimmed || !body.trim()) {
      toast.error("名称与正文都不能为空");
      return;
    }
    setBusy(true);
    try {
      const check = await api.validatePlannerTemplate(body);
      if (!check.ok) throw new Error("模板校验失败");
      if (selectedID === "new" || selectedID === null) {
        const created = await api.createPlannerTemplate(trimmed, body);
        setSelectedID(created.id);
        toast.success("模板已创建");
      } else {
        await api.updatePlannerTemplate(selectedID, trimmed, body);
        toast.success("模板已保存");
      }
      await load();
      onChanged?.();
    } catch (error) {
      toast.error("保存失败：" + (error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function setDefault() {
    if (typeof selectedID !== "number" || busy) return;
    setBusy(true);
    try {
      await api.setDefaultPlannerTemplate(selectedID);
      await load();
      onChanged?.();
      toast.success("已设为默认模板");
    } catch (error) {
      toast.error("设置默认失败：" + (error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (typeof selectedID !== "number" || busy) return;
    setBusy(true);
    try {
      await api.deletePlannerTemplate(selectedID);
      setSelectedID(null);
      setName("");
      setBody("");
      await load();
      onChanged?.();
      toast.success("模板已删除");
    } catch (error) {
      toast.error("删除失败：" + (error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          planner 模板
        </Button>
      </DialogTrigger>
      <DialogContent className="grid max-h-[88vh] w-full grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-4xl">
        <DialogHeader className="border-b px-6 py-5">
          <DialogTitle>planner 模板</DialogTitle>
          <DialogDescription>
            建任务时按模板 id 快照正文；之后修改模板不影响已建任务。正文变量：{VARS_HINT}
          </DialogDescription>
        </DialogHeader>
        <div className="grid min-h-0 overflow-y-auto lg:grid-cols-[15rem_minmax(0,1fr)] lg:overflow-hidden">
          <div className="flex min-h-0 flex-col gap-2 border-b p-3 lg:border-r lg:border-b-0">
            <Button type="button" variant="outline" size="sm" onClick={startNew}>
              新建模板
            </Button>
            <ScrollArea className="max-h-44 lg:max-h-none lg:flex-1">
              <div className="flex flex-col gap-1 pr-2">
                {templates.map((tpl) => (
                  <button
                    key={tpl.id}
                    type="button"
                    onClick={() => selectTemplate(tpl)}
                    className={cn(
                      "flex flex-col rounded-md border px-2 py-1.5 text-left text-xs",
                      selectedID === tpl.id ? "border-primary bg-primary/10" : "border-transparent hover:bg-muted",
                    )}
                  >
                    <span className="truncate font-medium">{tpl.name}</span>
                    <span className="text-[10px] text-muted-foreground">
                      {tpl.is_default ? "默认 · " : ""}
                      {tpl.builtin ? "内置（只读）" : "自建"}
                    </span>
                  </button>
                ))}
                {templates.length === 0 && <span className="p-2 text-xs text-muted-foreground">暂无模板</span>}
              </div>
            </ScrollArea>
          </div>
          <ScrollArea className="min-h-0">
            <div className="flex flex-col gap-4 p-6">
              <Field>
                <FieldLabel htmlFor="planner-template-name">名称</FieldLabel>
                <Input
                  id="planner-template-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  maxLength={120}
                  disabled={selected?.builtin}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="planner-template-body">正文</FieldLabel>
                <Textarea
                  id="planner-template-body"
                  className="min-h-72 font-mono text-xs"
                  value={body}
                  onChange={(e) => setBody(e.target.value)}
                  disabled={selected?.builtin}
                />
                <FieldDescription>
                  名称支持中文；正文上限 32KB。保存前会做试渲染，引用目录外变量会被拒绝。内置模板只读，可另存为新模板后修改。
                </FieldDescription>
              </Field>
            </div>
          </ScrollArea>
        </div>
        <div className="flex flex-wrap items-center justify-end gap-2 border-t px-6 py-3">
          {typeof selectedID === "number" && !selected?.is_default && (
            <Button size="sm" variant="ghost" onClick={() => void setDefault()} disabled={busy}>
              设为默认
            </Button>
          )}
          {typeof selectedID === "number" && !selected?.builtin && (
            <Button size="sm" variant="ghost" onClick={() => void remove()} disabled={busy}>
              删除
            </Button>
          )}
          <Button size="sm" onClick={() => void save()} disabled={busy || selected?.builtin}>
            {busy ? <Spinner data-icon="inline-start" /> : null}
            保存
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
