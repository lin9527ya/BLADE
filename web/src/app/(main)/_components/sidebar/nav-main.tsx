"use client";

import * as React from "react";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { ChevronRight } from "lucide-react";

import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { cn } from "@/lib/utils";
import type {
  NavBadge,
  NavGroup,
  NavMainItem,
  NavMainLinkItem,
  NavMainParentItem,
} from "@/navigation/sidebar/sidebar-items";

const SIDEBAR_OPEN_STORAGE_KEY = "blade.sidebar.open";

interface NavMainProps {
  readonly items: readonly NavGroup[];
}
interface NavItemProps {
  readonly item: NavMainItem;
  readonly isItemActive: (item: NavMainItem) => boolean;
  readonly isSubItemActive: (url: string, matchUrls?: readonly string[]) => boolean;
  readonly isSubmenuOpen: (item: NavMainParentItem) => boolean;
  readonly storedOpen?: boolean;
  readonly onToggleOpen: (id: string, open: boolean) => void;
}

interface NavLinkItemProps {
  readonly item: NavMainLinkItem;
  readonly isActive: boolean;
  readonly showIconFallback: boolean;
}

interface NavLinkIconProps {
  readonly item: NavMainLinkItem;
  readonly showFallback: boolean;
}

interface NavDropdownItemProps {
  readonly item: NavMainParentItem;
  readonly isActive: boolean;
  readonly isSubItemActive: (url: string, matchUrls?: readonly string[]) => boolean;
}

interface NavCollapsibleItemProps {
  readonly item: NavMainParentItem;
  readonly isActive: boolean;
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly isSubItemActive: (url: string, matchUrls?: readonly string[]) => boolean;
}

function CollapsedIconFallback({ title }: { title: string }) {
  return (
    <span className="flex size-4 shrink-0 items-center justify-center rounded-xs font-medium text-[10px] outline">
      {title.slice(0, 1)}
    </span>
  );
}

function hasSubItems(item: NavMainItem): item is NavMainParentItem {
  return Boolean(item.subItems?.length);
}

function matchesTargetPath(path: string, url: string, matchUrls?: readonly string[]): boolean {
  const candidates = matchUrls && matchUrls.length > 0 ? matchUrls : [url];
  return candidates.some((prefix) => path === prefix || path.startsWith(`${prefix}/`));
}

function readStoredOpenState(): Record<string, boolean> {
  const raw = getLocalStorageValue(SIDEBAR_OPEN_STORAGE_KEY);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object") return {};
    const out: Record<string, boolean> = {};
    for (const [k, v] of Object.entries(parsed)) {
      if (typeof v === "boolean") out[k] = v;
    }
    return out;
  } catch {
    return {};
  }
}

