"use client";

// 输入框历史：像 shell 一样用 ↑/↓ 翻已发送过的消息。
//
// 存 localStorage 而不是内存：刷新页面、切换会话后仍能翻到，符合"历史命令"的直觉。
// 每个 composer 用独立的 scope（任务/会话/意图），互不串台；内容可能含目标信息，
// 因此只留在本机浏览器，不上传、不落服务端。

const PREFIX = "blade.input-history.v1:";
const MAX_ENTRIES = 50;
const MAX_CHARS = 20_000;

function storage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null; // 隐私模式/禁用存储：静默退化为无历史，不影响发送
  }
}

function normalize(text: string): string {
  return text.trim();
}

/** loadInputHistory 返回从旧到新的历史条目；坏数据/空 scope 一律当没有。 */
export function loadInputHistory(scope: string): string[] {
  const s = storage();
  if (!s || !scope) return [];
  try {
    const raw = s.getItem(PREFIX + scope);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((x): x is string => typeof x === "string" && normalize(x) !== "");
  } catch {
    return [];
  }
}

/** rememberInputHistory 记录一条成功发出的消息；连续重复只留一条。 */
export function rememberInputHistory(scope: string, text: string): void {
  const value = normalize(text);
  const s = storage();
  if (!s || !scope || !value) return;
  const list = loadInputHistory(scope).filter((x) => x !== value);
  list.push(value);
  while (list.length > MAX_ENTRIES) list.shift();
  // 长消息（贴日志/报告）会迅速吃满配额；按总字符数从最旧的开始丢。
  let total = list.reduce((n, x) => n + x.length, 0);
  while (total > MAX_CHARS && list.length > 1) total -= list.shift()!.length;
  try {
    s.setItem(PREFIX + scope, JSON.stringify(list));
  } catch {
    // 配额满/被禁用：丢掉历史也不影响正常发送。
  }
}
