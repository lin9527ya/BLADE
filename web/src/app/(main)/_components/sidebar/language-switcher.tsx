"use client";

import { Languages } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";

// 语言开关（P41）：一个按钮在中/英之间切。
//
// 放在顶栏而不是只藏在「界面偏好」里：它是"我现在要看什么语言"的选择，属于最常用的一档；
// 偏好面板里那一行仍在（两处都能改，状态同一个）。
export function LanguageSwitcher() {
  const { lang, setLang, t } = useI18n();
  const next = lang === "zh" ? "en" : "zh";
  return (
    <Button
      size="icon"
      onClick={() => setLang(next)}
      aria-label={t("prefs.language", "语言")}
      title={
        lang === "zh" ? t("prefs.language.switchToEn", "切换到 English") : t("prefs.language.switchToZh", "切换到中文")
      }
    >
      <span className="flex items-center gap-1">
        <Languages className="size-4" />
        <span className="text-[10px] font-medium leading-none">{lang === "zh" ? "中" : "EN"}</span>
      </span>
    </Button>
  );
}
