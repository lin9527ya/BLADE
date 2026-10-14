"use client";

import * as React from "react";

import { InterceptWorkbench } from "./_components/intercept-workbench";

// 拦截与审批：四个分区在同一页（命令拦截 / 模型兜底审批 / 资产黑名单 / 审批记录）。
// 静态导出下 useSearchParams（?tab= 深链）需要 Suspense 包一层。
export default function InterceptPage() {
  return (
    <React.Suspense fallback={null}>
      <InterceptWorkbench />
    </React.Suspense>
  );
}
