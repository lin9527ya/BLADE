import test from "node:test";
import assert from "node:assert/strict";
import { aggregateOperation, completeAggregateOperation } from "./aggregate-operation.ts";

test("same request retries and remounts keep one key without storing request contents",async()=>{
  const map=new Map();const storage={getItem:key=>map.get(key),setItem:(key,value)=>map.set(key,value),removeItem:key=>map.delete(key)};
  const parameters=JSON.stringify({name:"private note fixture",scope_id:1});
  const a=await aggregateOperation(parameters,undefined,storage);
  assert.equal((await aggregateOperation(parameters,a,storage)).key,a.key);
  assert.equal((await aggregateOperation(parameters,undefined,storage)).key,a.key);
  assert.equal([...map.values()].join("").includes("private note fixture"),false);
  const b=await aggregateOperation(JSON.stringify({scope_id:2}),a,storage);assert.notEqual(a.key,b.key);
  completeAggregateOperation(a,storage);assert.equal(map.size,1);
  completeAggregateOperation(b,storage);assert.equal(map.size,0);
});

test("blocked browser storage does not prevent same-component retries",async()=>{
  const blocked={getItem(){throw Error("blocked")},setItem(){throw Error("blocked")},removeItem(){throw Error("blocked")}};
  const a=await aggregateOperation("fixture",undefined,blocked);
  assert.equal((await aggregateOperation("fixture",a,blocked)).key,a.key);
});
