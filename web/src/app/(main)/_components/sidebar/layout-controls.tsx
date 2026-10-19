"use client";

import { Settings } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { type FontKey, fontOptions } from "@/lib/fonts/registry";
import { useI18n } from "@/lib/i18n";
import type { ContentLayout, NavbarStyle, SidebarCollapsible, SidebarVariant } from "@/lib/preferences/layout";
import {
  applyContentLayout,
  applyFont,
  applyNavbarStyle,
  applySidebarCollapsible,
  applySidebarVariant,
} from "@/lib/preferences/layout-utils";
import { PREFERENCE_DEFAULTS } from "@/lib/preferences/preferences-config";
import { persistPreference } from "@/lib/preferences/preferences-storage";
import { THEME_PRESET_OPTIONS, type ThemeMode, type ThemePreset } from "@/lib/preferences/theme";
import { applyThemePreset } from "@/lib/preferences/theme-utils";
import { usePreferencesStore } from "@/stores/preferences/preferences-provider";

export function LayoutControls() {
  const { t, lang, setLang } = useI18n();
  const themeMode = usePreferencesStore((s) => s.themeMode);
  const resolvedThemeMode = usePreferencesStore((s) => s.resolvedThemeMode);
  const setThemeMode = usePreferencesStore((s) => s.setThemeMode);
  const themePreset = usePreferencesStore((s) => s.themePreset);
  const setThemePreset = usePreferencesStore((s) => s.setThemePreset);
  const contentLayout = usePreferencesStore((s) => s.contentLayout);
  const setContentLayout = usePreferencesStore((s) => s.setContentLayout);
  const navbarStyle = usePreferencesStore((s) => s.navbarStyle);
  const setNavbarStyle = usePreferencesStore((s) => s.setNavbarStyle);
  const variant = usePreferencesStore((s) => s.sidebarVariant);
  const setSidebarVariant = usePreferencesStore((s) => s.setSidebarVariant);
  const collapsible = usePreferencesStore((s) => s.sidebarCollapsible);
  const setSidebarCollapsible = usePreferencesStore((s) => s.setSidebarCollapsible);
  const font = usePreferencesStore((s) => s.font);
  const setFont = usePreferencesStore((s) => s.setFont);

  const onThemePresetChange = (preset: ThemePreset) => {
    applyThemePreset(preset);
    setThemePreset(preset);
    void persistPreference("theme_preset", preset);
  };

  const onThemeModeChange = (mode: ThemeMode | "") => {
    if (!mode) return;
    setThemeMode(mode);
    void persistPreference("theme_mode", mode);
  };

  const onContentLayoutChange = (layout: ContentLayout | "") => {
    if (!layout) return;
    applyContentLayout(layout);
    setContentLayout(layout);
    void persistPreference("content_layout", layout);
  };

  const onNavbarStyleChange = (style: NavbarStyle | "") => {
    if (!style) return;
    applyNavbarStyle(style);
    setNavbarStyle(style);
    void persistPreference("navbar_style", style);
  };

  const onSidebarStyleChange = (value: SidebarVariant | "") => {
    if (!value) return;
    setSidebarVariant(value);
    applySidebarVariant(value);
    void persistPreference("sidebar_variant", value);
  };

  const onSidebarCollapseModeChange = (value: SidebarCollapsible | "") => {
    if (!value) return;
    setSidebarCollapsible(value);
    applySidebarCollapsible(value);
    void persistPreference("sidebar_collapsible", value);
  };

  const onFontChange = (value: FontKey | "") => {
    if (!value) return;
    applyFont(value);
    setFont(value);
    void persistPreference("font", value);
  };

  const handleRestore = () => {
    setLang(PREFERENCE_DEFAULTS.language);
    onThemePresetChange(PREFERENCE_DEFAULTS.theme_preset);
    onThemeModeChange(PREFERENCE_DEFAULTS.theme_mode);
    onContentLayoutChange(PREFERENCE_DEFAULTS.content_layout);
    onNavbarStyleChange(PREFERENCE_DEFAULTS.navbar_style);
    onSidebarStyleChange(PREFERENCE_DEFAULTS.sidebar_variant);
    onSidebarCollapseModeChange(PREFERENCE_DEFAULTS.sidebar_collapsible);
    onFontChange(PREFERENCE_DEFAULTS.font);
  };

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button size="icon">
          <Settings />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end">
        <div className="flex flex-col gap-5">
          <div className="space-y-1.5">
            <h4 className="font-medium text-sm leading-none">{t("prefs.title", "界面偏好")}</h4>
            <p className="text-muted-foreground text-xs">
              {t("prefs.desc", "调整界面外观与布局；改动即时生效，存在本机。")}
            </p>
          </div>
          <div className="space-y-3 **:data-[slot=toggle-group]:w-full **:data-[slot=toggle-group-item]:flex-1 **:data-[slot=toggle-group-item]:text-xs">
            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.language", "语言")}</Label>
              <ToggleGroup
                size="sm"
                spacing={0}
                variant="outline"
                type="single"
                value={lang}
                onValueChange={(v) => {
                  if (v === "zh" || v === "en") setLang(v);
                }}
              >
                <ToggleGroupItem value="zh" aria-label="中文">
                  {t("prefs.language.zh", "中文")}
                </ToggleGroupItem>
                <ToggleGroupItem value="en" aria-label="English">
                  {t("prefs.language.en", "English")}
                </ToggleGroupItem>
              </ToggleGroup>
              <p className="text-muted-foreground text-[11px] leading-relaxed">
                {t("prefs.language.note", "英文覆盖尚未逐页补齐：没翻到的地方显示中文原文（不会露出内部键名）。")}
              </p>
            </div>

            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.themePreset", "主题配色")}</Label>
              <Select value={themePreset} onValueChange={onThemePresetChange}>
                <SelectTrigger size="sm" className="w-full text-xs">
                  <SelectValue placeholder={t("prefs.themePreset.placeholder", "选择配色")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {THEME_PRESET_OPTIONS.map((preset) => (
                      <SelectItem key={preset.value} className="text-xs" value={preset.value}>
                        <span
                          className="size-2.5 rounded-full"
                          style={{
                            backgroundColor:
                              (resolvedThemeMode ?? "light") === "dark" ? preset.primary.dark : preset.primary.light,
                          }}
                        />
                        {preset.label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.fonts", "字体")}</Label>
              <Select value={font} onValueChange={onFontChange}>
                <SelectTrigger size="sm" className="w-full text-xs">
                  <SelectValue placeholder={t("prefs.fonts.placeholder", "选择字体")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {fontOptions.map((font) => (
                      <SelectItem key={font.key} className="text-xs" value={font.key}>
                        {font.label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.themeMode", "明暗模式")}</Label>
              <ToggleGroup
                size="sm"
                spacing={0}
                variant="outline"
                type="single"
                value={themeMode}
                onValueChange={onThemeModeChange}
              >
                <ToggleGroupItem value="light" aria-label={t("prefs.themeMode.light", "浅色")}>
                  {t("prefs.themeMode.light", "浅色")}
                </ToggleGroupItem>
                <ToggleGroupItem value="dark" aria-label={t("prefs.themeMode.dark", "深色")}>
                  {t("prefs.themeMode.dark", "深色")}
                </ToggleGroupItem>
                <ToggleGroupItem value="system" aria-label={t("prefs.themeMode.system", "跟随系统")}>
                  {t("prefs.themeMode.system", "跟随系统")}
                </ToggleGroupItem>
              </ToggleGroup>
            </div>

            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.pageLayout", "页面宽度")}</Label>
              <ToggleGroup
                size="sm"
                spacing={0}
                variant="outline"
                type="single"
                value={contentLayout}
                onValueChange={onContentLayoutChange}
              >
                <ToggleGroupItem value="centered" aria-label={t("prefs.pageLayout.centered", "居中")}>
                  {t("prefs.pageLayout.centered", "居中")}
                </ToggleGroupItem>
                <ToggleGroupItem value="full-width" aria-label={t("prefs.pageLayout.fullWidth", "通栏")}>
                  {t("prefs.pageLayout.fullWidth", "通栏")}
                </ToggleGroupItem>
              </ToggleGroup>
            </div>

            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.navbar", "顶栏行为")}</Label>
              <ToggleGroup
                size="sm"
                spacing={0}
                variant="outline"
                type="single"
                value={navbarStyle}
                onValueChange={onNavbarStyleChange}
              >
                <ToggleGroupItem value="sticky" aria-label={t("prefs.navbar.sticky", "吸顶")}>
                  {t("prefs.navbar.sticky", "吸顶")}
                </ToggleGroupItem>
                <ToggleGroupItem value="scroll" aria-label={t("prefs.navbar.scroll", "随页滚动")}>
                  {t("prefs.navbar.scroll", "随页滚动")}
                </ToggleGroupItem>
              </ToggleGroup>
            </div>

            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.sidebarStyle", "侧栏样式")}</Label>
              <ToggleGroup
                size="sm"
                spacing={0}
                variant="outline"
                type="single"
                value={variant}
                onValueChange={onSidebarStyleChange}
              >
                <ToggleGroupItem value="inset" aria-label={t("prefs.sidebarStyle.inset", "内嵌")}>
                  {t("prefs.sidebarStyle.inset", "内嵌")}
                </ToggleGroupItem>
                <ToggleGroupItem value="sidebar" aria-label={t("prefs.sidebarStyle.sidebar", "经典")}>
                  {t("prefs.sidebarStyle.sidebar", "经典")}
                </ToggleGroupItem>
                <ToggleGroupItem value="floating" aria-label={t("prefs.sidebarStyle.floating", "浮动")}>
                  {t("prefs.sidebarStyle.floating", "浮动")}
                </ToggleGroupItem>
              </ToggleGroup>
            </div>

            <div className="space-y-1">
              <Label className="font-medium text-xs">{t("prefs.collapseMode", "侧栏折叠方式")}</Label>
              <ToggleGroup
                size="sm"
                spacing={0}
                variant="outline"
                type="single"
                value={collapsible}
                onValueChange={onSidebarCollapseModeChange}
              >
                <ToggleGroupItem value="icon" aria-label={t("prefs.collapseMode.icon", "仅图标")}>
                  {t("prefs.collapseMode.icon", "仅图标")}
                </ToggleGroupItem>
                <ToggleGroupItem value="offcanvas" aria-label={t("prefs.collapseMode.offcanvas", "抽屉")}>
                  {t("prefs.collapseMode.offcanvas", "抽屉")}
                </ToggleGroupItem>
              </ToggleGroup>
            </div>

            <Button type="button" size="sm" variant="outline" className="w-full text-xs" onClick={handleRestore}>
              {t("prefs.restore", "恢复默认")}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
