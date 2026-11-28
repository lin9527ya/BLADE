"use client";

import { CommandsWorkbench } from "../commands/_components/commands-workbench";

// 兼容别名：验证台账已并入「工具执行 → 验证视角」，保留旧路径直达能力。
export default function VerificationLedgerPage() {
  return <CommandsWorkbench initialView="verification" />;
}
