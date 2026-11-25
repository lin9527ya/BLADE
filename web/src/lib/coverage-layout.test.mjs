import {
  collectCoverageLineage,
  computeVisibleCoverageNodes,
  filterCoverageGraphByStatus,
  findMatchingCoverageKeys,
  layoutCoverageAssetNodes,
  layoutCoverageNetworkNodes,
  NETWORK_TOP_OFFSET,
} from "./coverage-layout.ts";
import assert from "node:assert/strict";
import test from "node:test";

const asset = (key, kind, label, extra = {}) => ({
  key,
  kind,
  label,
  tested: false,
  in_scope: true,
  ...extra,
});

test("layoutCoverageAssetNodes 将 service 与 app 垂直错开且折点跟随父节点右侧", () => {
  const nodes = [
    asset("co:1", "company", "ACME"),
    asset("ip:1", "ip", "10.0.0.1"),
    asset("svc:1", "service", "10.0.0.1:443"),
    asset("app:1", "app", "Portal"),
  ];
  const edges = [
    { src: "ip:1", dst: "co:1" },
    { src: "svc:1", dst: "ip:1" },
    { src: "app:1", dst: "ip:1" },
  ];

  const { renderNodes, renderEdges } = computeVisibleCoverageNodes(nodes, edges, new Map(), false);
  const { positions } = layoutCoverageAssetNodes(renderNodes, renderEdges);

  const ipPos = positions.get("ip:1");
  const svcPos = positions.get("svc:1");
  const appPos = positions.get("app:1");
  assert.ok(positions.get("co:1").x < ipPos.x);
  assert.ok(ipPos.x < svcPos.x && ipPos.x < appPos.x);
  assert.notEqual(svcPos.y, appPos.y, "同一父节点下的 service 与 app 不应重叠");
});

test("layoutCoverageNetworkNodes 保证三列分区与顶部标题留白", () => {
  const nodes = [
    asset("sub:1", "subdomain", "api.example.com", { zone: "internet" }),
    asset("svc:1", "service", "443", { zone: "boundary" }),
    asset("ep:1", "endpoint", "/admin", { zone: "internal" }),
  ];
  const { renderNodes } = computeVisibleCoverageNodes(nodes, [], new Map(), true);
  const { positions } = layoutCoverageNetworkNodes(renderNodes);

  assert.ok(positions.get("sub:1").x < positions.get("svc:1").x);
  assert.ok(positions.get("svc:1").x < positions.get("ep:1").x);
  assert.ok(positions.get("sub:1").y >= NETWORK_TOP_OFFSET);
});

test("filterCoverageGraphByStatus 按测试状态过滤并保留上游树干", () => {
  const nodes = [
    asset("co:1", "company", "ACME"),
    asset("sub:1", "subdomain", "a.acme.com"),
    asset("sub:2", "subdomain", "b.acme.com", { tested: true }),
    asset("ep:1", "endpoint", "/v1", { tested: true }),
    asset("ep:2", "endpoint", "/v2", { tested: false }),
  ];
  const edges = [
    { src: "sub:1", dst: "co:1" },
    { src: "sub:2", dst: "co:1" },
    { src: "ep:1", dst: "sub:2" },
    { src: "ep:2", dst: "sub:1" },
  ];

  const testedOnly = filterCoverageGraphByStatus(nodes, edges, "tested");
  assert.deepEqual(
    testedOnly.nodes.map((n) => n.key),
    ["co:1", "sub:2", "ep:1"],
  );

  const untestedOnly = filterCoverageGraphByStatus(nodes, edges, "untested");
  assert.deepEqual(
    untestedOnly.nodes.map((n) => n.key),
    ["co:1", "sub:1", "ep:2"],
  );
});

test("collectCoverageLineage 双向追踪上下游与 findMatchingCoverageKeys 检索", () => {
  const nodes = [
    asset("co:1", "company", "ACME"),
    asset("ip:1", "ip", "10.0.0.9"),
    asset("svc:1", "service", "8443", { port: 8443, page_title: "Admin" }),
  ];
  const edges = [
    { src: "ip:1", dst: "co:1" },
    { src: "svc:1", dst: "ip:1" },
  ];

  const lineage = collectCoverageLineage("ip:1", edges);
  assert.equal(lineage.upstreamCount, 1);
  assert.equal(lineage.downstreamCount, 1);
  assert.deepEqual(lineage.activeKeys, ["co:1", "ip:1", "svc:1"]);

  const { renderNodes } = computeVisibleCoverageNodes(nodes, edges, new Map(), false);
  assert.deepEqual(findMatchingCoverageKeys(renderNodes, "8443"), ["svc:1"]);
});
