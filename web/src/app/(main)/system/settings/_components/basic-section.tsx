"use client";

import * as React from "react";

import { UpdateCard } from "./update-card";
import { Section } from "@/components/page-sections";

import type { useSettingsState } from "./use-settings-state";

// 系统配置 · 基本设置。

export function BasicSection({ s }: { s: ReturnType<typeof useSettingsState> }) {
  const {  } = s;

  return (
    <>

          <Section title="基本设置" hint="版本检查与一键更新。更新/回滚会重启服务进程并中断运行中的任务。">
            <UpdateCard />
          </Section>
        
    </>
  );
}
