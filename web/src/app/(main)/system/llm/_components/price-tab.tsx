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

type Draft = {
  // Stable React key: model edits must not remount the row/input.
  id: string;
  model: string;
  input: string;
  output: string;
  cacheRead: string;
  cacheWrite: string;
  offpeakStart: string;
  offpeakEnd: string;
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
    offpeakStart: p.offpeak_start ?? "",
    offpeakEnd: p.offpeak_end ?? "",
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
        offpeakStart: "",
        offpeakEnd: "",
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
      if (Boolean(row.offpeakStart) !== Boolean(row.offpeakEnd)) {
        toast.error(`模型 ${model} 的谷时开始/结束必须同时填写或同时留空`);
        return;
      }
      if (row.offpeakStart && row.offpeakStart === row.offpeakEnd) {
        toast.error(`模型 ${model} 的谷时开始和结束不能相同`);
        return;
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
        offpeak_start: row.offpeakStart,
        offpeak_end: row.offpeakEnd,
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
          每百万 token 的 USD 价格。精确匹配模型；`default` 是未登记模型的兜底。价格全为 0
          的模型按“未计价”展示，不会伪造成本。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>模型</TableHead>
              <TableHead className="w-32 text-right">输入 / M</TableHead>
              <TableHead className="w-32 text-right">输出 / M</TableHead>
              <TableHead className="w-32 text-right">Cache 读 / M</TableHead>
              <TableHead className="w-32 text-right">Cache 写 / M</TableHead>
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
                      <div className="grid gap-2 rounded-md border border-dashed p-3 md:grid-cols-[auto_auto_minmax(9rem,1fr)_repeat(4,minmax(6rem,1fr))] md:items-end">
                        <div className="flex flex-col gap-1">
                          <span className="text-muted-foreground text-xs">谷时开始</span>
                          <Input
                            type="time"
                            value={row.offpeakStart}
                            onChange={(e) => patch(index, "offpeakStart", e.target.value)}
                            className="h-8 font-mono"
                          />
                        </div>
                        <div className="flex flex-col gap-1">
                          <span className="text-muted-foreground text-xs">谷时结束</span>
                          <Input
                            type="time"
                            value={row.offpeakEnd}
                            onChange={(e) => patch(index, "offpeakEnd", e.target.value)}
                            className="h-8 font-mono"
                          />
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
                        <p className="text-muted-foreground text-xs md:col-span-7">
                          开始/结束留空 = 全天按上方峰时价。支持跨午夜（如 22:30–08:30）；窗口外按峰时价计费。
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
