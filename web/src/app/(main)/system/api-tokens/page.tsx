"use client";

import { SettingsWorkbench } from "../settings/_components/settings-workbench";

// 兼容别名：API 令牌已并入系统配置的「API 令牌」分区，保留旧路由直达能力。
export default function APITokensPage() {
  return <SettingsWorkbench initialSection="tokens" />;
}
