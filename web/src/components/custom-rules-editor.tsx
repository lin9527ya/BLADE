"use client";

import * as React from "react";

import { PlusIcon, Trash2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import type { CustomRule } from "@/lib/types";

/**
 * CustomRulesEditor 编辑「自定义禁止条目」——探索策略的逃生舱：词表覆盖不到时写一条。
 *
 * 语义（与后端 policy.Validate 一致，写不进去后端会 400 并说明原因）：
 *  · 关键词：ASCII 走词边界（`.` 视为词内字符）、中文走子串；与词表同款否定语境豁免
 *    （"检查是否有 X"这类合规方向放行但留痕）。
 *  · 正则：大小写不敏感地按原文匹配，不套否定豁免；必须能编译。
 *  · 生效层「仅命令」：意图措辞不判，只编译进命令门——适合"说说没关系、做出来才有事"。
 */
export function CustomRulesEditor({
  rules,
  onChange,
  disabled = false,
  limit = 16,
  patternLimit = 200,
}: {
  rules: CustomRule[];
  onChange: (next: CustomRule[]) => void;
  disabled?: boolean;
  limit?: number;
  patternLimit?: number;
}) {
  const set = (i: number, patch: Partial<CustomRule>) =>
    onChange(rules.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));

  return (
    <div className="flex flex-col gap-2">
      {rules.map((r, i) => (
        <div key={i} className="flex flex-wrap items-center gap-1.5">
          <Input
            className="h-7 w-32 text-sm"
            placeholder="显示名（可选）"
            value={r.label ?? ""}
            disabled={disabled}
            onChange={(e) => set(i, { label: e.target.value })}
          />
          <Input
            className="h-7 min-w-40 flex-1 font-mono text-sm"
            placeholder={r.regex ? "正则，如 /internal/(?:admin|debug)" : "关键词，如 wp-admin / 脱库"}
            value={r.pattern}
            maxLength={patternLimit}
            disabled={disabled}
            onChange={(e) => set(i, { pattern: e.target.value })}
          />
          <NativeSelect
            size="sm"
            value={r.regex ? "regex" : "keyword"}
            disabled={disabled}
            onChange={(e) => set(i, { regex: e.target.value === "regex" })}
          >
            <NativeSelectOption value="keyword">关键词</NativeSelectOption>
            <NativeSelectOption value="regex">正则</NativeSelectOption>
          </NativeSelect>
          <NativeSelect
            size="sm"
            value={r.layer === "command" ? "command" : "both"}
            disabled={disabled}
            onChange={(e) => set(i, { layer: e.target.value as CustomRule["layer"] })}
          >
            <NativeSelectOption value="both">意图+命令</NativeSelectOption>
            <NativeSelectOption value="command">仅命令</NativeSelectOption>
          </NativeSelect>
          <Button
            size="sm"
            variant="ghost"
            className="h-6 shrink-0 px-1.5"
            disabled={disabled}
            onClick={() => onChange(rules.filter((_, idx) => idx !== i))}
          >
            <Trash2Icon className="size-3.5 text-red-500" />
          </Button>
        </div>
      ))}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={disabled || rules.length >= limit}
          onClick={() => onChange([...rules, { pattern: "", layer: "both" }])}
        >
          <PlusIcon className="size-3.5" /> 添加一条
        </Button>
        <span className="text-muted-foreground text-xs">
          {rules.length}/{limit} 条（空条目不会保存；与词表条目一起受命令门规则总数上限约束）
        </span>
      </div>
    </div>
  );
}
