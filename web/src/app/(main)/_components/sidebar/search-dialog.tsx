"use client";

import * as React from "react";

import { useRouter } from "next/navigation";

import { BookOpenIcon, BugIcon, Clock3Icon, NetworkIcon, SearchIcon, ShieldAlertIcon, TargetIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import type { GlobalSearchHit } from "@/lib/types";
import type { NavMainItem } from "@/navigation/sidebar/sidebar-items";
import { sidebarItems } from "@/navigation/sidebar/sidebar-items";

type SearchItem = {
  id: string;
  group: string;
  label: string;
  subtitle?: string;
  url: string;
  icon?: NavMainItem["icon"];
  disabled?: boolean;
  newTab?: boolean;
};

type RecentItem = {
  title: string;
  subtitle?: string;
  url: string;
  kind: string;
};

const RECENT_KEY = "blade.recent";
const REMOTE_GROUPS: {
  key: keyof RemoteResults;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
}[] = [
  { key: "tasks", label: "任务", icon: TargetIcon },
  { key: "findings", label: "发现", icon: BugIcon },
  { key: "assets", label: "资产", icon: NetworkIcon },
  { key: "cves", label: "CVE", icon: ShieldAlertIcon },
  { key: "playbooks", label: "打法", icon: BookOpenIcon },
];

type RemoteResults = {
  tasks: GlobalSearchHit[];
  findings: GlobalSearchHit[];
  assets: GlobalSearchHit[];
  cves: GlobalSearchHit[];
  playbooks: GlobalSearchHit[];
};

const EMPTY_REMOTE: RemoteResults = {
  tasks: [],
  findings: [],
  assets: [],
  cves: [],
  playbooks: [],
};

const sidebarGroupLabels = new Set(sidebarItems.flatMap((group) => (group.label ? [group.label] : [])));

function getSubItemGroup(groupLabel: string | undefined, itemTitle: string) {
  return sidebarGroupLabels.has(itemTitle) ? (groupLabel ?? "Other") : itemTitle;
}

// 原始（中文）结构：分组判定的逻辑跑在它上面——那一套是拿"标题恰好等于组名"来判父子关系的
// （上游模板的写法），翻过再比会失配。翻译只发生在展示层。
const rawSearchItems: SearchItem[] = sidebarItems.flatMap((group) =>
  group.items.flatMap((item) => {
    if (item.subItems) {
      return item.subItems.map((sub) => ({
        id: sub.id,
        group: getSubItemGroup(group.label, item.title),
        label: sub.title,
        url: sub.url,
        icon: item.icon,
        disabled: sub.disabled,
        newTab: sub.newTab,
      }));
    }
    return [
      {
        id: item.id,
        group: group.label ?? "Other",
        label: item.title,
        url: item.url,
        icon: item.icon,
        disabled: item.disabled,
        newTab: item.newTab,
      },
    ];
  }),
);

function getAvailableItems(items: SearchItem[]) {
  return items.filter((item) => !item.disabled && !item.url.includes("coming-soon"));
}

const rawRecommendations = getAvailableItems(rawSearchItems);

function groupBy(items: SearchItem[]) {
  const groups = [...new Set(items.map((item) => item.group))];
  return groups.map((group) => ({
    group,
    items: items.filter((item) => item.group === group),
  }));
}

function readRecent(): RecentItem[] {
  const raw = getLocalStorageValue(RECENT_KEY);
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter(
        (item): item is RecentItem =>
          typeof item === "object" &&
          item !== null &&
          typeof (item as RecentItem).title === "string" &&
          typeof (item as RecentItem).url === "string",
      )
      .slice(0, 10);
  } catch {
    return [];
  }
}

function rememberRecent(item: RecentItem) {
  const next = [item, ...readRecent().filter((current) => current.url !== item.url)].slice(0, 10);
  setLocalStorageValue(RECENT_KEY, JSON.stringify(next));
}

