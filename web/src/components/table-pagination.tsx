"use client";

import { useEffect, useRef, useState } from "react";

import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Pagination, PaginationContent, PaginationEllipsis, PaginationItem } from "@/components/ui/pagination";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { clampPage, paginateSlice, parseJumpPage, totalPagesFor } from "@/lib/pagination";
import { cn } from "@/lib/utils";

// 全站唯一的翻页组件，两种形态：
//
//   variant="full"（默认）—— 表格页脚：每页条数 + 范围 + 页码按钮 + 前后翻页。
//   variant="compact"      —— 工具条里的紧凑形态：范围 + 前后翻页 + 「n / N」，**没有
//                             页码按钮**；每页条数只有调用方传了 onPageSizeChange 才渲染。
//
// **page 一律 1-based**。此前手写的翻页里混着 0-based（commands / llm-records /
// assets / traffic），调用方按 0-based 传就会差一页——收敛时由调用方显式换算
// （`page={page + 1}` / `onPageChange={(p) => setPage(p - 1)}`），不在这里加第二套基准。
interface TablePaginationProps {
  page: number; // 1-based
  pageSize: number;
  // total 未知（接口不给总数，如 ScopeSentry 的列表）时传 undefined，改用 hasNext。
  total?: number;
  hasNext?: boolean;
  onPageChange: (p: number) => void;
  // 不传 = 不渲染「条/页」选择器（紧凑形态与固定页长的场景）。
  onPageSizeChange?: (s: number) => void;
  pageSizeOptions?: number[];
  variant?: "full" | "compact" | "inline";
  // 正在加载时禁用翻页，避免连点翻两页（有些调用方原先自己带这个守卫）。
  busy?: boolean;
  // 是否显示「跳至 [ ] 页 [跳转]」输入框（inline 形态默认开启）。
  showJump?: boolean;
  // 已知总页数且只有 1 页时隐藏整条分页栏。
  hideWhenSinglePage?: boolean;
  // 窄卡片场景下不展开页码数字按钮，只保留前后翻页 + 页码比 + 跳转输入。
  compactNumbers?: boolean;
  summaryLabel?: string;
  className?: string;
}

function PageJumpControl({
  totalPages,
  busy,
  onJump,
}: {
  totalPages: number;
  busy: boolean;
  onJump: (p: number) => void;
}) {
  const [draft, setDraft] = useState("");
  const target = parseJumpPage(draft, totalPages);
  const submit = () => {
    if (target === null) return;
    onJump(target);
    setDraft("");
  };
  return (
    <div className="flex items-center gap-1 text-muted-foreground text-xs">
      <span>跳至</span>
      <Input
        type="text"
        inputMode="numeric"
        aria-label="跳转页码"
        placeholder="页码"
        className="h-7 w-13 px-1.5 text-center text-xs tabular-nums"
        disabled={busy}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            submit();
          }
        }}
      />
      <span>页</span>
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="h-7 px-2 text-xs"
        disabled={busy || target === null}
        onClick={submit}
      >
        跳转
      </Button>
    </div>
  );
}

/** 前端本地列表分页 Hook：随条目变化自动夹紧页码，并在 resetKey 切换时回到第 1 页。 */
export function useClientPagination<T>(items: readonly T[], pageSize: number, resetKey?: string | number) {
  const [pageState, setPageState] = useState<{ key: string | number | undefined; page: number }>({
    key: resetKey,
    page: 1,
  });
  const rawPage = pageState.key === resetKey ? pageState.page : 1;
  const total = items.length;
  const page = clampPage(rawPage, total, pageSize);
  const pageItems = paginateSlice(items, page, pageSize);
  const setPage = (next: number) => setPageState({ key: resetKey, page: next });
  return {
    page,
    total,
    setPage,
    pageItems,
    paginationProps: {
      page,
      pageSize,
      total,
      onPageChange: setPage,
      variant: "inline" as const,
      hideWhenSinglePage: true,
      showJump: true,
    },
  };
}

// pageWindows returns the sequence of page numbers / ellipsis to render.
function pageWindows(page: number, total: number): (number | "...")[] {
  if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1);
  const out: (number | "...")[] = [1];
  if (page > 3) out.push("...");
  const lo = Math.max(2, page - 1);
  const hi = Math.min(total - 1, page + 1);
  for (let i = lo; i <= hi; i++) out.push(i);
  if (page < total - 2) out.push("...");
  out.push(total);
  return out;
}

function PageWindowButtons({
  safePage,
  totalPages,
  canPrev,
  canNext,
  busy,
  onPageChange,
}: {
  safePage: number;
  totalPages: number;
  canPrev: boolean;
  canNext: boolean;
  busy: boolean;
  onPageChange: (p: number) => void;
}) {
  return (
    <Pagination className="mx-0 w-auto justify-end">
      <PaginationContent>
        <PaginationItem>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={!canPrev}
            onClick={() => onPageChange(safePage - 1)}
            aria-label="上一页"
          >
            <ChevronLeftIcon className="size-4" />
          </Button>
        </PaginationItem>
        {pageWindows(safePage, totalPages).map((p, i) =>
          p === "..." ? (
            <PaginationItem key={`el-${i < 3 ? "start" : "end"}`}>
              <PaginationEllipsis />
            </PaginationItem>
          ) : (
            <PaginationItem key={p}>
              <Button
                type="button"
                variant={p === safePage ? "outline" : "ghost"}
                size="icon-sm"
                disabled={busy}
                onClick={() => onPageChange(p)}
                aria-current={p === safePage ? "page" : undefined}
              >
                {p}
              </Button>
            </PaginationItem>
          ),
        )}
        <PaginationItem>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={!canNext}
            onClick={() => onPageChange(safePage + 1)}
            aria-label="下一页"
          >
            <ChevronRightIcon className="size-4" />
          </Button>
        </PaginationItem>
      </PaginationContent>
    </Pagination>
  );
}

