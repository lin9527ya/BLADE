"use client";

import { LootWorkbench } from "../loot/_components/loot-workbench";

// 兼容别名：凭据工作台已合入「战果与凭据工作台」的凭据分区。
export default function CredentialsPage() {
  return <LootWorkbench initialSection="credentials" />;
}
