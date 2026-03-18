import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { LogOut, Menu, RefreshCw, Settings, X } from "lucide-react";
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
  const menuButtonRef = useRef<HTMLButtonElement | null>(null);
  const drawerRef = useRef<HTMLElement | null>(null);
  const navItems = useMemo(() => getVisibleNavItems(role), [role]);
  const drawerId = "mobile-quick-menu";
  const drawerTitleId = "mobile-quick-menu-title";

  const mobileTabs = useMemo(() => navItems.filter((item) => item.mobileTab !== false), [navItems]);
  const tabBaseClass =
    "flex min-h-14 flex-col items-center justify-center gap-1 px-1 text-xs transition-colors";

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

    const buttonElement = menuButtonRef.current;

    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener("keydown", handleKeyDown);
      if (buttonElement?.isConnected) buttonElement.focus();
    };
  }, [drawerOpen]);

  return (
    <>
      <nav className="fixed inset-x-0 bottom-0 z-40 border-t border-border/75 bg-background/88 pb-[env(safe-area-inset-bottom)] backdrop-blur-xl md:hidden">
        <div
          className="grid h-[68px]"
          style={{ gridTemplateColumns: `repeat(${Math.max(1, mobileTabs.length)}, minmax(0, 1fr))` }}
        >
          {mobileTabs.map((item) => {
            const Icon = item.icon;
            const active = location.pathname === item.path;
            return (
              <Link
                key={item.path}
                to={item.path}
                className={cn(
                  tabBaseClass,
                  active
                    ? "text-[hsl(var(--nav-active-foreground))]"
                    : "text-muted-foreground"
                )}
                aria-label={t('appShell.switchTo', { name: t(item.titleKey) })}
                aria-current={active ? "page" : undefined}
              >
                <span
                  className={cn(
                    "rounded-full px-2 py-0.5 transition-colors",
                    active
                      ? "bg-[hsl(var(--nav-active))]"
                      : "bg-transparent"
                  )}
                >
                  <Icon className="size-4" />
                </span>
                {t(item.titleKey)}
              </Link>
            );
          })}
        </div>
      </nav>

      <button
        ref={menuButtonRef}
        className="fixed right-3 top-[calc(env(safe-area-inset-top)+0.75rem)] z-50 rounded-full border border-border/80 bg-background/85 p-2.5 shadow-panel md:hidden"
        onClick={() => setDrawerOpen(true)}
        aria-label={t('appShell.openQuickMenu')}
        aria-controls={drawerId}
        aria-expanded={drawerOpen}
        type="button"
      >
        <Menu className="size-5" />
      </button>

      {drawerOpen ? (
        <div className="fixed inset-0 z-50 md:hidden">
          <button
            className="absolute inset-0 bg-black/50"
            aria-label={t('appShell.closeDrawer')}
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
                {t('appShell.quickOps')}
              </p>
              <Button variant="ghost" size="icon" aria-label={t('appShell.closeQuickMenu')} title={t('appShell.closeQuickMenu')} onClick={() => setDrawerOpen(false)}>
                <X className="size-5" />
              </Button>
            </div>

            <div className="space-y-2">
              {navItems.map((item) => {
                const Icon = item.icon;
                const active = location.pathname === item.path;
                return (
                  <Link
                    key={item.path}
                    to={item.path}
                    onClick={() => setDrawerOpen(false)}
                    aria-current={active ? "page" : undefined}
                    className={cn(
                      "flex items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-all duration-200",
                      active
                        ? "border-primary/35 bg-[hsl(var(--nav-active))] text-[hsl(var(--nav-active-foreground))]"
                        : "border-transparent text-muted-foreground transition-all duration-200 ease-out hover:border-border/70 hover:bg-background/70 hover:text-foreground"
                    )}
                  >
                    <Icon className="size-4" />
                    {t(item.titleKey)}
                  </Link>
                );
              })}
            </div>

            {/* 设置入口 */}
            <Link
              to="/app/settings"
              onClick={() => setDrawerOpen(false)}
              aria-current={location.pathname === "/app/settings" ? "page" : undefined}
              className={cn(
                "flex items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-all duration-200 mt-2",
                location.pathname === "/app/settings"
                  ? "border-primary/35 bg-[hsl(var(--nav-active))] text-[hsl(var(--nav-active-foreground))]"
                  : "border-transparent text-muted-foreground transition-all duration-200 ease-out hover:border-border/70 hover:bg-background/70 hover:text-foreground"
              )}
            >
              <Settings className="size-4" />
              {t("nav.settings")}
            </Link>

            <div className="mt-6">
              <Button variant="outline" className="h-10 w-full" onClick={onRefresh}>
                <RefreshCw className="mr-1 size-4" />
                {t('appShell.refreshData')}
              </Button>
            </div>

            <div className="mt-auto flex items-center justify-between border-t border-border/80 pt-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
              <div className="flex items-center gap-2">
                <p className="text-xs text-muted-foreground">{t('appShell.currentUser', { name: username ?? t('common.unknown') })}</p>
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
                  {t('appShell.logoutShort')}
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
