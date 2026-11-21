export type TaskReadState = "loading" | "ready" | "stale" | "error" | "notFound";

export function canonicalTaskID(value: string): string {
  if (!/^\d+$/.test(value)) return value;
  try {
    return BigInt(value).toString();
  } catch {
    return value;
  }
}

export function failedTaskRead(error: unknown, hasSnapshot: boolean): TaskReadState {
  if (error && typeof error === "object" && "status" in error && error.status === 404) return "notFound";
  return hasSnapshot ? "stale" : "error";
}
