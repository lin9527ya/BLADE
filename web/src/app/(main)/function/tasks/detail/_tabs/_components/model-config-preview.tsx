"use client";
import * as React from "react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { http } from "@/lib/api";
import type { TaskLLMResolution } from "@/lib/types";

type Preview = {
  expected: TaskLLMResolution;
  last_used?: { model: string; profile: string; at: string; status: string };
  skills: { name: string; state: string; text?: string }[];
  prompt: string;
  prompt_source: string;
  prompt_error?: string;
};
export function ModelConfigPreview({ taskId }: { taskId: string }) {
  const [open, setOpen] = React.useState(false),
    [role, setRole] = React.useState("mainagent"),
    [refresh, setRefresh] = React.useState(0),
    [data, setData] = React.useState<Preview>(),
    [error, setError] = React.useState("");
  React.useEffect(() => {
    setData(undefined);
    setError("");
    if (!open) return;
    const c = new AbortController();
    http<Preview>(`/tasks/${taskId}/llm/preview?role=${role}`, {
      signal: c.signal,
      cache: refresh ? "reload" : "no-cache",
    })
      .then((d) => {
        if (!c.signal.aborted) setData(d);
      })
      .catch((e) => {
        if (!c.signal.aborted) setError(e.message);
      });
    return () => c.abort();
  }, [taskId, role, open, refresh]);
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
            模型配置生效预览 · {open ? "收起" : "查看"}
          </button>
        </CardTitle>
      </CardHeader>
      {open && (
        <CardContent className="space-y-2 text-sm">
          <label>
            角色{" "}
            <select value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="mainagent">主 Agent</option>
              <option value="planner">Planner</option>
              <option value="worker">Worker</option>
            </select>
          </label>{" "}
          <button type="button" onClick={() => setRefresh((n) => n + 1)}>
            刷新
          </button>
          {error && <p role="alert">{error}</p>}
          {!data && !error && <p>正在读取…</p>}
          {data && (
            <>
              <p>
                按当前配置 · {data.expected.model || "未配置"} · {data.expected.reason || data.expected.source}
              </p>
              <p>
                最近实际调用 ·{" "}
                {data.last_used
                  ? `${data.last_used.model} · ${data.last_used.profile} · ${new Date(data.last_used.at).toLocaleString()} · ${data.last_used.status}`
                  : "未知（暂无记录）"}
              </p>
              <details>
                <summary>提示词预览 · {data.prompt_source}（示例变量）</summary>
                <pre className="max-h-72 overflow-auto whitespace-pre-wrap">{data.prompt_error || data.prompt}</pre>
              </details>
              <details>
                <summary>当前配置的技能索引 · {data.skills.length} 项</summary>
                <ul className="max-h-72 overflow-auto">
                  {data.skills.map((s) => (
                    <li key={s.name}>
                      {s.name} · {s.state === "absent" ? "未包含" : s.state === "name_only" ? "仅名称" : s.text}
                    </li>
                  ))}
                </ul>
              </details>
            </>
          )}
        </CardContent>
      )}
    </Card>
  );
}
