"use client";

import { LootWorkbench } from "../loot/_components/loot-workbench";

// 兼容别名：据点与隧道已合入「战果与凭据工作台」的据点与隧道分区。
export default function FootholdsPage() {
  return <LootWorkbench initialSection="footholds" />;
}
