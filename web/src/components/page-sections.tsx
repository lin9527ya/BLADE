"use client";

import type * as React from "react";

import { ChevronRightIcon } from "lucide-react";

import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";

// 分区式页面的共用骨架（照 CyberStrikeAI 的系统设置界面）：左侧分区导航 + 单分区内容面板。
// 内容面板刻意不用卡片：层级靠「分区标题 h2 → 子块标题 h3（下方细线）→ 字段标签/说明小字」
// 的排版实现，避免十几张卡片边框互相抢注意力。
//
// 用法（`/system/settings` 与 `/system/intercept` 都这么用）：页面持有 `active` 状态 +
// 一份 `SECTIONS` 常量，导航只负责切换，内容自己按 id 渲染。
//
// 保存语义不进这里：单字段行是「输入框 + 保存」inline；多字段子块把主操作放子块标题行右侧。

export interface NavSection {
  readonly id: string;
  readonly label: string;
  /** badge：非零时在标签右侧显示一个计数（如"审批记录 3"）。 */
  readonly badge?: number;
}

/** SectionNav：窄屏是顶部粘性 chips 条，lg+ 是左侧固定栏（sticky，长分区滚动时导航不丢）。 */
export function SectionNav({
  sections,
  active,
  onChange,
  ariaLabel,
}: {
  sections: readonly NavSection[];
  active: string;
  onChange: (id: string) => void;
  ariaLabel: string;
}) {
  return (
    <nav
      aria-label={ariaLabel}
      className="bg-background/95 sticky top-0 z-10 -mx-4 flex gap-1 overflow-x-auto px-4 py-2 backdrop-blur md:-mx-6 md:px-6 lg:mx-0 lg:w-44 lg:shrink-0 lg:flex-col lg:gap-0.5 lg:self-start lg:overflow-visible lg:bg-transparent lg:px-0 lg:py-0 lg:backdrop-blur-none"
    >
      {sections.map((sec) => (
        <button
          key={sec.id}
          type="button"
          aria-current={active === sec.id ? "true" : undefined}
          onClick={() => onChange(sec.id)}
          className={cn(
            "shrink-0 rounded-md px-3 py-1.5 text-left text-sm whitespace-nowrap transition-colors",
            "lg:w-full lg:rounded-l-none lg:border-l-2 lg:py-2",
            active === sec.id
              ? "bg-muted text-foreground font-medium lg:border-primary"
              : "text-muted-foreground hover:bg-muted/60 hover:text-foreground lg:border-transparent",
          )}
        >
          {sec.label}
          {sec.badge ? (
            <span className="bg-amber-500 text-white ml-1.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] font-semibold tabular-nums">
              {sec.badge > 99 ? "99+" : sec.badge}
            </span>
          ) : null}
        </button>
      ))}
    </nav>
  );
}

/** PageShell：两栏骨架 + 内容列（页标题由调用方放进内容列顶部）。 */
export function PageShell({ nav, children }: { nav: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-6 lg:flex-row lg:items-start lg:gap-8">
      {nav}
      <div className="flex min-w-0 flex-1 flex-col gap-10">{children}</div>
    </div>
  );
}

/** Section：一个分区 = 分区标题（h2）+ 一句生效时机/说明 + 若干子块。 */
export function Section({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-8">
      <div className="flex flex-col gap-1">
        <h2 className="text-xl font-semibold tracking-tight">{title}</h2>
        {hint && <p className="text-muted-foreground text-sm leading-relaxed">{hint}</p>}
      </div>
      {children}
    </section>
  );
}

/** SubBlock：子块标题（h3 + 下方细线），标题行右侧放就地动作（保存 / 测试连接等）。 */
export function SubBlock({
  title,
  icon,
  actions,
  children,
}: {
  title: string;
  icon?: React.ReactNode;
  actions?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2 border-b pb-2">
        {icon}
        <h3 className="text-base font-medium">{title}</h3>
        {actions && <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  );
}

/** Field：标签在上、控件、说明小字在下（CS 的 .form-hint 同款）。 */
export function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: React.ReactNode;
  htmlFor?: string;
  hint?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={htmlFor} className="text-sm font-normal text-muted-foreground">
        {label}
      </Label>
      {children}
      {hint && <p className="text-muted-foreground text-xs leading-relaxed">{hint}</p>}
    </div>
  );
}

/** SwitchRow：开关行——标签在左、开关在右，说明小字在下方。 */
export function SwitchRow({
  id,
  label,
  hint,
  checked,
  disabled,
  onChange,
  describedBy,
}: {
  id: string;
  label: React.ReactNode;
  hint?: React.ReactNode;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  describedBy?: string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-4">
        <Label htmlFor={id} className="text-sm font-normal text-muted-foreground">
          {label}
        </Label>
        <Switch
          id={id}
          checked={checked}
          disabled={disabled}
          onCheckedChange={onChange}
          aria-describedby={describedBy}
        />
      </div>
      {hint && (
        <p id={describedBy} className="text-muted-foreground text-xs leading-relaxed">
          {hint}
        </p>
      )}
    </div>
  );
}

/** Advanced：约定「默认可见 = 常用，折叠 = 高级」（CS 同款），与任务页「高级设置」同一交互。 */
export function Advanced({ title = "高级设置", children }: { title?: string; children: React.ReactNode }) {
  return (
    <Collapsible>
      <CollapsibleTrigger className="group text-muted-foreground hover:text-foreground flex items-center gap-1.5 text-xs font-medium">
        <ChevronRightIcon className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
        {title}
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-4 flex flex-col gap-4">{children}</CollapsibleContent>
    </Collapsible>
  );
}
