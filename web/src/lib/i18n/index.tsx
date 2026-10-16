"use client";

import * as React from "react";

import { PREFERENCE_DEFAULTS } from "@/lib/preferences/preferences-config";
import { persistPreference } from "@/lib/preferences/preferences-storage";

import { en } from "./en";

// 界面语言（P41）：中文是源文、英文是词典（见 en.ts 的说明）。
//
// 为什么不做成 i18next 那套（CSAI 用的是它）：本项目的界面**已经全是中文**，需要的是"把中文
// 翻成英文"的单向能力 + 一个开关，而不是一套从零组织文案的资源系统。这里 ~40 行就够：
//   · `t(key, zh)` —— en 有就用 en，没有就用传进来的中文原文（缺翻译不会露出键名）；
//   · 语言存在偏好 cookie 里（与主题/字体同一套机制，刷新与重启都记得住）；
//   · 切换即时生效（React context），并把 <html lang> 同步过去（读屏与浏览器翻译靠它）。

export type Lang = "zh" | "en";

export const LANGS: readonly Lang[] = ["zh", "en"];

export function isLang(v: string | undefined | null): v is Lang {
  return v === "zh" || v === "en";
}

type I18nValue = {
  lang: Lang;
  /** t(键, 中文原文)：en 缺这个键时返回中文原文——**不返回键名**。 */
  t: (key: string, zh: string, vars?: Record<string, string | number>) => string;
  setLang: (lang: Lang) => void;
};

const I18nContext = React.createContext<I18nValue | null>(null);

/** translate 是纯函数（provider 之外也能用：日志、非组件代码、测试）。 */
export function translate(lang: Lang, key: string, zh: string, vars?: Record<string, string | number>): string {
  let out = zh;
  if (lang === "en") {
    const hit = en[key];
    if (hit) out = hit;
  }
  if (vars) {
    for (const [k, v] of Object.entries(vars)) {
      out = out.replaceAll(`{${k}}`, String(v));
    }
  }
  return out;
}

export function I18nProvider({
  initialLang,
  children,
}: {
  initialLang?: Lang;
  children: React.ReactNode;
}) {
  const [lang, setLangState] = React.useState<Lang>(initialLang ?? PREFERENCE_DEFAULTS.language);

  // 首次挂载时从 cookie 读一次：静态导出没有服务端渲染这一步，language 只能在客户端落定。
  React.useEffect(() => {
    const cookie = document.cookie
      .split("; ")
      .find((c) => c.startsWith("language="));
    const value = cookie ? decodeURIComponent(cookie.split("=")[1] ?? "") : "";
    if (isLang(value) && value !== lang) setLangState(value);
    // 只在挂载时同步一次；之后以 store 为准（切语言写 cookie + setState）。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    document.documentElement.lang = lang === "en" ? "en" : "zh-CN";
  }, [lang]);

  const setLang = React.useCallback((next: Lang) => {
    setLangState(next);
    void persistPreference("language", next);
    document.documentElement.lang = next === "en" ? "en" : "zh-CN";
  }, []);

  const t = React.useCallback(
    (key: string, zh: string, vars?: Record<string, string | number>) => translate(lang, key, zh, vars),
    [lang],
  );

  const value = React.useMemo(() => ({ lang, t, setLang }), [lang, t, setLang]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

/** useI18n 在 provider 之外调用时回落到中文原文（不抛错：文案不该让页面崩）。 */
export function useI18n(): I18nValue {
  const ctx = React.useContext(I18nContext);
  if (ctx) return ctx;
  return {
    lang: "zh",
    t: (_key, zh, vars) => translate("zh", _key, zh, vars),
    setLang: () => {},
  };
}
