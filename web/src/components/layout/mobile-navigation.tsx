import { useEffect, useMemo, useRef, useState } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { LogOut, MoreHorizontal, RefreshCw, X } from "lucide-react";
import { getVisibleNavItems } from "@/components/layout/navigation";
import { ThemeToggle } from "@/components/theme-toggle";
import { DisplayPreferencesToggle } from "@/components/display-preferences-toggle";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { UserRecord } from "@/types/domain";

type MobileNavigationProps = {
  username: string | null;
  role: UserRecord["role"] | null;
  onLogout: () => void;
  onRefresh: () => void;
};

export function MobileNavigation({ username, role, onLogout, onRefresh }: MobileNavigationProps) {
  const { t } = useTranslation();
  const location = useLocation();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const moreButtonRef = useRef<HTMLButtonElement | null>(null);
  const drawerRef = useRef<HTMLElement | null>(null);
  const navItems = useMemo(() => getVisibleNavItems(role), [role]);
  const drawerId = "mobile-quick-menu";
  const drawerTitleId = "mobile-quick-menu-title";

  // The 4 primary tabs shown in the bottom bar
  const primaryTabs = useMemo(() => navItems.filter((item) => item.mobileTab === true), [navItems]);

  // Items shown in the "More" drawer (everything not in primary tabs)
  const drawerItems = useMemo(() => navItems.filter((item) => item.mobileTab !== true), [navItems]);

  useEffect(() => {
    if (!drawerOpen) {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const collectFocusableNodes = () => {
      if (!drawerRef.current) {
        return [] as HTMLElement[];
      }
      const candidates = drawerRef.current.querySelectorAll<HTMLElement>(
        "a[href], button:not([disabled]), [tabindex]:not([tabindex='-1'])"
      );
      return Array.from(candidates).filter((node) => !node.hasAttribute("disabled"));
    };

    const initialFocusableNodes = collectFocusableNodes();
    initialFocusableNodes[0]?.focus();

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setDrawerOpen(false);
        return;
      }

      if (event.key !== "Tab") {
        return;
      }

      const focusableNodes = collectFocusableNodes();
      if (focusableNodes.length === 0) {
        return;
      }

      const firstNode = focusableNodes[0];
      const lastNode = focusableNodes[focusableNodes.length - 1];
      const activeElement = document.activeElement as HTMLElement | null;

      if (event.shiftKey) {
        if (!activeElement || activeElement === firstNode || !drawerRef.current?.contains(activeElement)) {
          event.preventDefault();
          lastNode.focus();
        }
        return;
      }

      if (activeElement === lastNode) {
        event.preventDefault();
        firstNode.focus();
      }
    };

    const buttonElement = moreButtonRef.current;

    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener("keydown", handleKeyDown);
      if (buttonElement?.isConnected) buttonElement.focus();
    };
  }, [drawerOpen]);

  return (
    <>
      {/* 5-tab bottom bar */}
      <nav
        className="fixed inset-x-0 bottom-0 z-40 grid h-[60px] grid-cols-5 border-t border-border/75 bg-background/88 backdrop-blur-xl md:hidden"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
        aria-label={t("appShell.mobileNav")}
      >
        {primaryTabs.map((item) => {
          const Icon = item.icon;
          const active = location.pathname === item.path;
          return (
            <NavLink
              key={item.path}
              to={item.path}
              end={item.path === "/app/overview"}
              aria-label={t("appShell.switchTo", { name: t(item.titleKey) })}
              aria-current={active ? "page" : undefined}
              className={cn(
                "flex flex-col items-center justify-center gap-1 px-1 text-[10px] transition-colors",
                active
                  ? "text-[hsl(var(--nav-active-foreground))] font-semibold"
                  : "text-muted-foreground"
              )}
            >
              <span
                className={cn(
                  "rounded-full px-2 py-0.5 transition-colors",
                  active ? "bg-[hsl(var(--nav-active))]" : "bg-transparent"
                )}
              >
                <Icon className="size-[18px]" aria-hidden />
              </span>
              <span>{t(item.titleKey)}</span>
            </NavLink>
          );
        })}

        {/* 5th tab — More */}
        <button
          ref={moreButtonRef}
          type="button"
          onClick={() => setDrawerOpen(true)}
          aria-label={t("appShell.openQuickMenu")}
          aria-controls={drawerId}
          aria-expanded={drawerOpen}
          className={cn(
            "flex flex-col items-center justify-center gap-1 px-1 text-[10px] transition-colors",
            drawerOpen
              ? "text-[hsl(var(--nav-active-foreground))] font-semibold"
              : "text-muted-foreground"
          )}
        >
          <span
            className={cn(
              "rounded-full px-2 py-0.5 transition-colors",
              drawerOpen ? "bg-[hsl(var(--nav-active))]" : "bg-transparent"
            )}
          >
            <MoreHorizontal className="size-[18px]" aria-hidden />
          </span>
          <span>{t("nav.more")}</span>
        </button>
      </nav>

      {/* More drawer */}
      {drawerOpen ? (
        <div className="fixed inset-0 z-50 md:hidden">
          <button
            className="absolute inset-0 bg-black/50"
            aria-label={t("appShell.closeDrawer")}
            onClick={() => setDrawerOpen(false)}
            type="button"
          />

          <section
            ref={drawerRef}
            id={drawerId}
            role="dialog"
            aria-modal="true"
            aria-labelledby={drawerTitleId}
            className="absolute right-0 top-0 flex h-full w-[84%] flex-col overflow-y-auto border-l border-border/75 bg-background/95 p-4 shadow-panel thin-scrollbar"
          >
            <div className="mb-4 flex items-center justify-between">
              <p id={drawerTitleId} className="inline-flex items-center gap-2 text-sm text-muted-foreground">
                <img src="/xirang-mark.svg" alt="XiRang" className="size-5 rounded-sm" />
                {t("appShell.quickOps")}
              </p>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("appShell.closeQuickMenu")}
                title={t("appShell.closeQuickMenu")}
                onClick={() => setDrawerOpen(false)}
              >
                <X className="size-5" />
              </Button>
            </div>

            <div className="space-y-2">
              {drawerItems.map((item) => {
                const Icon = item.icon;
                const active = location.pathname === item.path;
                return (
                  <Link
                    key={item.path}
                    to={item.path}
                    onClick={() => setDrawerOpen(false)}
                    aria-current={active ? "page" : undefined}
                    className={cn(
                      "flex items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-[color,background-color,opacity] duration-200",
                      active
                        ? "border-primary/35 bg-[hsl(var(--nav-active))] text-[hsl(var(--nav-active-foreground))]"
                        : "border-transparent text-muted-foreground hover:border-border/70 hover:bg-background/70 hover:text-foreground"
                    )}
                  >
                    <Icon className="size-4" />
                    {t(item.titleKey)}
                  </Link>
                );
              })}
            </div>

            <div className="mt-6">
              <Button variant="outline" className="h-10 w-full" onClick={onRefresh}>
                <RefreshCw className="mr-1 size-4" />
                {t("appShell.refreshData")}
              </Button>
            </div>

            <div className="mt-auto flex items-center justify-between border-t border-border/80 pt-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
              <div className="flex items-center gap-2">
                <p className="text-xs text-muted-foreground">{t("appShell.currentUser", { name: username ?? t("common.unknown") })}</p>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs text-muted-foreground hover:text-foreground"
                  onClick={() => {
                    onLogout();
                    setDrawerOpen(false);
                  }}
                >
                  <LogOut className="mr-1 size-3" />
                  {t("appShell.logoutShort")}
                </Button>
              </div>
              <div className="flex items-center gap-1">
                <DisplayPreferencesToggle />
                <ThemeToggle />
              </div>
            </div>
          </section>
        </div>
      ) : null}
    </>
  );
}