export function SearchDialog() {
  const { t } = useI18n();
  const [open, setOpen] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const [remote, setRemote] = React.useState<RemoteResults>(EMPTY_REMOTE);
  const [loading, setLoading] = React.useState(false);
  const [recent, setRecent] = React.useState<RecentItem[]>([]);
  const router = useRouter();
  const searchItems = React.useMemo(
    () =>
      rawSearchItems.map((item) => ({
        ...item,
        label: t(`nav.${item.id}`, item.label),
      })),
    [t],
  );
  const recommendations = React.useMemo(
    () =>
      rawRecommendations.map((item) => ({
        ...item,
        label: t(`nav.${item.id}`, item.label),
      })),
    [t],
  );

  React.useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === "j" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen((prev) => !prev);
      }
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, []);

  React.useEffect(() => {
    if (!open) return;
    setRecent(readRecent());
  }, [open]);

  const trimmed = query.trim();
  React.useEffect(() => {
    if (!open || trimmed.length === 0) {
      setRemote(EMPTY_REMOTE);
      setLoading(false);
      return;
    }
    let alive = true;
    setLoading(true);
    const timer = window.setTimeout(() => {
      api
        .globalSearch(trimmed, 5)
        .then((result) => {
          if (!alive) return;
          setRemote(result);
        })
        .catch(() => {
          if (!alive) return;
          setRemote(EMPTY_REMOTE);
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    }, 200);
    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
  }, [open, trimmed]);

  const handleOpenChange = (value: boolean) => {
    setOpen(value);
    if (!value) {
      setQuery("");
      setRemote(EMPTY_REMOTE);
      setLoading(false);
    }
  };

  const navigate = (url: string, newTab?: boolean) => {
    handleOpenChange(false);
    if (newTab) {
      window.open(url, "_blank", "noopener,noreferrer");
    } else {
      router.push(url);
    }
  };

  const handleSelect = (item: SearchItem) => {
    if (item.disabled) return;
    rememberRecent({
      title: item.label,
      subtitle: item.subtitle,
      url: item.url,
      kind: "page",
    });
    navigate(item.url, item.newTab);
  };

  const handleRemoteSelect = (kind: string, item: GlobalSearchHit) => {
    rememberRecent({
      title: item.title,
      subtitle: item.subtitle,
      url: item.url,
      kind,
    });
    navigate(item.url);
  };

  const localPages = React.useMemo(() => {
    if (!trimmed) return [];
    const needle = trimmed.toLowerCase();
    return searchItems.filter(
      (item) =>
        !item.disabled && (item.label.toLowerCase().includes(needle) || item.group.toLowerCase().includes(needle)),
    );
  }, [searchItems, trimmed]);

  const remoteGroups = REMOTE_GROUPS.map((config) => ({
    ...config,
    items: remote[config.key] ?? [],
  })).filter((group) => group.items.length > 0);

  const hasResults = localPages.length > 0 || remoteGroups.length > 0;

  const renderGroups = (items: SearchItem[]) =>
    groupBy(items).map(({ group, items: groupItems }, index) => (
      <React.Fragment key={group}>
        {index > 0 && <CommandSeparator />}
        <CommandGroup heading={group}>
          {groupItems.map((item) => (
            <CommandItem
              disabled={item.disabled}
              key={`${group}-${item.id}`}
              value={`page ${group} ${item.label}`}
              onSelect={() => handleSelect(item)}
            >
              <span className="flex min-w-0 items-center gap-2">
                {item.icon && <item.icon />}
                <span className="truncate">{item.label}</span>
              </span>
            </CommandItem>
          ))}
        </CommandGroup>
      </React.Fragment>
    ));

  return (
    <>
      <Button
        onClick={() => handleOpenChange(true)}
        variant="link"
        className="px-0! font-normal text-muted-foreground hover:no-underline"
      >
        <SearchIcon data-icon="inline-start" />
        Search
        <kbd className="inline-flex h-5 select-none items-center gap-1 rounded border bg-muted px-1.5 font-medium text-[10px]">
          <span className="text-xs">⌘</span>J
        </kbd>
      </Button>
      <CommandDialog open={open} onOpenChange={handleOpenChange}>
        <Command shouldFilter={false}>
          <CommandInput
            placeholder={t("sidebar.searchMenu", "搜索任务、发现、资产、CVE、打法…（Ctrl/⌘ + J 随时唤起）")}
            value={query}
            onValueChange={setQuery}
          />
          <CommandList>
            {trimmed && !hasResults && !loading ? (
              <CommandEmpty>{t("sidebar.noResults", "没有匹配的结果")}</CommandEmpty>
            ) : null}
            {trimmed ? (
              <>
                {localPages.length > 0 && renderGroups(localPages)}
                {remoteGroups.map((group, index) => {
                  const Icon = group.icon;
                  return (
                    <React.Fragment key={group.key}>
                      {(index > 0 || localPages.length > 0) && <CommandSeparator />}
                      <CommandGroup heading={group.label}>
                        {group.items.map((item) => (
                          <CommandItem
                            key={`${group.key}-${item.id}`}
                            value={`${group.key} ${item.title} ${item.subtitle ?? ""}`}
                            onSelect={() => handleRemoteSelect(group.key, item)}
                          >
                            <span className="flex min-w-0 items-start gap-2">
                              <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                              <span className="min-w-0">
                                <span className="block truncate">{item.title}</span>
                                {item.subtitle ? (
                                  <span className="block truncate text-muted-foreground text-xs">{item.subtitle}</span>
                                ) : null}
                              </span>
                            </span>
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    </React.Fragment>
                  );
                })}
                {loading && (
                  <div className="flex items-center justify-center gap-2 py-3 text-muted-foreground text-xs">
                    <Spinner className="size-3.5" /> 检索中…
                  </div>
                )}
              </>
            ) : (
              <>
                {recent.length > 0 && (
                  <>
                    <CommandGroup heading="最近访问">
                      {recent.map((item) => (
                        <CommandItem
                          key={`recent-${item.url}`}
                          value={`recent ${item.title} ${item.url}`}
                          onSelect={() => {
                            rememberRecent(item);
                            navigate(item.url);
                          }}
                        >
                          <Clock3Icon className="size-4 text-muted-foreground" />
                          <span className="flex min-w-0 flex-col">
                            <span className="truncate">{item.title}</span>
                            {item.subtitle ? (
                              <span className="truncate text-muted-foreground text-xs">{item.subtitle}</span>
                            ) : null}
                          </span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                    <CommandSeparator />
                  </>
                )}
                {renderGroups(recommendations)}
              </>
            )}
          </CommandList>
        </Command>
      </CommandDialog>
    </>
  );
}
