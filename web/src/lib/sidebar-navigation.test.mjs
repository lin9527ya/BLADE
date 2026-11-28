import { quickSearchAliases, sidebarItems } from "../navigation/sidebar/sidebar-items.ts";
import { buildSidebarLabelMap, resolveBreadcrumbCrumbs } from "./breadcrumbs-format.ts";
import { en } from "./i18n/en.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("sidebar maintains slim top-level footprint, unique ids, and covers legacy routes", () => {
  const topLevelCount = sidebarItems.reduce((sum, g) => sum + g.items.length, 0);
  assert.equal(sidebarItems.length, 3);
  assert.ok(topLevelCount <= 16, `expected <= 16 top-level items, got ${topLevelCount}`);

  const ids = new Set();
  const matchedPrefixes = new Set();
  for (const group of sidebarItems) {
    assert.ok(en[`nav.group.${group.id}`], `missing i18n key nav.group.${group.id}`);
    for (const item of group.items) {
      assert.ok(!ids.has(item.id), `duplicate nav id ${item.id}`);
      ids.add(item.id);
      assert.ok(en[`nav.${item.id}`], `missing i18n key nav.${item.id}`);
      if ("url" in item) {
        for (const u of item.matchUrls ?? [item.url]) matchedPrefixes.add(u);
      }
      for (const sub of item.subItems ?? []) {
        assert.ok(!ids.has(sub.id), `duplicate sub nav id ${sub.id}`);
        ids.add(sub.id);
        assert.ok(en[`nav.${sub.id}`], `missing i18n key nav.${sub.id}`);
        for (const u of sub.matchUrls ?? [sub.url]) matchedPrefixes.add(u);
      }
    }
  }

  for (const alias of quickSearchAliases) {
    assert.ok(en[`nav.${alias.id}`], `missing i18n key nav.${alias.id}`);
  }

  const requiredRoutes = [
    "/dashboard",
    "/chat",
    "/function/tasks",
    "/function/schedules",
    "/function/findings",
    "/function/assets",
    "/function/sync",
    "/function/loot",
    "/function/credentials",
    "/function/footholds",
    "/function/traffic",
    "/function/campaigns",
    "/function/deliverables",
    "/function/coverage",
    "/function/playbooks",
    "/function/cve",
    "/function/broadcast",
    "/function/commands",
    "/function/verification-ledger",
    "/function/workspace",
    "/system/tools",
    "/system/skills",
    "/system/mcp",
    "/system/llm",
    "/system/agents",
    "/system/intercept",
    "/system/notify",
    "/system/im",
    "/system/diagnostics",
    "/system/logs",
    "/system/storage",
    "/function/llm-records",
    "/system/settings",
    "/system/api-tokens",
  ];
  for (const route of requiredRoutes) {
    assert.ok(matchedPrefixes.has(route), `route ${route} not matched by any sidebar item`);
  }
});

test("resolveBreadcrumbCrumbs prevents clickable 404 on virtual /function and /system segments", () => {
  const labels = buildSidebarLabelMap(sidebarItems);

  assert.deepEqual(resolveBreadcrumbCrumbs("/dashboard", labels), []);
  assert.deepEqual(resolveBreadcrumbCrumbs("/", labels), []);

  const taskDetail = resolveBreadcrumbCrumbs("/function/tasks/detail", labels);
  assert.deepEqual(taskDetail, [
    { href: "/function", label: "功能", clickable: false },
    { href: "/function/tasks", label: "任务", clickable: true },
    { href: "/function/tasks/detail", label: "任务详情", clickable: false },
  ]);

  const approvals = resolveBreadcrumbCrumbs("/system/intercept/approvals", labels);
  assert.deepEqual(approvals, [
    { href: "/system", label: "系统", clickable: false },
    { href: "/system/intercept", label: "拦截与审批", clickable: true },
    { href: "/system/intercept/approvals", label: "审批记录", clickable: false },
  ]);

  const apiTokens = resolveBreadcrumbCrumbs("/system/api-tokens", labels);
  assert.deepEqual(apiTokens, [
    { href: "/system", label: "系统", clickable: false },
    { href: "/system/api-tokens", label: "API 令牌", clickable: false },
  ]);
});
