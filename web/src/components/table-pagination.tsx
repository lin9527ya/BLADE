"use client";

import { useEffect, useRef } from "react";

import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Pagination, PaginationContent, PaginationEllipsis, PaginationItem } from "@/components/ui/pagination";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

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
  variant?: "full" | "compact";
  // 正在加载时禁用翻页，避免连点翻两页（有些调用方原先自己带这个守卫）。
  busy?: boolean;
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
}: TablePaginationProps) {
  const known = typeof total === "number";
  const totalPages = known ? Math.max(1, Math.ceil((total as number) / pageSize)) : Math.max(1, page);
  const safePage = known ? Math.min(Math.max(1, page), totalPages) : Math.max(1, page);
  const from = !known ? (safePage - 1) * pageSize + 1 : total === 0 ? 0 : (safePage - 1) * pageSize + 1;
  const to = known ? Math.min(safePage * pageSize, total as number) : safePage * pageSize;
  const canPrev = !busy && safePage > 1;
  const canNext = !busy && (known ? safePage < totalPages : !!hasNext);
  const onPageChangeRef = useRef(onPageChange);
  onPageChangeRef.current = onPageChange;

  useEffect(() => {
    if (page !== safePage) onPageChangeRef.current(safePage);
  }, [page, safePage]);

  if (variant === "compact") {
    return (
      <div className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
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
      </div>
    );
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-t px-4 py-2 lg:px-6">
      <div className="text-muted-foreground flex items-center gap-2 text-xs">
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
        <Pagination className="mx-0 w-auto justify-end">
          <PaginationContent>
            <PaginationItem>
              <Button
                variant="ghost"
                size="icon-sm"
                disabled={safePage === 1}
                onClick={() => onPageChange(safePage - 1)}
                aria-label="上一页"
              >
                <ChevronLeftIcon className="size-4" />
              </Button>
            </PaginationItem>
            {pageWindows(safePage, totalPages).map((p, i) =>
              p === "..." ? (
                <PaginationItem key={`el-${i}`}>
                  <PaginationEllipsis />
                </PaginationItem>
              ) : (
                <PaginationItem key={p}>
                  <Button
                    variant={p === safePage ? "outline" : "ghost"}
                    size="icon-sm"
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
                variant="ghost"
                size="icon-sm"
                disabled={safePage === totalPages}
                onClick={() => onPageChange(safePage + 1)}
                aria-label="下一页"
              >
                <ChevronRightIcon className="size-4" />
              </Button>
            </PaginationItem>
          </PaginationContent>
        </Pagination>
      )}
    </div>
  );
}
