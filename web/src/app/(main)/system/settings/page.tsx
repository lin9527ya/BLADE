"use client";

import * as React from "react";

import Link from "next/link";

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
  const {
    trafficCapture,
    setTrafficCapture,
    agentTrafficBinding,
    setAgentTrafficBinding,
    webSearch,
    setWebSearch,
    backend,
    setBackend,
    braveKeySet,
    setBraveKeySet,
    braveKeyInput,
    setBraveKeyInput,
    tavilyKeySet,
    setTavilyKeySet,
    tavilyKeyInput,
    setTavilyKeyInput,
    savingTavilyKey,
    setSavingTavilyKey,
    proxyInput,
    setProxyInput,
    savingProxy,
    setSavingProxy,
    globalProxyInput,
    setGlobalProxyInput,
    savingGlobalProxy,
    setSavingGlobalProxy,
    testing,
    setTesting,
    loaded,
    setLoaded,
    section,
    setSection,
    savingTraffic,
    setSavingTraffic,
    savingWebSearch,
    setSavingWebSearch,
    savingPython,
    setSavingPython,
    savingKey,
    setSavingKey,
    pyInterp,
    setPyInterp,
    workers,
    setWorkers,
    savingWorkers,
    setSavingWorkers,
    tokenBudget,
    setTokenBudget,
    savingBudget,
    setSavingBudget,
    execCPU,
    setExecCPU,
    execMem,
    setExecMem,
    execProcs,
    setExecProcs,
    execMaxActive,
    setExecMaxActive,
    mcpConcurrent,
    setMcpConcurrent,
    mcpBreakerFails,
    setMcpBreakerFails,
    mcpBreakerCooldown,
    setMcpBreakerCooldown,
    savingLimits,
    setSavingLimits,
    intelKeyInput,
    setIntelKeyInput,
    intelKeySet,
    setIntelKeySet,
    intelEmail,
    setIntelEmail,
    intelBaseURL,
    setIntelBaseURL,
    intelProxy,
    setIntelProxy,
    intelQuota,
    setIntelQuota,
    intelUsed,
    setIntelUsed,
    intelBusy,
    setIntelBusy,
    intelTesting,
    setIntelTesting,
    intelTestMsg,
    setIntelTestMsg,
    intelRows,
    setIntelRows,
    polDefault,
    setPolDefault,
    polTech,
    setPolTech,
    polWrites,
    setPolWrites,
    polCustom,
    setPolCustom,
    savingPol,
    setSavingPol,
    injectPlanner,
    setInjectPlanner,
    injectWorker,
    setInjectWorker,
    noaCompaction,
    setNoaCompaction,
    scopeMode,
    setScopeMode,
    scopeModeEnv,
    setScopeModeEnv,
    scopeHours,
    setScopeHours,
    savingScope,
    setSavingScope,
    artifactLang,
    setArtifactLang,
    savingArtifactLang,
    setSavingArtifactLang,
    credBudget,
    setCredBudget,
    credBudgetAction,
    setCredBudgetAction,
    savingCredBudget,
    setSavingCredBudget,
    siteName,
    setSiteName,
    savingSiteName,
    setSavingSiteName,
    enforce,
    setEnforce,
    killReason,
    setKillReason,
    killBusy,
    setKillBusy,
    opsecKB,
    setOpsecKB,
    resumeBusy,
    setResumeBusy,
    sendMode,
    apply,
    savePolicyDefault,
    togglePolicyChip,
    loadIntelQueries,
    saveIntel,
    clearIntelKey,
    testIntel,
    saveLimits,
    saveWorkers,
    saveTokenBudget,
    savePython,
    detectPython,
    toggleTraffic,
    toggleInjectPlanner,
    saveScope,
    saveArtifactLang,
    saveCredBudget,
    saveSiteName,
    loadEnforcement,
    doKillSwitch,
    doResumeAll,
    toggleAgentTrafficBinding,
    toggleInjectWorker,
    toggleNoaCompaction,
    saveWebSearch,
    saveBraveKey,
    saveTavilyKey,
    saveProxy,
    saveGlobalProxy,
    runTest,
    braveNeedsKey,
  } = useSettingsState();

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
        <h1 className="text-xl font-semibold tracking-tight">系统配置</h1>
        <p className="text-muted-foreground text-sm">全局运行时开关</p>
      </div>

      {section === "basic" && <BasicSection s={s} />}

      {section === "network" && <NetworkSection s={s} />}

      {section === "search" && <SearchSection s={s} />}

      {section === "intel" && <IntelligenceSection s={s} />}

      {section === "policy" && <PolicySection s={s} />}

      {section === "runtime" && <RuntimeSection s={s} />}

      {section === "advanced" && <AdvancedSection s={s} />}
    </PageShell>
  );
}
