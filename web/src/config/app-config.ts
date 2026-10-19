// 应用标识。
//
// **名字刻意中性**：这个平台的用途（授权渗透测试控制台）不该写在标题与描述里——被扫到一次
// 就等于告诉了对方"这台机器上有攻击平台"，那比"不知道是什么"糟得多。真正的隐身靠的是
// 不暴露端口（见 deploy/README.md），但标题是最便宜的补漏。
//
// 想换成别的名字：改这里的 name / meta.title / meta.description 后重新构建
// （运行时还能用设置 site_name 单独覆盖 document.title）。
//
// 版本号由 next.config.mjs 在构建期从 package.json 注入——**只注入这一个字段**。
// 不要在这里 `import packageJson from "../../package.json"`：那会把整份依赖清单
// （含各库版本号）打进浏览器 chunk，与上面「不写用途」的目标相反。
const APP_VERSION = process.env.NEXT_PUBLIC_APP_VERSION ?? "dev";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "BLADE",
  version: APP_VERSION,
  copyright: `© ${currentYear}, BLADE.`,
  meta: {
    // 不带用途描述：扫描器抓到 <title> 时看不出这是什么。
    title: "BLADE",
    description: "BLADE console",
  },
};
