"use client";

import * as React from "react";

import { InterceptWorkbench } from "../_components/intercept-workbench";

// 兼容别名：旧地址（审批记录）落在合并页的「审批记录」分区。
export default function ApprovalsPage() {
  return (
    <React.Suspense fallback={null}>
      <InterceptWorkbench initialSection="approvals" />
    </React.Suspense>
  );
}
