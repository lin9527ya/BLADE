"use client";

import * as React from "react";

import { InterceptWorkbench } from "../_components/intercept-workbench";

// 兼容别名：旧地址（资产拦截）落在合并页的「资产黑名单」分区。
export default function AssetInterceptPage() {
  return (
    <React.Suspense fallback={null}>
      <InterceptWorkbench initialSection="assets" />
    </React.Suspense>
  );
}