export function TablePagination({
  page,
  pageSize,
  total,
  hasNext,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = [10, 20, 50],
  variant = "full",
  busy = false,
  showJump,
  hideWhenSinglePage = false,
  compactNumbers = false,
  summaryLabel,
  className,
}: TablePaginationProps) {
  const known = typeof total === "number";
  const totalPages = known ? totalPagesFor(total as number, pageSize) : Math.max(1, page);
  const safePage = known ? clampPage(page, total as number, pageSize) : Math.max(1, page);
  const from = !known ? (safePage - 1) * pageSize + 1 : total === 0 ? 0 : (safePage - 1) * pageSize + 1;
  const to = known ? Math.min(safePage * pageSize, total as number) : safePage * pageSize;
  const canPrev = !busy && safePage > 1;
  const canNext = !busy && (known ? safePage < totalPages : !!hasNext);
  const jumpEnabled = (showJump ?? variant === "inline") && totalPages > 1;
  const onPageChangeRef = useRef(onPageChange);
  onPageChangeRef.current = onPageChange;

  useEffect(() => {
    if (page !== safePage) onPageChangeRef.current(safePage);
  }, [page, safePage]);

  if (hideWhenSinglePage && known && totalPages <= 1) {
    return null;
  }

  if (variant === "inline") {
    return (
      <div
        className={cn(
          "flex flex-wrap items-center justify-between gap-2 border-t pt-2.5 text-muted-foreground text-xs",
          className,
        )}
      >
        <span className="tabular-nums">
          {summaryLabel ??
            (known ? `每页 ${pageSize} 条 · 第 ${safePage} / ${totalPages} 页 · 共 ${total} 条` : `第 ${safePage} 页`)}
        </span>
        {totalPages > 1 && (
          <div className="flex flex-wrap items-center gap-2">
            {compactNumbers ? (
              <div className="flex items-center gap-1">
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="size-7"
                  disabled={!canPrev}
                  onClick={() => onPageChange(safePage - 1)}
                  aria-label="上一页"
                >
                  <ChevronLeftIcon className="size-3.5" />
                </Button>
                <span className="px-1 tabular-nums">{known ? `${safePage} / ${totalPages}` : String(safePage)}</span>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="size-7"
                  disabled={!canNext}
                  onClick={() => onPageChange(safePage + 1)}
                  aria-label="下一页"
                >
                  <ChevronRightIcon className="size-3.5" />
                </Button>
              </div>
            ) : (
              <PageWindowButtons
                safePage={safePage}
                totalPages={totalPages}
                canPrev={canPrev}
                canNext={canNext}
                busy={busy}
                onPageChange={onPageChange}
              />
            )}
            {jumpEnabled && <PageJumpControl totalPages={totalPages} busy={busy} onJump={onPageChange} />}
          </div>
        )}
      </div>
    );
  }

  if (variant === "compact") {
    return (
      <div className={cn("ml-auto flex flex-wrap items-center gap-2 text-muted-foreground text-xs", className)}>
        {onPageSizeChange && (
          <Select value={String(pageSize)} onValueChange={(v) => onPageSizeChange(Number(v))}>
            <SelectTrigger size="sm" className="h-7 w-20">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {pageSizeOptions.map((s) => (
                <SelectItem key={s} value={String(s)}>
                  {s} / 页
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        <span className="tabular-nums">{known ? `${from}–${to} / ${total}` : `第 ${safePage} 页`}</span>
        <Button
          variant="outline"
          size="icon"
          className="size-8"
          disabled={!canPrev}
          onClick={() => onPageChange(safePage - 1)}
          aria-label="上一页"
        >
          <ChevronLeftIcon />
        </Button>
        <span className="tabular-nums">{known ? `${safePage} / ${totalPages}` : String(safePage)}</span>
        <Button
          variant="outline"
          size="icon"
          className="size-8"
          disabled={!canNext}
          onClick={() => onPageChange(safePage + 1)}
          aria-label="下一页"
        >
          <ChevronRightIcon />
        </Button>
        {jumpEnabled && <PageJumpControl totalPages={totalPages} busy={busy} onJump={onPageChange} />}
      </div>
    );
  }

  return (
    <div className={cn("flex flex-wrap items-center justify-between gap-2 border-t px-4 py-2 lg:px-6", className)}>
      <div className="flex items-center gap-2 text-muted-foreground text-xs">
        {onPageSizeChange && (
          <>
            <Select value={String(pageSize)} onValueChange={(v) => onPageSizeChange(Number(v))}>
              <SelectTrigger size="sm" className="h-7 w-16">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {pageSizeOptions.map((s) => (
                  <SelectItem key={s} value={String(s)}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span>条/页</span>
          </>
        )}
        {!known ? (
          <span className="tabular-nums">第 {safePage} 页</span>
        ) : total === 0 ? (
          <span>共 0 条</span>
        ) : (
          <span className="tabular-nums">
            {from}–{to} / 共 {total} 条
          </span>
        )}
      </div>

      {totalPages > 1 && (
        <div className="flex flex-wrap items-center gap-2">
          <PageWindowButtons
            safePage={safePage}
            totalPages={totalPages}
            canPrev={canPrev}
            canNext={canNext}
            busy={busy}
            onPageChange={onPageChange}
          />
          {jumpEnabled && <PageJumpControl totalPages={totalPages} busy={busy} onJump={onPageChange} />}
        </div>
      )}
    </div>
  );
}
