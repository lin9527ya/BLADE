import { clampPage, paginateSlice, parseJumpPage, totalPagesFor } from "./pagination.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("totalPagesFor 和 clampPage 边界正确", () => {
  assert.equal(totalPagesFor(0, 5), 1);
  assert.equal(totalPagesFor(5, 5), 1);
  assert.equal(totalPagesFor(6, 5), 2);
  assert.equal(totalPagesFor(21, 10), 3);

  assert.equal(clampPage(0, 12, 5), 1);
  assert.equal(clampPage(2, 12, 5), 2);
  assert.equal(clampPage(9, 12, 5), 3);
  assert.equal(clampPage(NaN, 12, 5), 1);
});

test("paginateSlice 按页切片并保护越界页码", () => {
  const items = ["a", "b", "c", "d", "e", "f", "g"];
  assert.deepEqual(paginateSlice(items, 1, 5), ["a", "b", "c", "d", "e"]);
  assert.deepEqual(paginateSlice(items, 2, 5), ["f", "g"]);
  assert.deepEqual(paginateSlice(items, 99, 5), ["f", "g"]);
});

test("parseJumpPage 解析正整数并夹紧上限", () => {
  assert.equal(parseJumpPage(" 2 ", 5), 2);
  assert.equal(parseJumpPage("99", 5), 5);
  assert.equal(parseJumpPage("0", 5), null);
  assert.equal(parseJumpPage("-1", 5), null);
  assert.equal(parseJumpPage("abc", 5), null);
  assert.equal(parseJumpPage("", 5), null);
});
