export type AggregateOperation = { parameters: string; key: string; fingerprint?: string };
const STORAGE_KEY = "blade:aggregate-operation";
type OperationStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

function browserStorage(): OperationStorage | undefined {
  try { return typeof window === "undefined" ? undefined : window.sessionStorage; } catch { return undefined; }
}
function newKey(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  bytes[6] = (bytes[6] & 15) | 64; bytes[8] = (bytes[8] & 63) | 128;
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

// Storage is a best-effort convenience, not the authoritative receipt. It stores
// only a parameter digest and random operation key, never report/note contents.
export async function aggregateOperation(parameters: string, previous?: AggregateOperation, storage = browserStorage()): Promise<AggregateOperation> {
  if (previous?.parameters === parameters) return previous;
  let fingerprint: string | undefined;
  try {
    const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(parameters));
    fingerprint = Array.from(new Uint8Array(hash), (value) => value.toString(16).padStart(2, "0")).join("");
  } catch { /* Non-secure contexts may lack subtle; same-component retries still work. */ }
  let key: string | undefined;
  if (fingerprint) {
    try {
      const saved = JSON.parse(storage?.getItem(STORAGE_KEY) ?? "null");
      if (saved?.fingerprint === fingerprint && typeof saved.key === "string" && /^[a-f0-9]{32}$/.test(saved.key)) key = saved.key;
    } catch { /* unavailable/corrupt storage must not block the form */ }
  }
  const operation = { parameters, key: key ?? newKey(), fingerprint };
  if (fingerprint) { try { storage?.setItem(STORAGE_KEY, JSON.stringify({ fingerprint, key: operation.key })); } catch {} }
  return operation;
}

export function completeAggregateOperation(operation: AggregateOperation, storage = browserStorage()) {
  try {
    const saved = JSON.parse(storage?.getItem(STORAGE_KEY) ?? "null");
    if (saved?.key === operation.key) storage?.removeItem(STORAGE_KEY);
  } catch {}
}
