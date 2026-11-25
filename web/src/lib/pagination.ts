export function totalPagesFor(total: number, pageSize: number): number {
  if (!Number.isFinite(total) || !Number.isFinite(pageSize) || pageSize <= 0 || total <= 0) return 1;
  return Math.max(1, Math.ceil(total / pageSize));
}

export function clampPage(page: number, total: number, pageSize: number): number {
  const totalPages = totalPagesFor(total, pageSize);
  if (!Number.isFinite(page) || page < 1) return 1;
  return Math.min(Math.floor(page), totalPages);
}

export function paginateSlice<T>(items: readonly T[], page: number, pageSize: number): T[] {
  if (pageSize <= 0) return [...items];
  const safePage = clampPage(page, items.length, pageSize);
  const start = (safePage - 1) * pageSize;
  return items.slice(start, start + pageSize);
}

/** 解析用户输入的跳转页码（正整数；超出总页数自动夹紧到末页，空值或非正整数返回 null）。 */
export function parseJumpPage(raw: string, totalPages: number): number | null {
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const parsed = Number.parseInt(trimmed, 10);
  if (!Number.isFinite(parsed) || parsed < 1 || !Number.isFinite(totalPages) || totalPages < 1) return null;
  return Math.min(parsed, Math.floor(totalPages));
}
