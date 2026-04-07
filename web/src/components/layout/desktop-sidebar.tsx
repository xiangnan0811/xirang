import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { PanelLeftClose, PanelLeftOpen, Settings } from "lucide-react";
import { getVisibleNavItems, navGroups } from "@/components/layout/navigation";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import type { UserRecord } from "@/types/domain";

type DesktopSidebarProps = {
  role: UserRecord["role"] | null;
  isCollapsed: boolean;
  hasWarning?: boolean;
  onToggleCollapse: () => void;
};

export function DesktopSidebar({ role, isCollapsed, hasWarning = false, onToggleCollapse }: DesktopSidebarProps) {
  const { t } = useTranslation();
  const location = useLocation();
  const navItems = getVisibleNavItems(role);

  const navItemBaseClass =
    "flex items-center py-2 text-sm transition-[color,background-color,opacity] duration-200";
  const navItemActiveClass =
    "bg-secondary text-foreground font-medium rounded-md";
  const navItemIdleClass =
    "text-muted-foreground hover:bg-accent hover:text-accent-foreground rounded-md";

  return (
    <aside
      className={cn(
        "fixed left-0 top-0 z-40 hidden h-screen flex-col border-r border-border bg-card md:flex pb-4 transition-[width] duration-200",
        hasWarning ? "pt-[84px]" : "pt-[52px]",
        isCollapsed ? "w-16 px-2" : "w-60 px-3"
      )}
    >
      <nav className="flex flex-1 flex-col pt-3 overflow-y-auto thin-scrollbar pb-2">
        {navGroups.map((group, groupIndex) => {
          const groupItems = navItems.filter((item) => item.group === group.key);
          if (groupItems.length === 0) return null;

          return (
            <div key={group.key} className={cn(groupIndex > 0 && "mt-4")}>
              {/* Group label — hidden in collapsed mode */}
              {!isCollapsed && (
                <span className="px-3 mb-1.5 block text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                  {t(group.labelKey)}
                </span>
              )}

              <div className="flex flex-col gap-1">
                {groupItems.map((item) => {
                  const active = location.pathname === item.path;
                  const Icon = item.icon;
                  return (
                    <Link
                      key={item.path}
                      to={item.path}
                      title={t(item.titleKey)}
                      aria-label={t(item.titleKey)}
                      aria-current={active ? "page" : undefined}
                      className={cn(
                        navItemBaseClass,
                        isCollapsed ? "justify-center px-2" : "gap-3 px-3",
                        active ? navItemActiveClass : navItemIdleClass
                      )}
                    >
                      <Icon className={cn("shrink-0", isCollapsed ? "size-5" : "size-4")} />
                      {!isCollapsed && (
                        <span className="text-[13px] font-medium">{t(item.titleKey)}</span>
                      )}
                    </Link>
                  );
                })}
              </div>
            </div>
          );
        })}
      </nav>

      <Separator className="my-3" />

      {/* Settings link pinned to bottom */}
      <div className={cn("shrink-0 mb-2", isCollapsed ? "px-0 flex justify-center" : "px-0")}>
        <Link
          to="/app/settings"
          title={t("nav.settings")}
          aria-label={t("nav.settings")}
          aria-current={location.pathname === "/app/settings" ? "page" : undefined}
          className={cn(
            navItemBaseClass,
            isCollapsed ? "justify-center px-2" : "gap-3 px-3",
            location.pathname === "/app/settings" ? navItemActiveClass : navItemIdleClass
          )}
        >
          <Settings className={cn("shrink-0", isCollapsed ? "size-5" : "size-4")} />
          {!isCollapsed && (
            <span className="text-[13px] font-medium">{t("nav.settings")}</span>
          )}
        </Link>
      </div>

      <div className={cn("shrink-0", isCollapsed ? "px-0 flex justify-center" : "px-0")}>
        <Button
          variant="ghost"
          size={isCollapsed ? "icon" : "default"}
          className={cn("text-muted-foreground", isCollapsed ? "size-10" : "w-full justify-start px-3 h-10")}
          onClick={onToggleCollapse}
          aria-label={isCollapsed ? t('appShell.expandSidebar') : t('appShell.collapseSidebar')}
          title={isCollapsed ? t('appShell.expandSidebar') : t('appShell.collapseSidebar')}
        >
          {isCollapsed ? (
            <PanelLeftOpen className="size-5" />
          ) : (
            <>
              <PanelLeftClose className="mr-3 size-[18px]" />
              <span className="text-[13px] font-medium">{t('appShell.collapsePanel')}</span>
            </>
          )}
        </Button>
      </div>
    </aside>
  );
}
