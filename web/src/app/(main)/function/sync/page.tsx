"use client";

import Link from "next/link";

import { ArrowLeftIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

import { ScopeSentryPanel } from "../assets/_components/scope-sentry-panel";

// 兼容别名页：ScopeSentry 同步已收纳至「资产」工具栏弹窗，原路由复用同一组件保持可用。
export default function AssetSyncPage() {
  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">资产同步</h1>
          <p className="text-muted-foreground text-sm">从外部数据源同步资产入库</p>
        </div>
        <Button asChild variant="outline" size="sm">
          <Link href="/function/assets">
            <ArrowLeftIcon className="size-4" /> 返回资产库
          </Link>
        </Button>
      </div>
      <Tabs defaultValue="scopesentry">
        <TabsList>
          <TabsTrigger value="scopesentry">ScopeSentry</TabsTrigger>
        </TabsList>
        <TabsContent value="scopesentry" className="mt-4">
          <ScopeSentryPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
