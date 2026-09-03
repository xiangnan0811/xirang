import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { backupFilesHref, BACKUP_FILES_PATH, getVisibleNavItems, isNavPathActive, navGroups } from "@/components/layout/navigation";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import type { UserRecord } from "@/types/domain";

type DesktopSidebarProps = {
  role: UserRecord["role"] | null;
  isCollapsed: boolean;
  onToggleCollapse: () => void;
};

export function DesktopSidebar({
  role,
  isCollapsed,
  onToggleCollapse,
}: DesktopSidebarProps) {
  const { t } = useTranslation();
  const location = useLocation();
  const allItems = getVisibleNavItems(role);
  const navItemClass = ({
    isActive,
    isCollapsed,
  }: {
    isActive: boolean;
    isCollapsed: boolean;
  }) =>
    cn(
      "relative flex items-center py-1.5 text-sm font-medium transition-[color,background-color,opacity] duration-200 rounded-md",
      isCollapsed ? "justify-center px-2" : "gap-3 px-3",
      isActive
        ? [
            "bg-primary/10 text-primary",
            // left indicator rail — absolute pseudo-element via before:
            "before:absolute before:inset-y-2 before:-left-[9px] before:w-[3px] before:rounded-r before:bg-gradient-to-b before:from-primary before:to-info",
          ]
        : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
    );

  const pinnedItems = allItems.filter((item) => item.group === "pinned");

  return (
    <aside
      className={cn(
        "fixed left-0 top-0 z-40 hidden h-screen flex-col border-r border-border bg-card/80 backdrop-blur-xl pb-4 pt-14 transition-[width] duration-200 md:flex",
        isCollapsed ? "w-16 px-2" : "w-60 px-3",
      )}
    >
      <nav className="flex flex-1 flex-col pt-3 overflow-y-auto thin-scrollbar pb-2">
        {navGroups.map((group, groupIndex) => {
          const groupItems = allItems.filter((item) => item.group === group.key);
          if (groupItems.length === 0) return null;

          return (
            <div key={group.key} className={cn(groupIndex > 0 && "mt-4")}>
              {/* Group label — hidden in collapsed mode */}
              {!isCollapsed && (
                <span className="px-3 mb-1.5 block text-micro font-medium uppercase tracking-[0.08em] text-muted-foreground/70">
                  {t(group.labelKey)}
                </span>
              )}

              <div className="flex flex-col gap-0.5">
                {groupItems.map((item) => {
                  const Icon = item.icon;
                  const active = isNavPathActive(item.path, location.pathname);
                  return (
                    <Link
                      key={item.path}
                      to={item.path === BACKUP_FILES_PATH ? backupFilesHref(location.pathname, location.search) : item.path}
                      title={t(item.titleKey)}
                      aria-label={t(item.titleKey)}
                      aria-current={active ? "page" : undefined}
                      className={navItemClass({ isActive: active, isCollapsed })}
                    >
                      <Icon
                        className={cn(
                          "shrink-0",
                          isCollapsed ? "size-5" : "size-4",
                          active && "text-primary",
                        )}
                        aria-hidden
                      />
                      {!isCollapsed && (
                        <span className="truncate text-nav">
                          {t(item.titleKey)}
                        </span>
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

      {/* Pinned items (settings, etc.) */}
      {pinnedItems.length > 0 && (
        <div
          className={cn(
            "shrink-0 mb-2 flex flex-col gap-0.5",
            isCollapsed ? "px-0 items-center" : "px-0",
          )}
        >
          {pinnedItems.map((item) => {
            const Icon = item.icon;
            const active = isNavPathActive(item.path, location.pathname);
            return (
              <Link
                key={item.path}
                to={item.path === BACKUP_FILES_PATH ? backupFilesHref(location.pathname, location.search) : item.path}
                title={t(item.titleKey)}
                aria-label={t(item.titleKey)}
                aria-current={active ? "page" : undefined}
                className={navItemClass({ isActive: active, isCollapsed })}
              >
                <Icon
                  className={cn(
                    "shrink-0",
                    isCollapsed ? "size-5" : "size-4",
                    active && "text-primary",
                  )}
                  aria-hidden
                />
                {!isCollapsed && (
                  <span className="truncate text-nav">
                    {t(item.titleKey)}
                  </span>
                )}
              </Link>
            );
          })}
        </div>
      )}

      {/* Collapse / expand toggle */}
      <div
        className={cn(
          "shrink-0",
          isCollapsed ? "px-0 flex justify-center" : "px-0",
        )}
      >
        <Button
          variant="ghost"
          size={isCollapsed ? "icon" : "default"}
          className={cn(
            "text-muted-foreground",
            isCollapsed ? "size-10" : "w-full justify-start px-3 h-10",
          )}
          onClick={onToggleCollapse}
          aria-label={
            isCollapsed
              ? t("appShell.expandSidebar")
              : t("appShell.collapseSidebar")
          }
          title={
            isCollapsed
              ? t("appShell.expandSidebar")
              : t("appShell.collapseSidebar")
          }
        >
          {isCollapsed ? (
            <PanelLeftOpen className="size-5" aria-hidden />
          ) : (
            <>
              <PanelLeftClose className="mr-3 size-[18px]" aria-hidden />
              <span className="text-nav font-medium">
                {t("appShell.collapsePanel")}
              </span>
            </>
          )}
        </Button>
      </div>
    </aside>
  );
}
