"use client";

import * as React from "react";

import { FileUpIcon, LoaderIcon, Trash2Icon, UploadIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { ADImportFile, ADImportPreview, ADImportResult } from "@/lib/types";

// 枚举结果导入：把**离线枚举**的 JSON 落进这个任务的探索图（AD 的 SharpHound / 云上的
// aws-cli、kubectl 输出）。两种来源共用同一套纪律与同一份界面——它们进的是同一张图。
//
// 与资产 CSV 导入同规矩：**预览作废**——文件清单一变，上一份预览就清掉，
// 「拿旧预览配新文件」点不下应用。枚举结果是分文件的（users/groups/… 或 IAM/K8s 各一份），
// 所以这里一次收多个：跨文件的主体引用（组、会话里的账号、绑定指向的角色）在同一份计划里
// 才解析得出来。

type ImportSource = "ad" | "cloud";

const SOURCE: Record<
  ImportSource,
  {
    title: string;
    trigger: string;
    desc: string;
    accept: string[];
    preview: (t: string, f: ADImportFile[]) => Promise<ADImportPreview>;
    apply: (t: string, f: ADImportFile[]) => Promise<ADImportResult>;
  }
> = {
  ad: {
    title: "导入 AD 采集结果",
    trigger: "导入采集结果",
    desc: "把 SharpHound / BloodHound 导出的 JSON（users.json、groups.json、computers.json、domains.json…）一次全选进来，先「检查」看清会导什么，确认无误再落库。已存在的对象按 SID 合并，不会重复登记。",
    accept: [".json"],
    preview: (t, f) => api.previewADImport(t, f),
    apply: (t, f) => api.importAD(t, f),
  },
  cloud: {
    title: "导入云上枚举结果",
    trigger: "导入云上枚举结果",
    desc:
      "把云上枚举的 JSON 导进来（同一张图，与域内数据一起算路径）。支持两种：" +
      "① AWS `aws iam get-account-authorization-details` 的输出；" +
      "② K8s `kubectl get clusterroles,clusterrolebindings,roles,rolebindings,serviceaccounts -A -o json` 的输出。" +
      "**平台不持有云凭据**——枚举在有凭据的那台机器上做，这里只读离线结果。",
    accept: [".json"],
    preview: (t, f) => api.previewCloudImport(t, f),
    apply: (t, f) => api.importCloud(t, f),
  },
};

const CATEGORY_LABEL: Record<string, string> = {
  user: "用户",
  group: "组",
  computer: "机器",
  domain: "域",
  ou: "OU",
  gpo: "GPO",
  stub: "推断节点",
  unknown: "未知",
};

const REL_LABEL: Record<string, string> = {
  member_of: "成员",
  admin_to: "本机管理员",
  session_on: "有会话",
  can: "权限",
};

function countSummary(counts: Record<string, number>, labels: Record<string, string>): string {
  return Object.entries(counts)
    .map(([k, v]) => `${labels[k] ?? k} ${v}`)
    .join(" · ");
}

export function ADImportDialog({
  taskId,
  onImported,
  source = "ad",
}: {
  taskId: string;
  onImported: () => void;
  /** 吃哪一种枚举结果：AD 的 SharpHound / 云上的 aws-cli、kubectl 输出。 */
  source?: ImportSource;
}) {
  const cfg = SOURCE[source];
  const [open, setOpen] = React.useState(false);
  const [files, setFiles] = React.useState<ADImportFile[]>([]);
  const [preview, setPreview] = React.useState<ADImportPreview | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [applying, setApplying] = React.useState(false);
  const fileRef = React.useRef<HTMLInputElement>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: source 变化时清空上一次的导入状态；effect 体内只重置 state、不读取 source。
  React.useEffect(() => {
    if (!open) return;
    setFiles([]);
    setPreview(null);
  }, [open, source]);

  const addFiles = async (picked: FileList | null) => {
    if (!picked || picked.length === 0) return;
    try {
      const loaded: ADImportFile[] = [];
      for (const f of Array.from(picked)) {
        loaded.push({ name: f.name, json: await f.text() });
      }
      setFiles((prev) => [...prev, ...loaded]);
      setPreview(null); // 清单变了，旧预览作废
    } catch (e) {
      toast.error(`读取文件失败：${String((e as Error)?.message ?? e)}`);
    }
  };

  const runPreview = async () => {
    setBusy(true);
    try {
      setPreview(await cfg.preview(taskId, files));
    } catch (e) {
      toast.error(`预览失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setApplying(true);
    try {
      const res = await cfg.apply(taskId, files);
      const parts = [
        `新增 ${res.nodes_created} 个对象`,
        `合并 ${res.nodes_updated} 个`,
        `新建 ${res.edges_created} 条关系`,
      ];
      if ((res.failures ?? 0) > 0) {
        toast.warning(`${parts.join("、")}；${res.failures} 项失败`);
      } else if (res.warnings.length > 0) {
        toast.warning(`${parts.join("、")}；有 ${res.warnings.length} 条提醒（预览里可查）`);
      } else {
        toast.success(parts.join("、"));
      }
      setOpen(false);
      onImported();
    } catch (e) {
      toast.error(`导入失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setApplying(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <UploadIcon data-icon="inline-start" /> {cfg.trigger}
        </Button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <DialogHeader className="border-b p-6">
          <DialogTitle>{cfg.title}</DialogTitle>
          <DialogDescription>{cfg.desc}</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto p-6">
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => fileRef.current?.click()} disabled={busy || applying}>
              <FileUpIcon data-icon="inline-start" /> 选择文件（可多选）
            </Button>
            <input
              ref={fileRef}
              type="file"
              multiple
              accept={cfg.accept.join(",") + ",application/json"}
              className="hidden"
              onChange={(e) => {
                void addFiles(e.target.files);
                e.target.value = ""; // 同一个文件能连着选第二次
              }}
            />
            <span className="text-xs text-muted-foreground">
              采集结果里没有的引用（未导的组/账号）会标成「推断节点」，不会被丢掉
            </span>
          </div>

          {files.length > 0 && (
            <ul className="mt-3 space-y-1">
              {files.map((f, i) => (
                <li key={`${f.name}-${i}`} className="flex items-center gap-2 text-sm">
                  <span className="truncate">{f.name}</span>
                  <span className="shrink-0 text-xs text-muted-foreground">{Math.round(f.json.length / 1024)} KB</span>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-6 w-6 shrink-0"
                    disabled={busy || applying}
                    onClick={() => {
                      setFiles((prev) => prev.filter((_, idx) => idx !== i));
                      setPreview(null);
                    }}
                  >
                    <Trash2Icon className="h-3.5 w-3.5" />
                  </Button>
                </li>
              ))}
            </ul>
          )}

          {preview && (
            <div className="mt-4 space-y-3 rounded-md border p-4">
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Badge variant="default">新增对象 {preview.nodes_new}</Badge>
                {preview.nodes_existing > 0 && <Badge variant="outline">已有 {preview.nodes_existing}</Badge>}
                <Badge variant="secondary">新增关系 {preview.edges_new}</Badge>
                {preview.edges_existing > 0 && <Badge variant="outline">已有关系 {preview.edges_existing}</Badge>}
              </div>
              {Object.keys(preview.objects).length > 0 && (
                <p className="text-sm text-muted-foreground">{countSummary(preview.objects, CATEGORY_LABEL)}</p>
              )}
              {Object.keys(preview.edges).length > 0 && (
                <p className="text-sm text-muted-foreground">{countSummary(preview.edges, REL_LABEL)}</p>
              )}

              {preview.highvalue.length > 0 && (
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">高价值对象（路径会往这些目标收敛）</p>
                  <ul className="space-y-0.5 text-xs">
                    {preview.highvalue.map((h) => (
                      <li key={`${h.category}-${h.name}`} className="flex items-center gap-2">
                        <Badge variant="secondary" className="shrink-0">
                          {CATEGORY_LABEL[h.category] ?? h.category}
                        </Badge>
                        <span className="truncate font-mono">{h.name}</span>
                        {h.stub && <span className="shrink-0 text-muted-foreground">（推断）</span>}
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              {preview.warnings.length > 0 && (
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">提醒</p>
                  <ul className="space-y-0.5 text-xs text-muted-foreground">
                    {preview.warnings.map((w) => (
                      <li key={w}>{w}</li>
                    ))}
                  </ul>
                </div>
              )}

              {preview.skipped_rights && Object.keys(preview.skipped_rights).length > 0 && (
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">
                    未建边的权限（白名单外，避免图被噪声淹没）
                  </p>
                  <p className="font-mono text-xs text-muted-foreground">
                    {Object.entries(preview.skipped_rights)
                      .map(([k, v]) => `${k}×${v}`)
                      .join("、")}
                  </p>
                </div>
              )}
            </div>
          )}
        </div>

        <DialogFooter className="border-t p-6">
          <Button variant="outline" onClick={() => setOpen(false)} disabled={busy || applying}>
            取消
          </Button>
          <Button variant="secondary" onClick={runPreview} disabled={busy || applying || files.length === 0}>
            {busy ? <LoaderIcon className="animate-spin" data-icon="inline-start" /> : null}
            检查
          </Button>
          <Button onClick={apply} disabled={applying || !preview || preview.nodes_new + preview.nodes_existing === 0}>
            {applying ? <LoaderIcon className="animate-spin" data-icon="inline-start" /> : null}
            应用导入{preview ? `（${preview.nodes_new} 个新对象）` : ""}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
