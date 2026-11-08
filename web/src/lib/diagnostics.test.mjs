import assert from "node:assert/strict";
import test from "node:test";
import { diagnosticHitRate, cacheStructureExplanation } from "./diagnostics.ts";
test("diagnostic ratios distinguish missing and invalid samples", () => {
  assert.equal(diagnosticHitRate(0,0),"—");
  assert.equal(diagnosticHitRate(100,80),"80.00%");
  for (const [i,r] of [[20,80],[-1,0],[NaN,0],[100,-1]]) assert.equal(diagnosticHitRate(i,r),"数据异常");
});
test("structure evidence does not claim a proven cache cause", () => {
  const r={previous:true,system_same:true,tools_same:true,shared_messages:2,messages:3,previous_messages:2};
  assert.match(cacheStructureExplanation(r),/不能仅凭结构确定/);
  assert.match(cacheStructureExplanation({...r,previous:false}),/不等于模型会话首次调用/);
  assert.match(cacheStructureExplanation({...r,tools_same:false}),/不是根因确认/);
  assert.match(cacheStructureExplanation({...r,shared_messages:1}),/不是根因确认/);
});
