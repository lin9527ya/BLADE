"use client";

import * as React from "react";

import { PlusIcon, SaveIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { LLMPrice } from "@/lib/types";

type DraftWindow = {
  // Stable key so editing a time does not remount the input.
  id: string;
  start: string;
  end: string;
};

type Draft = {
  // Stable React key: model edits must not remount the row/input.
  id: string;
  model: string;
  input: string;
  output: string;
  cacheRead: string;
  cacheWrite: string;
  offpeakWindows: DraftWindow[];
  offpeakTimezone: string;
  offpeakInput: string;
  offpeakOutput: string;
  offpeakCacheRead: string;
  offpeakCacheWrite: string;
};

let draftSeq = 0;
function nextDraftID(): string {
  draftSeq += 1;
  return `price-row-${draftSeq}`;
}

function draftOf(p: LLMPrice): Draft {
  return {
    id: nextDraftID(),
    model: p.model,
    input: String(p.input_per_million),
    output: String(p.output_per_million),
    cacheRead: String(p.cache_read_per_million),
    cacheWrite: String(p.cache_write_per_million),
    offpeakWindows: (p.offpeak_windows ?? []).map((window) => ({
      id: nextDraftID(),
      start: window.start,
      end: window.end,
    })),
    offpeakTimezone: p.offpeak_timezone || "Asia/Shanghai",
    offpeakInput: String(p.offpeak_input_per_million),
    offpeakOutput: String(p.offpeak_output_per_million),
    offpeakCacheRead: String(p.offpeak_cache_read_per_million),
    offpeakCacheWrite: String(p.offpeak_cache_write_per_million),
  };
}

function num(value: string): number {
  const n = Number(value.trim() === "" ? "0" : value);
  return Number.isFinite(n) ? n : Number.NaN;
}

// 模型价格表：按 model 精确匹配；default 行是未登记模型的兜底。
export function LLMPriceTab() {
  const [rows, setRows] = React.useState<Draft[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .llmPrices()
      .then((items) => setRows(items.map(draftOf)))
      .catch((e: Error) => toast.error(`读取价格表失败：${e.message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  function patch(index: number, key: keyof Draft, value: string) {
    setRows((current) => current.map((row, i) => (i === index ? { ...row, [key]: value } : row)));
  }

  function patchWindow(index: number, windowID: string, key: "start" | "end", value: string) {
    setRows((current) =>
      current.map((row, i) =>
        i === index
          ? {
              ...row,
              offpeakWindows: row.offpeakWindows.map((window) =>
                window.id === windowID ? { ...window, [key]: value } : window,
              ),
            }
          : row,
      ),
    );
  }

  function addWindow(index: number) {
    setRows((current) =>
      current.map((row, i) =>
        i === index
          ? { ...row, offpeakWindows: [...row.offpeakWindows, { id: nextDraftID(), start: "", end: "" }] }
          : row,
      ),
    );
  }

  function removeWindow(index: number, windowID: string) {
    setRows((current) =>
      current.map((row, i) =>
        i === index ? { ...row, offpeakWindows: row.offpeakWindows.filter((window) => window.id !== windowID) } : row,
      ),
    );
  }

  function addRow() {
    setRows((current) => [
      ...current,
      {
        id: nextDraftID(),
        model: "",
        input: "0",
        output: "0",
        cacheRead: "0",
        cacheWrite: "0",
        offpeakWindows: [],
        offpeakTimezone: "Asia/Shanghai",
        offpeakInput: "0",
        offpeakOutput: "0",
        offpeakCacheRead: "0",
        offpeakCacheWrite: "0",
      },
    ]);
  }

  function removeRow(index: number) {
    setRows((current) => current.filter((_, i) => i !== index));
  }

  async function save() {
    const seen = new Set<string>();
    const items: LLMPrice[] = [];
    for (const row of rows) {
      const model = row.model.trim();
      if (!model) {
        toast.error("模型名不能为空");
        return;
      }
      if (seen.has(model)) {
        toast.error(`模型 ${model} 重复`);
        return;
      }
      seen.add(model);
      const windows = row.offpeakWindows
        .map((window) => ({ start: window.start.trim(), end: window.end.trim() }))
        .filter((window) => window.start || window.end);
      if (windows.length > 8) {
        toast.error(`模型 ${model} 最多配置 8 个谷时时段`);
        return;
      }
      const seenWindows = new Set<string>();
      for (const window of windows) {
        if (!window.start || !window.end) {
          toast.error(`模型 ${model} 的每个谷时时段都必须同时填写开始和结束`);
          return;
        }
        if (window.start === window.end) {
          toast.error(`模型 ${model} 的谷时开始和结束不能相同`);
          return;
        }
        const key = `${window.start}-${window.end}`;
        if (seenWindows.has(key)) {
          toast.error(`模型 ${model} 的谷时时段 ${key} 重复`);
          return;
        }
        seenWindows.add(key);
      }
      const values = [
        num(row.input),
        num(row.output),
        num(row.cacheRead),
        num(row.cacheWrite),
        num(row.offpeakInput),
        num(row.offpeakOutput),
        num(row.offpeakCacheRead),
        num(row.offpeakCacheWrite),
      ];
      if (values.some((v) => !Number.isFinite(v) || v < 0)) {
        toast.error(`模型 ${model} 的价格必须是非负数`);
        return;
      }
      items.push({
        model,
        input_per_million: values[0],
        output_per_million: values[1],
        cache_read_per_million: values[2],
        cache_write_per_million: values[3],
        offpeak_windows: windows,
        offpeak_timezone: row.offpeakTimezone.trim() || "Asia/Shanghai",
        offpeak_input_per_million: values[4],
        offpeak_output_per_million: values[5],
        offpeak_cache_read_per_million: values[6],
        offpeak_cache_write_per_million: values[7],
      });
    }
    setSaving(true);
    try {
      setRows((await api.saveLLMPrices(items)).map(draftOf));
      toast.success("价格表已保存");
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  if (loading) {
    return (
      <div className="flex min-h-40 items-center justify-center">
        <Spinner />
      </div>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>模型价格</CardTitle>
        <CardDescription>
          每百万 token 的 USD 价格。上方四项是峰时价；谷时只需添加优惠时段并填写谷时单价，
          未命中任何谷时时段的时间都按峰时价计费。`default` 是未登记模型的兜底，全为 0
          的模型按“未计价”展示，不会伪造成本。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>模型</TableHead>
              <TableHead className="w-32 text-right">峰时输入 / M</TableHead>
              <TableHead className="w-32 text-right">峰时输出 / M</TableHead>
              <TableHead className="w-32 text-right">峰时 Cache 读 / M</TableHead>
              <TableHead className="w-32 text-right">峰时 Cache 写 / M</TableHead>
              <TableHead className="w-12" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                  还没有价格行。添加模型价格后，成本卡才会计算费用。
                </TableCell>
              </TableRow>
            ) : (
              rows.map((row, index) => (
                <React.Fragment key={row.id}>
                  <TableRow>
                    <TableCell>
                      <Input
                        value={row.model}
                        onChange={(e) => patch(index, "model", e.target.value)}
                        placeholder="例如 deepseek-chat 或 default"
                        className="font-mono"
                      />
                    </TableCell>
                    {(["input", "output", "cacheRead", "cacheWrite"] as const).map((key) => (
                      <TableCell key={key}>
                        <Input
                          inputMode="decimal"
                          value={row[key]}
                          onChange={(e) => patch(index, key, e.target.value)}
                          className="text-right font-mono"
                        />
                      </TableCell>
                    ))}
                    <TableCell>
                      <Button variant="ghost" size="icon-sm" onClick={() => removeRow(index)} aria-label="删除价格行">
                        <Trash2Icon />
                      </Button>
                    </TableCell>
                  </TableRow>
                  <TableRow className="hover:bg-transparent">
                    <TableCell colSpan={6} className="bg-muted/20 pt-0 pb-3">
                      <div className="grid gap-3 rounded-md border border-dashed p-3 md:grid-cols-[minmax(18rem,1.5fr)_minmax(9rem,1fr)_repeat(4,minmax(6rem,1fr))] md:items-start">
                        <div className="flex flex-col gap-2">
                          <span className="text-muted-foreground text-xs">谷时时段（可添加多个）</span>
                          {row.offpeakWindows.length === 0 ? (
                            <span className="text-muted-foreground text-xs">未设置：全天按上方峰时价计费</span>
                          ) : (
                            row.offpeakWindows.map((window) => (
                              <div key={window.id} className="flex items-end gap-1.5">
                                <div className="flex flex-col gap-1">
                                  <span className="text-[10px] text-muted-foreground">开始</span>
                                  <Input
                                    type="time"
                                    value={window.start}
                                    onChange={(e) => patchWindow(index, window.id, "start", e.target.value)}
                                    aria-label={`${row.model || "模型"} 谷时开始`}
                                    className="h-8 w-28 font-mono"
                                  />
                                </div>
                                <div className="flex flex-col gap-1">
                                  <span className="text-[10px] text-muted-foreground">结束</span>
                                  <Input
                                    type="time"
                                    value={window.end}
                                    onChange={(e) => patchWindow(index, window.id, "end", e.target.value)}
                                    aria-label={`${row.model || "模型"} 谷时结束`}
                                    className="h-8 w-28 font-mono"
                                  />
                                </div>
                                <Button
                                  variant="ghost"
                                  size="icon-sm"
                                  onClick={() => removeWindow(index, window.id)}
                                  aria-label="删除谷时时段"
                                >
                                  <Trash2Icon />
                                </Button>
                              </div>
                            ))
                          )}
                          <Button variant="outline" size="sm" className="w-fit" onClick={() => addWindow(index)}>
                            <PlusIcon /> 添加谷时时段
                          </Button>
                        </div>
                        <div className="flex flex-col gap-1">
                          <span className="text-muted-foreground text-xs">时区（默认北京时间）</span>
                          <Input
                            value={row.offpeakTimezone}
                            onChange={(e) => patch(index, "offpeakTimezone", e.target.value)}
                            placeholder="Asia/Shanghai"
                            className="h-8 font-mono"
                          />
                        </div>
                        {(
                          [
                            ["offpeakInput", "谷时输入 / M"],
                            ["offpeakOutput", "谷时输出 / M"],
                            ["offpeakCacheRead", "谷时 Cache 读 / M"],
                            ["offpeakCacheWrite", "谷时 Cache 写 / M"],
                          ] as const
                        ).map(([key, label]) => (
                          <div key={key} className="flex flex-col gap-1">
                            <span className="text-muted-foreground text-xs">{label}</span>
                            <Input
                              inputMode="decimal"
                              value={row[key]}
                              onChange={(e) => patch(index, key, e.target.value)}
                              className="h-8 text-right font-mono"
                            />
                          </div>
                        ))}
                        <p className="text-muted-foreground text-xs md:col-span-6">
                          可添加多个谷时时段（如 DeepSeek 两段优惠时间）；未命中任何谷时时段的时间按上方峰时价计费。
                          支持跨午夜（如 22:30–08:30）。
                        </p>
                      </div>
                    </TableCell>
                  </TableRow>
                </React.Fragment>
              ))
            )}
          </TableBody>
        </Table>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={addRow}>
            <PlusIcon /> 添加模型
          </Button>
          <Button size="sm" onClick={() => void save()} disabled={saving}>
            <SaveIcon /> {saving ? "保存中…" : "保存价格表"}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
