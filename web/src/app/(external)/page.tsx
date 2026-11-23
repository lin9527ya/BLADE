"use client";

import { useEffect } from "react";

import { useRouter } from "next/navigation";

import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

// 静态导出不能依赖服务端 redirect()：那样 / 会被导出成 Next 的错误页。
// 这里改成客户端跳转——已登录进主界面，未登录按是否初始化过密码去 /login 或 /setup。
export default function Home() {
  const router = useRouter();

  useEffect(() => {
    if (auth.getToken()) {
      router.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => router.replace(initialized ? "/login" : "/setup"))
      .catch(() => router.replace("/login"));
  }, [router]);

  return (
    <div className="flex min-h-dvh items-center justify-center text-muted-foreground text-sm">正在加载…</div>
  );
}
