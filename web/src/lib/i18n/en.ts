// 英文词典（P41 中英切换）。
//
// **只装英文**：中文是源文——界面里那些中文串本身就是 zh 文案，`t(key, "中文")` 的第二个参数
// 就是它。这样有两个好处：
//   ① 不必维护一份与代码并行的中文表（那种表会漂移成"页面上是 A、表里是 B"）；
//   ② 英文缺条目时**回落显示中文**，而不是把 `prefs.themePreset` 这种内部键名印到界面上
//      （缺翻译是个已知的中间状态，不该变成可见的故障）。
//
// 键按区域分组，扁平写：`区域.名字`。加文案时：先在组件里写字面中文，再把英文补到这里。

export const en: Record<string, string> = {
  // ---- 界面偏好（右上角 Preferences）----
  "prefs.title": "Preferences",
  "prefs.desc": "Customize the look and layout of the console. Changes apply instantly and are stored locally.",
  "prefs.language": "Language",
  "prefs.language.zh": "中文",
  "prefs.language.en": "English",
  "prefs.themePreset": "Theme preset",
  "prefs.themePreset.placeholder": "Pick a preset",
  "prefs.fonts": "Fonts",
  "prefs.fonts.placeholder": "Pick a font",
  "prefs.themeMode": "Theme mode",
  "prefs.themeMode.light": "Light",
  "prefs.themeMode.dark": "Dark",
  "prefs.themeMode.system": "System",
  "prefs.pageLayout": "Page layout",
  "prefs.pageLayout.centered": "Centered",
  "prefs.pageLayout.fullWidth": "Full width",
  "prefs.navbar": "Navbar behavior",
  "prefs.navbar.sticky": "Sticky",
  "prefs.navbar.scroll": "Scroll",
  "prefs.sidebarStyle": "Sidebar style",
  "prefs.sidebarStyle.inset": "Inset",
  "prefs.sidebarStyle.sidebar": "Sidebar",
  "prefs.sidebarStyle.floating": "Floating",
  "prefs.collapseMode": "Sidebar collapse",
  "prefs.collapseMode.icon": "Icons only",
  "prefs.collapseMode.offcanvas": "Off-canvas",
  "prefs.restore": "Restore defaults",
  "prefs.language.switchToEn": "Switch to English",
  "prefs.language.switchToZh": "切换到中文",
  "prefs.language.note":
    "English coverage is still being filled in page by page — untranslated areas show the Chinese source text (never internal keys).",

  // ---- 侧栏导航 ----
  "nav.group.1": "Operations",
  "nav.group.2": "System",
  "nav.dashboard": "Dashboard",
  "nav.chat": "Chat",
  "nav.tasks": "Tasks",
  "nav.campaigns": "Campaigns",
  "nav.findings": "Findings",
  "nav.traffic": "Traffic",
  "nav.commands": "Executions",
  "nav.llm-records": "LLM recordings",
  "nav.assets": "Assets",
  "nav.sync": "Asset sync",
  "nav.workspace": "Workspace",
  "nav.capabilities": "Capabilities",
  "nav.tools": "Tools",
  "nav.skills": "Skills",
  "nav.mcp": "MCP",
  "nav.models-agents": "Models & agents",
  "nav.llm": "LLM profiles",
  "nav.agents": "Agents",
  "nav.intercept": "Intercept & approvals",
  "nav.notify": "Notifications",
  "nav.logs": "Logs",
  "nav.storage": "Storage",
  "nav.settings": "Settings",

  // ---- 侧栏其它 ----
  "sidebar.search": "Search",
  "sidebar.searchMenu": "Search pages and features… (Ctrl/⌘ + J anywhere)",
  "sidebar.noResults": "No matching page",
  "sidebar.toggle": "Toggle sidebar",
};
