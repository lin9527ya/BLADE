import { formatShellArgs, mcpGuardSummary, parseShellArgs, transportLabel, validateMcpForm } from "./mcp-helpers.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("parseShellArgs and formatShellArgs roundtrip quoted flags cleanly", () => {
  const parsed = parseShellArgs(`-y @pkg/server --dir "/path/with spaces" --tag='a b' plain\\ arg`);
  assert.deepEqual(parsed, ["-y", "@pkg/server", "--dir", "/path/with spaces", "--tag=a b", "plain arg"]);
  const formatted = formatShellArgs(parsed);
  assert.deepEqual(parseShellArgs(formatted), parsed);
  assert.deepEqual(parseShellArgs("   "), []);
});

test("transportLabel distinguishes stdio, http, and sse accurately", () => {
  assert.equal(transportLabel("stdio"), "（本地起进程）");
  assert.equal(transportLabel("http"), "（远程 Streamable HTTP）");
  assert.equal(transportLabel("sse"), "（远程 Legacy SSE）");
});

test("validateMcpForm enforces safe names and transport fields", () => {
  assert.equal(
    validateMcpForm({ name: "my server", transport: "stdio", command: "npx", url: "" }),
    "名称不能包含空格、斜杠或逗号",
  );
  assert.equal(
    validateMcpForm({ name: "bad__name", transport: "stdio", command: "npx", url: "" }),
    "名称不能包含双下划线 __",
  );
  assert.equal(
    validateMcpForm({ name: "burp", transport: "sse", command: "", url: "ftp://127.0.0.1:9876/sse" }),
    "远程 URL 仅支持 http:// 或 https://",
  );
  assert.equal(
    validateMcpForm({ name: "burp", transport: "sse", command: "", url: "http://127.0.0.1:9876/sse" }),
    null,
  );
  assert.equal(validateMcpForm({ name: "chrome-devtools", transport: "stdio", command: "npx", url: "" }), null);
});

test("mcpGuardSummary formats open and healthy guard snapshots", () => {
  assert.equal(mcpGuardSummary(undefined).includes("暂无运行记录"), true);
  assert.equal(
    mcpGuardSummary({
      server: "browser",
      max_concurrent: 2,
      inflight: 1,
      consecutive_failures: 3,
      open: true,
      open_remaining_sec: 45,
    }),
    "并发上限 2（进行中 1） · 熔断已开闸（剩余 45s）",
  );
});