export function NavMain({ items }: NavMainProps) {
  const { t } = useI18n();
  const [openPrefs, setOpenPrefs] = React.useState<Record<string, boolean>>({});
  const [prefsHydrated, setPrefsHydrated] = React.useState(false);

  React.useEffect(() => {
    setOpenPrefs(readStoredOpenState());
    setPrefsHydrated(true);
  }, []);

  const handleToggleOpen = React.useCallback((id: string, open: boolean) => {
    setOpenPrefs((prev) => {
      const next = { ...prev, [id]: open };
      setLocalStorageValue(SIDEBAR_OPEN_STORAGE_KEY, JSON.stringify(next));
      return next;
    });
  }, []);

  const localized = React.useMemo(
    () =>
      items.map((group) => ({
        ...group,
        label: group.label ? t(`nav.group.${group.id}`, group.label) : group.label,
        items: group.items.map((item) =>
          item.subItems
            ? {
                ...item,
                title: t(`nav.${item.id}`, item.title),
                subItems: item.subItems.map((sub) => ({ ...sub, title: t(`nav.${sub.id}`, sub.title) })),
              }
            : { ...item, title: t(`nav.${item.id}`, item.title) },
        ),
      })),
    [items, t],
  );
  const rawPath = usePathname();
  const path = (() => {
    try {
      return decodeURIComponent(rawPath);
    } catch {
      return rawPath;
    }
  })();

  const isItemActive = (item: NavMainItem) => {
    if (hasSubItems(item)) {
      return item.subItems.some((sub) => matchesTargetPath(path, sub.url, sub.matchUrls));
    }
    return matchesTargetPath(path, item.url, item.matchUrls);
  };

  const isSubItemActive = (url: string, matchUrls?: readonly string[]) => {
    return matchesTargetPath(path, url, matchUrls);
  };

  const isSubmenuOpen = (item: NavMainParentItem) => {
    return item.subItems.some((sub) => matchesTargetPath(path, sub.url, sub.matchUrls));
  };

  return (
    <>
      {localized.map((group) => (
        <SidebarGroup key={group.id}>
          {group.label && (
            <SidebarGroupLabel className="group-data-[collapsible=icon]:pointer-events-none">
              {group.label}
            </SidebarGroupLabel>
          )}
          <SidebarGroupContent>
            <SidebarMenu>
              {group.items.map((item) => (
                <NavItem
                  key={item.id}
                  item={item}
                  isItemActive={isItemActive}
                  isSubItemActive={isSubItemActive}
                  isSubmenuOpen={isSubmenuOpen}
                  storedOpen={prefsHydrated ? openPrefs[item.id] : undefined}
                  onToggleOpen={handleToggleOpen}
                />
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      ))}
    </>
  );
}

function NavItem({ item, isItemActive, isSubItemActive, isSubmenuOpen, storedOpen, onToggleOpen }: NavItemProps) {
  const { state, isMobile } = useSidebar();
  const isCollapsedDesktop = state === "collapsed" && !isMobile;

  if (!hasSubItems(item)) {
    return <NavLinkItem item={item} isActive={isItemActive(item)} showIconFallback={isCollapsedDesktop} />;
  }

  if (isCollapsedDesktop) {
    return <NavDropdownItem item={item} isActive={isItemActive(item)} isSubItemActive={isSubItemActive} />;
  }

  const activeRouteInMenu = isSubmenuOpen(item);
  const open = activeRouteInMenu || (storedOpen ?? false);

  return (
    <NavCollapsibleItem
      item={item}
      isActive={isItemActive(item)}
      open={open}
      onOpenChange={(next) => onToggleOpen(item.id, next)}
      isSubItemActive={isSubItemActive}
    />
  );
}

// InterceptPendingBadge polls /api/intercept/pending every 5 s and shows an alert
// badge both when expanded (count pill) and collapsed to icon rail (corner dot).
function InterceptPendingBadge() {
  const [count, setCount] = React.useState(0);
  usePolling(
    async (live) => {
      try {
        const nextCount = await api.interceptPendingCount();
        if (live()) setCount(nextCount);
      } catch {
        /* ignore */
      }
    },
    5000,
    [],
  );
  if (count === 0) return null;
  return (
    <>
      <SidebarMenuBadge className="flex h-4.5 min-w-[18px] items-center justify-center rounded-full bg-amber-500 px-1 font-semibold text-[10px] text-white leading-none shadow-sm ring-1 ring-amber-400/40">
        {count > 99 ? "99+" : count}
      </SidebarMenuBadge>
      <span
        aria-hidden="true"
        title={`${count} 项待审批`}
        className="pointer-events-none absolute top-1.5 right-1.5 hidden size-2 rounded-full bg-amber-500 ring-2 ring-sidebar group-data-[collapsible=icon]:block"
      />
    </>
  );
}

function NavLinkItem({ item, isActive, showIconFallback }: NavLinkItemProps) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild aria-disabled={item.disabled} tooltip={item.title} isActive={isActive}>
        <Link
          prefetch={false}
          href={item.url}
          target={item.newTab ? "_blank" : undefined}
          rel={item.newTab ? "noreferrer" : undefined}
        >
          <NavLinkIcon item={item} showFallback={showIconFallback} />
          <span>{item.title}</span>
        </Link>
      </SidebarMenuButton>
      {item.liveBadge === "intercept_pending" ? <InterceptPendingBadge /> : <NavItemBadge badge={item.badge} />}
    </SidebarMenuItem>
  );
}

function NavLinkIcon({ item, showFallback }: NavLinkIconProps) {
  const Icon = item.icon;

  if (Icon) {
    return <Icon />;
  }

  if (showFallback) {
    return <CollapsedIconFallback title={item.title} />;
  }

  return null;
}

function NavDropdownItem({ item, isActive, isSubItemActive }: NavDropdownItemProps) {
  const Icon = item.icon;

  return (
    <SidebarMenuItem>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <SidebarMenuButton tooltip={item.title} isActive={isActive} disabled={item.disabled}>
            {Icon ? <Icon /> : <CollapsedIconFallback title={item.title} />}
            <span>{item.title}</span>
          </SidebarMenuButton>
        </DropdownMenuTrigger>

        <DropdownMenuContent side="right" align="start" sideOffset={12} className="w-48">
          <DropdownMenuGroup>
            {item.subItems.map((subItem) => {
              const SubIcon = subItem.icon;

              return (
                <DropdownMenuItem key={subItem.id} asChild disabled={subItem.disabled}>
                  <Link
                    prefetch={false}
                    href={subItem.url}
                    target={subItem.newTab ? "_blank" : undefined}
                    rel={subItem.newTab ? "noreferrer" : undefined}
                    aria-current={isSubItemActive(subItem.url, subItem.matchUrls) ? "page" : undefined}
                    className="flex items-center gap-2"
                  >
                    {SubIcon && <SubIcon />}
                    <span>{subItem.title}</span>
                  </Link>
                </DropdownMenuItem>
              );
            })}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuItem>
  );
}

function NavCollapsibleItem({ item, isActive, open, onOpenChange, isSubItemActive }: NavCollapsibleItemProps) {
  const Icon = item.icon;

  return (
    <Collapsible asChild open={open} onOpenChange={onOpenChange} className="group/collapsible">
      <SidebarMenuItem>
        <CollapsibleTrigger asChild>
          <SidebarMenuButton tooltip={item.title} isActive={isActive} disabled={item.disabled}>
            {Icon && <Icon />}
            <span>{item.title}</span>
            <ChevronRight className="ml-auto transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90" />
          </SidebarMenuButton>
        </CollapsibleTrigger>
        {item.liveBadge === "intercept_pending" ? <InterceptPendingBadge /> : <NavItemBadge badge={item.badge} />}

        <CollapsibleContent>
          <SidebarMenuSub>
            {item.subItems.map((subItem) => {
              const SubIcon = subItem.icon;

              return (
                <SidebarMenuSubItem key={subItem.id}>
                  <SidebarMenuSubButton
                    asChild
                    aria-disabled={subItem.disabled}
                    isActive={isSubItemActive(subItem.url, subItem.matchUrls)}
                  >
                    <Link
                      prefetch={false}
                      href={subItem.url}
                      target={subItem.newTab ? "_blank" : undefined}
                      rel={subItem.newTab ? "noreferrer" : undefined}
                    >
                      {SubIcon && <SubIcon />}
                      <span>{subItem.title}</span>
                    </Link>
                  </SidebarMenuSubButton>
                </SidebarMenuSubItem>
              );
            })}
          </SidebarMenuSub>
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  );
}

function NavItemBadge({ badge }: { badge?: NavBadge }) {
  if (!badge) {
    return null;
  }

  return (
    <SidebarMenuBadge
      className={cn(
        "rounded-sm border capitalize",
        badge === "new" &&
          "border-green-600 text-green-600 peer-hover/menu-button:text-green-600 peer-data-active/menu-button:text-green-600 dark:border-green-500 dark:text-green-400",
        badge === "soon" && "border-muted-foreground text-muted-foreground",
      )}
    >
      {badge}
    </SidebarMenuBadge>
  );
}
