"use client";

import type * as React from "react";

import { Label } from "@/components/ui/label";

// Field：拦截工作台共用的紧凑字段（小号大写标签 + 控件），与设置页的 Field 刻意不同——
// 这里的表单更密（规则编辑器一屏十几项）。judge 分区与 rules 分区共用同一份定义。

export function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">{label}</Label>
      {children}
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}
