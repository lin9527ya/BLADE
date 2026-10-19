// 字体注册表：**只列真的能出货的字体**。
//
// 这里曾经有 18 种（Inter / Roboto / Lora / …），但它们全部 src 到同一个 Geist 文件——那是
// 离线构建时的临时 fallback，文件头写着「build 后恢复 registry.ts.bak」，而那个 .bak 早已不在
// 仓库里。结果是偏好面板在切、CSS 变量在变、字形纹丝不动：一个看起来能用、其实没接线的开关。
//
// 收成 geist 包自带的三族：零网络依赖、离线可构建，而且切换是**真的有效果**。
// 将来若要接回 Google Fonts，注意那需要构建期联网——这正是当初做 fallback 的原因。
//
// 配套：globals.css 里 `html[data-font=…]` 的规则也只保留这三项。删掉规则本身就是优雅降级
// ——一个指向已删字体的旧 cookie 不匹配任何规则，`--font-sans` 保持默认的 Geist。
import localFont from "next/font/local";

import { GeistPixelSquare } from "geist/font/pixel";

const geist = localFont({
  src: "../../../node_modules/geist/dist/fonts/geist-sans/Geist-Variable.woff2",
  variable: "--font-geist",
});
const geistMono = localFont({
  src: "../../../node_modules/geist/dist/fonts/geist-mono/GeistMono-Variable.woff2",
  variable: "--font-geist-mono",
});

export const fontRegistry = {
  geist: { label: "Geist", font: geist },
  geistMono: { label: "Geist Mono", font: geistMono },
  geistPixelSquare: { label: "Geist Pixel Square", font: GeistPixelSquare },
} as const;

export type FontKey = keyof typeof fontRegistry;

export const fontVars = (Object.values(fontRegistry) as Array<(typeof fontRegistry)[FontKey]>)
  .map((f) => f.font.variable)
  .join(" ");

export const fontOptions = (
  Object.entries(fontRegistry) as [FontKey, { label: string; font: { variable: string } }][]
).map(([key, f]) => ({
  key,
  label: f.label,
  variable: f.font.variable,
}));
