"use client";

import * as React from "react";

import { useSearchParams } from "next/navigation";

import { PageShell, SectionNav } from "@/components/page-sections";
import { Button } from "@/components/ui/button";

import { AdvancedSection } from "./advanced-section";
import { ApiTokensSection } from "./api-tokens-section";
import { BasicSection } from "./basic-section";
import { IntelligenceSection } from "./intel-section";
import { NetworkSection } from "./network-section";
import { PolicySection } from "./policy-section";
import { RuntimeSection } from "./runtime-section";
import { SearchSection } from "./search-section";
import { SECTIONS, type SectionId, useSettingsState } from "./use-settings-state";

function isSectionId(v: string | null | undefined): v is SectionId {
  return Boolean(v && SECTIONS.some((sec) => sec.id === v));
}

function SettingsWorkbenchInner({ initialSection }: { initialSection?: SectionId }) {
  const s = useSettingsState(initialSection);
  const { section, setSection } = s;
  const searchParams = useSearchParams();
  const sectionParam = searchParams.get("section");
  const prevSectionParam = React.useRef<string | null>(sectionParam);

  React.useEffect(() => {
    if (initialSection) return;
    if (isSectionId(sectionParam)) {
      setSection(sectionParam);
    } else if (prevSectionParam.current && !sectionParam) {
      setSection("basic");
    }
    prevSectionParam.current = sectionParam;
  }, [initialSection, sectionParam, setSection]);

  const handleSectionChange = React.useCallback(
    (id: string) => {
      const next = id as SectionId;
      setSection(next);
      if (!initialSection && typeof window !== "undefined") {
        const url = new URL(window.location.href);
        if (next === "basic") url.searchParams.delete("section");
        else url.searchParams.set("section", next);
        window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`);
      }
    },
    [initialSection, setSection],
  );

  const renderSectionContent = () => {
    if (section === "tokens") return <ApiTokensSection />;
    if (!s.loaded) {
      return (
        <div role={s.loadError ? "alert" : "status"} className="space-y-3 rounded-md border p-4 text-sm">
          <p>{s.loadError ? `设置读取失败：${s.loadError}。尚未加载有效配置，不能保存默认值。` : "正在读取设置…"}</p>
          {s.loadError && (
            <Button variant="outline" disabled={s.loadingSettings} onClick={() => void s.loadSettings()}>
              重新读取
            </Button>
          )}
        </div>
      );
    }
    return (
      <>
        {s.hasUnsavedChanges && (
          <p className="text-muted-foreground text-sm">有未保存的草稿；保存其他分区不会覆盖它们。</p>
        )}
        {section === "basic" && <BasicSection />}
        {section === "network" && <NetworkSection s={s} />}
        {section === "search" && <SearchSection s={s} />}
        {section === "intel" && <IntelligenceSection s={s} />}
        {section === "policy" && <PolicySection s={s} />}
        {section === "runtime" && <RuntimeSection s={s} />}
        {section === "advanced" && <AdvancedSection s={s} />}
      </>
    );
  };

  return (
    <PageShell
      nav={<SectionNav sections={SECTIONS} active={section} onChange={handleSectionChange} ariaLabel="设置分区" />}
    >
      <div>
        <h1 className="font-semibold text-xl tracking-tight">系统配置</h1>
        <p className="text-muted-foreground text-sm">全局运行时开关与访问令牌</p>
      </div>
      {renderSectionContent()}
    </PageShell>
  );
}

export function SettingsWorkbench({ initialSection }: { initialSection?: SectionId }) {
  return (
    <React.Suspense fallback={null}>
      <SettingsWorkbenchInner initialSection={initialSection} />
    </React.Suspense>
  );
}
