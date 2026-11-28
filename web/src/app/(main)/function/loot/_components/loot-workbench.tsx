"use client";

import * as React from "react";

import { useSearchParams } from "next/navigation";

import { TrophyIcon } from "lucide-react";

import { PageShell, SectionNav } from "@/components/page-sections";

import { CredentialsSection } from "./credentials-panel";
import { FootholdsSection } from "./footholds-panel";
import { LootAttemptsSection, LootLedgerSection } from "./loot-ledger-panel";

const SECTIONS = [
  { id: "ledger", label: "综合台账" },
  { id: "credentials", label: "凭据工作台" },
  { id: "footholds", label: "据点与隧道" },
  { id: "attempts", label: "账号尝试" },
] as const;

export type LootSectionId = (typeof SECTIONS)[number]["id"];

function isLootSectionId(v: string | null | undefined): v is LootSectionId {
  return Boolean(v && SECTIONS.some((s) => s.id === v));
}

function LootWorkbenchInner({ initialSection }: { initialSection?: LootSectionId }) {
  const searchParams = useSearchParams();
  const tabParam = searchParams.get("tab");
  const kindParam = searchParams.get("kind") ?? undefined;
  const [active, setActive] = React.useState<LootSectionId>(
    initialSection ?? (isLootSectionId(tabParam) ? tabParam : "ledger"),
  );
  const [initialKind, setInitialKind] = React.useState<string | undefined>(kindParam);
  const prevTabParam = React.useRef<string | null>(tabParam);

  React.useEffect(() => {
    setInitialKind(kindParam);
  }, [kindParam]);

  React.useEffect(() => {
    if (initialSection) return;
    if (isLootSectionId(tabParam)) {
      setActive(tabParam);
    } else if (prevTabParam.current && !tabParam) {
      setActive("ledger");
    }
    prevTabParam.current = tabParam;
  }, [initialSection, tabParam]);

  const handleSectionChange = React.useCallback(
    (id: string) => {
      const next = id as LootSectionId;
      setActive(next);
      if (!initialSection && typeof window !== "undefined") {
        const url = new URL(window.location.href);
        if (next === "ledger") url.searchParams.delete("tab");
        else url.searchParams.set("tab", next);
        window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`);
      }
    },
    [initialSection],
  );

  return (
    <PageShell
      nav={<SectionNav sections={SECTIONS} active={active} onChange={handleSectionChange} ariaLabel="战果工作台分区" />}
    >
      <div>
        <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
          <TrophyIcon className="size-5 text-muted-foreground" /> 战果与凭据工作台
        </h1>
        <p className="text-muted-foreground text-sm">
          统一检索战果台账与遗留物，并对在手凭据、据点与隧道进行存活核验或失效标记。
        </p>
      </div>

      {active === "ledger" && <LootLedgerSection initialKind={initialKind} />}
      {active === "credentials" && <CredentialsSection />}
      {active === "footholds" && <FootholdsSection />}
      {active === "attempts" && <LootAttemptsSection />}
    </PageShell>
  );
}

export function LootWorkbench({ initialSection }: { initialSection?: LootSectionId }) {
  return (
    <React.Suspense fallback={null}>
      <LootWorkbenchInner initialSection={initialSection} />
    </React.Suspense>
  );
}
