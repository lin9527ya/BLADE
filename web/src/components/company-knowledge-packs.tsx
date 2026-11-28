"use client";
import * as React from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { http } from "@/lib/api";

type Pack = {
  id: number;
  key: string;
  name: string;
  version: string;
  status: string;
  license: string;
  sources: string[];
  entry_count: number;
  reviewed_by?: string;
};
type Entry = { id: string; title: string; body: string; source: string };
const names: Record<string, string> = { draft: "待审核", approved: "已审核", disabled: "已停用" };
export function CompanyKnowledgePacks({ company }: { company: number }) {
  const base = `/companies/${company}/knowledge-packs`;
  const [canReview, setCanReview] = React.useState(false);
  const [packs, setPacks] = React.useState<Pack[]>([]),
    [selected, setSelected] = React.useState(0),
    [search, setSearch] = React.useState(""),
    [query, setQuery] = React.useState(""),
    [cursors, setCursors] = React.useState([""]);
  const [result, setResult] = React.useState<{ entries: Entry[]; next: string }>(),
    [message, setMessage] = React.useState(""),
    [busy, setBusy] = React.useState(false),
    [refresh, setRefresh] = React.useState(0);
  React.useEffect(() => {
    const c = new AbortController();
    http<{ packs: Pack[]; can_review: boolean }>(base, { signal: c.signal, cache: refresh ? "reload" : "no-cache" })
      .then((r) => {
        if (!c.signal.aborted) {
          setPacks(r.packs ?? []);
          setCanReview(!!r.can_review);
        }
      })
      .catch((e) => {
        if (!c.signal.aborted) setMessage(e.message);
      });
    return () => c.abort();
  }, [base, refresh]);
  const after = cursors[cursors.length - 1];
  React.useEffect(() => {
    setResult(undefined);
    if (!selected) return;
    const c = new AbortController();
    http<{ entries: Entry[]; next: string }>(
      `${base}/${selected}/entries?${new URLSearchParams({ q: query, after })}`,
      { signal: c.signal, cache: refresh ? "reload" : "no-cache" },
    )
      .then((r) => {
        if (!c.signal.aborted) setResult(r);
      })
      .catch((e) => {
        if (!c.signal.aborted) setMessage(e.message);
      });
    return () => c.abort();
  }, [selected, base, query, after, refresh]);
  async function change(path: string, body: BodyInit, zip = false) {
    setBusy(true);
    setMessage("");
    try {
      await http(path, {
        method: "POST",
        body,
        headers: { "Content-Type": zip ? "application/zip" : "application/json" },
      });
      setRefresh((n) => n + 1);
      setMessage(zip ? "已导入，可展开检查条目。" : "审核状态已更新。");
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="space-y-2 border-b pb-3">
      <h3 className="font-medium">企业知识包</h3>
      {canReview && (
        <label className="text-sm">
          导入 ZIP
          <Input
            type="file"
            disabled={busy}
            accept=".zip"
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) void change(base, f, true);
              e.target.value = "";
            }}
          />
        </label>
      )}
      <details className="text-xs">
        <summary>知识包格式</summary>
        <p>
          ZIP 根目录放 manifest.json 和 entries.jsonl。清单填写 id、name、version、license、sources（数组）、entry_count
          和条目文件的 SHA-256（sha256）。每行条目包含 id、title、body、source。
        </p>
      </details>
      {message && (
        <p role="status" className="text-xs">
          {message}
        </p>
      )}
      {packs.map((p) => (
        <div className="space-y-1 rounded border p-2 text-sm" key={p.id}>
          <button
            type="button"
            className="font-medium"
            aria-expanded={selected === p.id}
            onClick={() => {
              setSelected(selected === p.id ? 0 : p.id);
              setQuery("");
              setSearch("");
              setCursors([""]);
            }}
          >
            {p.name} · {p.version} · {names[p.status]} · {p.entry_count} 条
          </button>
          {selected === p.id && (
            <div>
              <p className="text-xs">
                {p.license} · {p.sources.join("、")} {p.reviewed_by && `· 审核人 ${p.reviewed_by}`}
              </p>
              <div className="my-2 flex gap-2">
                <Input value={search} placeholder="检索本包标题或正文" onChange={(e) => setSearch(e.target.value)} />
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setCursors([""]);
                    setQuery(search);
                  }}
                >
                  检索
                </Button>
              </div>
              {result?.entries.map((e) => (
                <details key={e.id}>
                  <summary>{e.title}</summary>
                  <p className="whitespace-pre-wrap break-words">{e.body}</p>
                  <p className="text-muted-foreground text-xs">来源 · {e.source}</p>
                </details>
              ))}
              {result?.entries.length === 0 && <p>没有匹配的条目</p>}
              <div className="mt-2 flex gap-2">
                {cursors.length > 1 && (
                  <Button size="sm" variant="ghost" onClick={() => setCursors(cursors.slice(0, -1))}>
                    上一页
                  </Button>
                )}
                {result?.next && (
                  <Button size="sm" variant="ghost" onClick={() => setCursors([...cursors, result.next])}>
                    下一页
                  </Button>
                )}
                {canReview && p.status !== "approved" && (
                  <Button
                    size="sm"
                    disabled={busy}
                    onClick={() => change(`${base}/${p.id}/review`, JSON.stringify({ status: "approved" }))}
                  >
                    审核通过
                  </Button>
                )}
                {canReview && p.status !== "disabled" && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy}
                    onClick={() => change(`${base}/${p.id}/review`, JSON.stringify({ status: "disabled" }))}
                  >
                    停用版本
                  </Button>
                )}
              </div>
            </div>
          )}
        </div>
      ))}
    </section>
  );
}
