"use client";

import * as React from "react";

import { Section } from "@/components/page-sections";

import { UpdateCard } from "./update-card";

// 系统配置 · 基本设置。

export function BasicSection() {
  return (
    <>
      <Section title="基本设置" hint="版本检查与一键更新。更新/回滚会重启服务进程并中断运行中的任务。">
        <UpdateCard />
      </Section>
    </>
  );
}
