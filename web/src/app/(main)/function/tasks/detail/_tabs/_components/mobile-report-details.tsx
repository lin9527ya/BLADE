"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import { http } from "@/lib/api";
import type { DecompileAuditReport, MobileInspectionStage, WorkspaceFile } from "@/lib/types";

const stages: Record<string, string> = {
  android_manifest: "Android 清单",
  android_resources: "归档资源文件读取",
  network_config: "网络配置文件读取",
  source_output: "源码产出",
  split_base_manifest: "分包基础清单",
  info_plist: "Info.plist",
  provisioning: "签名描述文件",
  binary_metadata: "原生二进制元数据",
  miniapp_packages: "小程序包解析",
  miniapp_extraction: "小程序包展开",
  file_review: "报告文件检查",
};
const states: Record<string, string> = {
  complete: "完成",
  partial: "部分完成",
  not_present: "样本未包含",
  unavailable: "无法读取",
  limited: "达到上限",
  unknown: "未知",
  tool_missing: "工具不可用",
  not_checked: "未检查",
  not_produced: "未产生输出",
  available: "有输出",
};
export function MobileChecks({ checks }: { checks?: MobileInspectionStage[] }) {
  if (!checks?.length) return null;
  return (
    <ul className="space-y-1 text-xs">
      {checks.map((c) => (
        <li key={c.name}>
          {stages[c.name] ?? c.name} · {states[c.status] ?? c.status}
          {c.total > 0 && ` · ${c.checked}/${c.total}`}
          {c.total === 0 && c.checked > 0 && ` · ${c.checked} 个文件`}
          {c.note && ` · ${c.note}`}
        </li>
      ))}
    </ul>
  );
}

// Human review uses the workspace's authenticated original-file reader. The
// report and the underlying sample stay independently addressable.
export function MobileSourceReview({ taskId, report }: { taskId: string; report: DecompileAuditReport }) {
  const files = Array.from(
    new Set(
      [
        ...(report.endpoints ?? []),
        ...(report.risks ?? []),
        ...(report.sign_candidates ?? []),
        ...(report.secrets ?? []),
      ].map((r) => r.file),
    ),
  ).sort();
  const [file, setFile] = React.useState(""),
    [opened, setOpened] = React.useState(""),
    [source, setSource] = React.useState(""),
    [error, setError] = React.useState("");
  React.useEffect(() => {
    setSource("");
    setError("");
    if (!opened) return;
    const abort = new AbortController();
    const path = `tasks/${taskId}/${report.decompiled_dir}/${opened}`;
    http<WorkspaceFile>(`/workspace/read?${new URLSearchParams({ path, mobile_manual_review: "1" })}`, {
      signal: abort.signal,
      cache: "no-store",
    })
      .then((r) => {
        if (!abort.signal.aborted) {
          if (r.binary || r.too_large) setError("文件不适合文本预览");
          else setSource(r.content ?? "");
        }
      })
      .catch((e) => {
        if (!abort.signal.aborted) setError(e.message);
      });
    return () => abort.abort();
  }, [opened, taskId, report.decompiled_dir]);
  React.useEffect(() => {
    const hide = () => {
      if (document.hidden) {
        setOpened("");
        setSource("");
      }
    };
    document.addEventListener("visibilitychange", hide);
    return () => document.removeEventListener("visibilitychange", hide);
  }, []);
  if (!files.length || !report.run_id || report.run_id === "legacy") return null;
  return (
    <div className="space-y-1 rounded border p-2 text-xs">
      <label>
        手工核验原始文件{" "}
        <select
          value={file}
          onChange={(e) => {
            setFile(e.target.value);
            setOpened("");
            setSource("");
          }}
        >
          <option value="">选择来源文件</option>
          {files.map((f) => (
            <option key={f}>{f}</option>
          ))}
        </select>
      </label>
      <Button
        size="sm"
        variant="outline"
        disabled={!file}
        onClick={() => {
          setSource("");
          setOpened(opened ? "" : file);
        }}
      >
        {opened ? "关闭原文" : "查看原文"}
      </Button>
      {error && <p role="alert">{error}</p>}
      {opened && source && (
        <pre className="max-h-64 overflow-auto">
          {source
            .split(/\r?\n/)
            .map((line, i) => `${i + 1}  ${line}`)
            .join("\n")}
        </pre>
      )}
    </div>
  );
}
