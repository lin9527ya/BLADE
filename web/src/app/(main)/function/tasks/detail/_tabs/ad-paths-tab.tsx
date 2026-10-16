"use client";

import * as React from "react";

import { LoaderIcon, MoveRightIcon, RefreshCwIcon } from "lucide-react";

import { ADImportDialog } from "@/components/ad-import-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { ADPath, ADPathsResponse } from "@/lib/types";

// 攻击路径（P32 域内 / P39 云上）：把导入的拓扑读成「从手头账号到高价值目标」的最短路。
//
// 域内（AD 采集结果）与云上（IAM 授权明细 / K8s RBAC）进的是**同一张图**、走的是同一条
// 路径算法（db/ad_paths.go：member_of / can 顺势走），所以这里一起读——现实里"从一台机器上的
// 凭据到云上 admin 角色"正是要连起来看的那种路径。
//
// 这一页只读图、不碰目标：路径表示**存在**这条利用链，要不要走、怎么走仍走既有的意图与命令门。
// 起点默认是全部非特权账号（域管/管理员自己不算「手头的账号」——那会让「DA 一跳走到 DA」把真路挤掉）。

const CATEGORY_LABEL: Record<string, string> = {
  // 域内（AD）
  user: "用户",
  group: "组",
  computer: "机器",
  domain: "域",
  ou: "OU",
  gpo: "GPO",
  // 云上（P39）：主体与授权
  account: "账号",
  role: "角色",
  policy: "策略",
  service_account: "服务账号",
  namespace: "命名空间",
  cluster: "集群",
  stub: "推断节点",
  unknown: "未知",
};

const REL_LABEL: Record<string, string> = {
  member_of: "成员",
  admin_to: "本机管理员",
  session_on: "有会话",
  can: "权限",
};

function PathCard({ path }: { path: ADPath }) {
  return (
    <div className="rounded-md border p-4">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-mono">{path.start}</span>
        <MoveRightIcon className="h-4 w-4 text-muted-foreground" />
        <span className="font-mono font-medium">{path.target}</span>
        {(path.target_tags ?? []).map((t) => (
          <Badge key={t} variant="secondary">
            {t}
          </Badge>
        ))}
        <span className="ml-auto text-xs text-muted-foreground">{path.steps.length} 跳</span>
      </div>
      <ol className="mt-3 space-y-2">
        {path.steps.map((s, i) => (
          <li key={`${s.from}-${s.rel}-${s.to}-${i}`} className="text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono">{s.from}</span>
              <Badge variant="outline" className="shrink-0">
                {REL_LABEL[s.rel] ?? s.rel}
              </Badge>
              {(s.rights ?? []).length > 0 && (
                <span className="font-mono text-xs text-muted-foreground">{(s.rights ?? []).join(" ")}</span>
              )}
              <MoveRightIcon className="h-3.5 w-3.5 text-muted-foreground" />
              <span className="font-mono">{s.to}</span>
            </div>
            {s.note && <p className="mt-0.5 text-xs text-muted-foreground">{s.note}</p>}
          </li>
        ))}
      </ol>
    </div>
  );
}

export function ADPathsTab({ taskId }: { taskId: string }) {
  const [data, setData] = React.useState<ADPathsResponse | null>(null);
  const [start, setStart] = React.useState("0");
  const [loading, setLoading] = React.useState(false);
  const [failed, setFailed] = React.useState("");

  const load = React.useCallback(
    (startID: number) => {
      setLoading(true);
      api
        .adPaths(taskId, startID)
        .then((d) => {
          setData(d);
          setFailed("");
        })
        .catch((e: unknown) => setFailed(String((e as Error)?.message ?? e)))
        .finally(() => setLoading(false));
    },
    [taskId],
  );

  React.useEffect(() => {
    load(0);
    setStart("0");
  }, [load]);

  const objects = data?.stats.objects ?? {};
  const edges = data?.stats.edges ?? {};
  const hasGraph = (data?.stats.nodes ?? 0) > 0;

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="flex flex-col gap-3 p-4">
          <div className="flex flex-wrap items-center gap-2">
            <ADImportDialog taskId={taskId} onImported={() => load(Number(start))} />
            <ADImportDialog taskId={taskId} source="cloud" onImported={() => load(Number(start))} />
            <Button variant="ghost" size="sm" onClick={() => load(Number(start))} disabled={loading}>
              {loading ? <LoaderIcon className="animate-spin" data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}
              刷新
            </Button>
            {(data?.starts.length ?? 0) > 0 && (
              <div className="ml-auto flex items-center gap-2">
                <span className="text-xs text-muted-foreground">起点</span>
                <Select
                  value={start}
                  onValueChange={(v) => {
                    setStart(v);
                    load(Number(v));
                  }}
                >
                  <SelectTrigger className="w-64">
                    <SelectValue placeholder="选择起点账号" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="0">全部非特权账号</SelectItem>
                    {(data?.starts ?? []).map((s) => (
                      <SelectItem key={s.id} value={String(s.id)}>
                        {s.name}
                        {s.owned ? "（在手）" : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>

          {hasGraph ? (
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <Badge variant="secondary">域内对象 {data?.stats.nodes ?? 0}</Badge>
              {Object.entries(objects).map(([k, v]) => (
                <span key={k}>
                  {CATEGORY_LABEL[k] ?? k} {v}
                </span>
              ))}
              <span className="mx-1">·</span>
              {Object.entries(edges).map(([k, v]) => (
                <span key={k}>
                  {REL_LABEL[k] ?? k} {v}
                </span>
              ))}
              <span className="mx-1">·</span>
              <span>高价值目标 {data?.stats.highvalue ?? 0}</span>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              还没有域内/云上数据：导入一次 SharpHound / BloodHound 采集结果（域内），或
              aws-cli / kubectl 的枚举输出（云上），就能看到从手头账号到 Domain Admins、域控、
              admin 策略、集群管理员这些目标的最短利用链。
            </p>
          )}

          {failed && <p className="text-sm text-destructive">读取失败：{failed}</p>}
        </CardContent>
      </Card>

      {hasGraph && (data?.paths.length ?? 0) === 0 && (
        <Card>
          <CardContent className="p-4 text-sm text-muted-foreground">
            当前起点的账号走不到任何高价值目标（也可能图里还没有标记为高价值的对象）。
          </CardContent>
        </Card>
      )}

      {(data?.paths ?? []).map((p) => (
        <PathCard key={`${p.start}-${p.target}`} path={p} />
      ))}

      {(data?.paths.length ?? 0) > 0 && (
        <p className="px-1 text-xs text-muted-foreground">
          这些路径由目录采集结果推导，表示**存在**这条利用链，不代表已实施；动手仍走意图与命令门。
        </p>
      )}
    </div>
  );
}
