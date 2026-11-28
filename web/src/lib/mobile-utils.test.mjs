import {
  deviceCapabilitySummary,
  formatSampleBytes,
  parseCandidateDomainInput,
  trafficSourceLabel,
  uiElementDisplayLabel,
} from "./mobile-utils.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("formatSampleBytes formats large APKs accurately", () => {
  assert.equal(formatSampleBytes(0), "0 B");
  assert.equal(formatSampleBytes(1024 * 1024 * 512), "512.0 MB");
  assert.equal(formatSampleBytes(1024 * 1024 * 1024), "1.0 GB");
});

test("trafficSourceLabel identifies mobile WS SSE", () => {
  assert.equal(trafficSourceLabel({ source: "mobile", method: "POST" }), "移动端");
  assert.equal(trafficSourceLabel({ source: "web", method: "WS" }), "WebSocket");
  assert.equal(trafficSourceLabel({ source: "web", method: "SSE" }), "SSE");
  assert.equal(trafficSourceLabel({ source: "web", method: "GET" }), "");
});

test("deviceCapabilitySummary summarizes root/CA/Frida flags", () => {
  const tags = deviceCapabilitySummary({
    connection_type: "usb",
    rooted: true,
    system_ca_status: "system_apex",
    frida_server: "running",
  });
  assert.deepEqual(tags, ["USB ADB", "Root", "系统CA就绪", "Frida运行中"]);
});

test("uiElementDisplayLabel and parseCandidateDomainInput", () => {
  assert.equal(
    uiElementDisplayLabel({
      index: 0,
      class: "Button",
      text: "",
      resource_id: "com.demo:id/submit_btn",
      enabled: true,
      bounds: "[0,0][10,10]",
      center_x: 5,
      center_y: 5,
    }),
    "submit_btn",
  );
  assert.deepEqual(parseCandidateDomainInput("https://api.acme.cn/v1, auth.acme.cn\napi.acme.cn"), [
    "api.acme.cn",
    "auth.acme.cn",
  ]);
});

test("iOS discovery labels identify the platform independently of ADB", () => {
  assert.deepEqual(
    deviceCapabilitySummary({
      platform: "ios",
      connection_type: "usb",
      rooted: false,
      system_ca_status: "unknown",
      frida_server: "unknown",
    }),
    ["USB iOS"],
  );
});
