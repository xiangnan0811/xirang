import { useRef, useState, type KeyboardEvent } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { PageHero } from "@/components/ui/page-hero";
import { getBackupActivePage } from "@/lib/backup-navigation";
import { cn } from "@/lib/utils";

const routeTabs = [
  { page: "data", path: "/app/backups/data", labelKey: "backups.tabs.data" },
  { page: "overview", path: "/app/backups/overview", labelKey: "backups.tabs.overview" },
  { page: "recovery", path: "/app/backups/recovery", labelKey: "backups.tabs.recovery" },
] as const;

export function BackupsPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const tabRefs = useRef<Array<HTMLAnchorElement | null>>([]);
  const activePage = getBackupActivePage(location.pathname);
  const [lastDataSearch, setLastDataSearch] = useState(() =>
    activePage === "data" ? location.search : "",
  );
  if (activePage === "data" && lastDataSearch !== location.search) {
    setLastDataSearch(location.search);
  }
  const filesSearch = activePage === "data" ? location.search : lastDataSearch;

  const handleTabKeyDown = (event: KeyboardEvent<HTMLAnchorElement>, index: number) => {
    let targetIndex: number | null = null;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      targetIndex = (index + 1) % routeTabs.length;
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      targetIndex = (index - 1 + routeTabs.length) % routeTabs.length;
    } else if (event.key === "Home") {
      targetIndex = 0;
    } else if (event.key === "End") {
      targetIndex = routeTabs.length - 1;
    }
    if (targetIndex === null) return;
    event.preventDefault();
    const target = routeTabs[targetIndex];
    navigate(target.page === "data" ? `${target.path}${filesSearch}` : target.path);
    requestAnimationFrame(() => tabRefs.current[targetIndex]?.focus());
  };

  return (
    <div className="animate-fade-in flex min-h-0 flex-col gap-5">
      <PageHero
        title={t("backups.pageTitle")}
        subtitle={t("backups.pageDesc")}
        actions={
          <Button shape="pill" size="lg" asChild>
            <Link to="/app/tasks">{t("backups.newBackup")}</Link>
          </Button>
        }
      />

      <div className="overflow-x-auto">
        <div
          role="tablist"
          aria-label={t("backups.tabsAriaLabel")}
          className="inline-flex min-h-11 min-w-max items-center gap-1 rounded-lg border border-border bg-background/70 p-1"
        >
          {routeTabs.map((tab, index) => {
            const selected = activePage === tab.page;
            return (
              <NavLink
                key={tab.page}
                ref={(node) => {
                  tabRefs.current[index] = node;
                }}
                id={`backups-${tab.page}-tab`}
                to={tab.page === "data" ? `${tab.path}${filesSearch}` : tab.path}
                role="tab"
                aria-selected={selected}
                aria-controls={`backups-${tab.page}-panel`}
                tabIndex={selected ? 0 : -1}
                onKeyDown={(event) => handleTabKeyDown(event, index)}
                className={cn(
                  "inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center rounded-md px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
                  selected
                    ? "bg-primary text-primary-foreground shadow-sm"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground",
                )}
              >
                {t(tab.labelKey)}
              </NavLink>
            );
          })}
        </div>
      </div>

      <div className="min-h-0 flex-1">
        {routeTabs.map((tab) => {
          const selected = activePage === tab.page;
          return (
            <section
              key={tab.page}
              id={`backups-${tab.page}-panel`}
              role="tabpanel"
              aria-labelledby={`backups-${tab.page}-tab`}
              hidden={!selected}
              className="min-h-0"
            >
              {selected ? <Outlet /> : null}
            </section>
          );
        })}
      </div>
    </div>
  );
}
