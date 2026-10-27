import { loadInputHistory, rememberInputHistory } from "./input-history.ts";
import assert from "node:assert/strict";
import test from "node:test";

// Node 里没有 window；history 模块在调用时才取 localStorage，所以装一个内存实现即可。
function installStorage() {
  const data = new Map();
  globalThis.window = {
    localStorage: {
      getItem: (key) => (data.has(key) ? data.get(key) : null),
      setItem: (key, value) => data.set(key, String(value)),
    },
  };
  return data;
}

test("remember dedups consecutive entries and keeps oldest→newest order", () => {
  installStorage();
  rememberInputHistory("t", "one");
  rememberInputHistory("t", "two");
  rememberInputHistory("t", "two");
  rememberInputHistory("t", " three ");
  assert.deepEqual(loadInputHistory("t"), ["one", "two", "three"]);
});

test("empty and whitespace-only messages are ignored", () => {
  installStorage();
  rememberInputHistory("t", "   ");
  rememberInputHistory("t", "");
  assert.deepEqual(loadInputHistory("t"), []);
});

test("scopes are isolated and corrupt JSON degrades to empty", () => {
  const data = installStorage();
  rememberInputHistory("a", "alpha");
  rememberInputHistory("b", "beta");
  assert.deepEqual(loadInputHistory("a"), ["alpha"]);
  assert.deepEqual(loadInputHistory("b"), ["beta"]);
  data.set("blade.input-history.v1:bad", "{not json");
  assert.deepEqual(loadInputHistory("bad"), []);
});

test("history is capped at 50 entries", () => {
  installStorage();
  for (let i = 0; i < 60; i++) rememberInputHistory("t", `cmd-${i}`);
  const rows = loadInputHistory("t");
  assert.equal(rows.length, 50);
  assert.equal(rows[0], "cmd-10");
  assert.equal(rows.at(-1), "cmd-59");
});
