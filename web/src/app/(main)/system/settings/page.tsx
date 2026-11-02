"use client";

import { PageShell, SectionNav } from "@/components/page-sections";

import { AdvancedSection } from "./_components/advanced-section";
import { BasicSection } from "./_components/basic-section";
import { IntelligenceSection } from "./_components/intel-section";
import { NetworkSection } from "./_components/network-section";
import { PolicySection } from "./_components/policy-section";
import { RuntimeSection } from "./_components/runtime-section";
import { SearchSection } from "./_components/search-section";
import { SECTIONS, type SectionId, useSettingsState } from "./_components/use-settings-state";

// 预计可见度的排序权重（高 → 低）。字符串比较会把"中"排到"低"前面，那正是这张清单
// 最该避免的误导——人扫一眼顺序就形成了印象。

export default function SystemSettingsPage() {
  const s = useSettingsState();
  const { section, setSection } = useSettingsState();

  return (
    <PageShell
      nav={
        <SectionNav
          sections={SECTIONS}
          active={section}
          onChange={(id) => setSection(id as SectionId)}
          ariaLabel="设置分区"
        />
      }
    >
      <div>
        <h1 className="font-semibold text-xl tracking-tight">系统配置</h1>
        <p className="text-muted-foreground text-sm">全局运行时开关</p>
      </div>

      {section === "basic" && <BasicSection />}

      {section === "network" && <NetworkSection s={s} />}

      {section === "search" && <SearchSection s={s} />}

      {section === "intel" && <IntelligenceSection s={s} />}

      {section === "policy" && <PolicySection s={s} />}

      {section === "runtime" && <RuntimeSection s={s} />}

      {section === "advanced" && <AdvancedSection s={s} />}
    </PageShell>
  );
}
