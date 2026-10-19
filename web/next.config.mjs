import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// 静态导出：`NEXT_EXPORT=1 next build` 产出纯静态目录到 web/out，可直接丢进
// nginx web 根目录运行。开发(next dev)不设该变量，保留 /api 反代与热更新。
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo：整站走 mock，无后端，无需 /api 反代。
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

// 版本号在构建期读出来注入，**刻意只注入这一个字段**。此前 app-config.ts 直接
// `import packageJson from "../../package.json"`，会把整份清单（全部依赖名 + 版本号）
// 打进浏览器 chunk（实测 76KB，dashboard 与 tasks/detail 首屏都背着）。那既浪费字节，
// 也等于把技术栈与可对 CVE 的版本号发给任何打开页面的人——与 app-config.ts 自己写的
// 「被扫到也认不出是什么」相矛盾。config 在构建期的 Node 里跑，读它不会进产物。
const pkg = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8"));

/** @type {import('next').NextConfig} */
const nextConfig = {
  // 只把版本号交给前端；package.json 的其余部分留在构建机。
  env: { NEXT_PUBLIC_APP_VERSION: pkg.version },
  // 避免父目录的 lockfile 影响根目录推断及资源路径生成。
  turbopack: {
    root: fileURLToPath(new URL(".", import.meta.url)),
    // 非 mock 构建：把 mock 层整个换成替身（stub.ts），6.7k 行演示数据不进产物。
    // 动态 import 只能让它降到"懒加载 chunk"（仍在 web/out 里）；要让文件根本不生成，
    // 得在解析阶段就换掉。mock 构建（NEXT_PUBLIC_MOCK=1）不加别名，真实现照旧。
    ...(isMock
      ? {}
      : {
          resolveAlias: {
            "@/lib/mock/handler": "./src/lib/mock/stub.ts",
            "@/lib/mock/report": "./src/lib/mock/stub.ts",
          },
        }),
  },
  reactCompiler: true,
  // 允许从局域网 IP 访问 dev 资源（HMR），按需增删。
  // dev 阶段放开任意 IPv4 来源访问 /_next/* 与 HMR（局域网 IP 变动也不受影响）。
  // 注意：Next 出于安全禁止裸 "*"，需用分段通配；"*.*.*.*" 匹配任意 IPv4。
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // 纯静态导出：无 Node 运行时；图片不经优化；每个路由产出 <route>/index.html。
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo：无后端，不需要 /api 反代。
          images: { unoptimized: true },
        }
      : {
          // 开发：把 /api/* 反代到 Go 后端（默认 :8787，可用 BLADE_API 覆盖）。
          async rewrites() {
            const backend = process.env.BLADE_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
