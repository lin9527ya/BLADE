import type { MobileDevice, MobileUIElement, TrafficExchange } from "./types";

/** 格式化移动端安装包/数据大小（支持 GB 级大包）。 */
export function formatSampleBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const idx = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const val = bytes / 1024 ** idx;
  return `${idx === 0 ? val : val.toFixed(1)} ${units[idx]}`;
}

/** 根据流量来源和 HTTP 方法生成直观标签（如 移动端 / WS / SSE）。 */
export function trafficSourceLabel(ex: Pick<TrafficExchange, "source" | "method">): string {
  if (ex.method === "WS") return "WebSocket";
  if (ex.method === "SSE") return "SSE";
  if (ex.source === "mobile") return "移动端";
  return "";
}

/** 将 MobileDevice 状态概括成简明徽标文本。 */
export function deviceCapabilitySummary(
  dev: Pick<MobileDevice, "connection_type" | "rooted" | "system_ca_status" | "frida_server">,
): string[] {
  const tags: string[] = [];
  tags.push(dev.connection_type === "wifi" ? "Wi-Fi ADB" : "USB ADB");
  if (dev.rooted) tags.push("Root");
  if (dev.system_ca_status === "system_apex" || dev.system_ca_status === "system_legacy") {
    tags.push("系统CA就绪");
  } else if (dev.system_ca_status === "user") {
    tags.push("用户CA");
  }
  if (dev.frida_server === "running") {
    tags.push("Frida运行中");
  }
  return tags;
}

/** 构造 UI 元素的简短可读标题，用于工作台控件列表。 */
export function uiElementDisplayLabel(el: MobileUIElement): string {
  const parts = [el.text, el.content_desc, el.resource_id?.split("/").pop()].filter((s) => Boolean(s && s.trim()));
  if (parts.length > 0) return parts[0]!;
  return `[${el.class}] (${el.center_x}, ${el.center_y})`;
}

/** 解析逗号或换行分隔的后端域名输入框为去重小写列表。 */
export function parseCandidateDomainInput(raw: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const part of raw.split(/[\s,，\n]+/)) {
    const clean =
      part
        .trim()
        .toLowerCase()
        .replace(/^https?:\/\//, "")
        .split("/")[0]
        ?.split(":")[0] ?? "";
    if (!clean || seen.has(clean)) continue;
    seen.add(clean);
    out.push(clean);
  }
  return out;
}
