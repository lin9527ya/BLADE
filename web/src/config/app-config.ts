// 应用标识。
//
// **名字刻意中性**：这个平台的用途（授权渗透测试控制台）不该写在标题与描述里——被扫到一次
// 就等于告诉了对方"这台机器上有攻击平台"，那比"不知道是什么"糟得多。真正的隐身靠的是
// 不暴露端口（见 deploy/README.md），但标题是最便宜的补漏。
//
// 想换成别的名字：改这里的 name / meta.title / meta.description 后重新构建
// （运行时还能用设置 site_name 单独覆盖 document.title）。
import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "BLADE",
  version: packageJson.version,
  copyright: `© ${currentYear}, BLADE.`,
  meta: {
    // 不带用途描述：扫描器抓到 <title> 时看不出这是什么。
    title: "BLADE",
    description: "BLADE console",
  },
};
