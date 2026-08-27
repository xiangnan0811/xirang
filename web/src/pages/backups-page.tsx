import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { PageHero } from "@/components/ui/page-hero";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import type { BackupHealthData } from "@/types/domain";

const routeTabs = [
  { page: "data", path: "/app/backups/data", labelKey: "backups.tabs.data" },
  { page: "overview", path: "/app/backups/overview", labelKey: "backups.tabs.overview" },
  { page: "recovery", path: "/app/backups/recovery", labelKey: "backups.tabs.recovery" },
] as const;

type RouteTabPage = (typeof routeTabs)[number]["page"];

export function BackupsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const location = useLocation();
  const navigate = useNavigate();
  const tabRefs = useRef<Array<HTMLAnchorElement | null>>([]);
  const [healthData, setHealthData] = useState<BackupHealthData | null>(null);
  const [healthLoading, setHealthLoading] = useState(true);
  const activePage = getActivePage(location.pathname);

  useEffect(() => {
    if (!token) {
      if (import.meta.env.VITE_ENABLE_DEMO_MODE === "true") {
        let cancelled = false;
        setHealthLoading(true);
        import("@/data/mock")
          .then((mocks) => {
            if (!cancelled) setHealthData(mocks.buildMockBackupHealth());
          })
          .finally(() => {
            if (!cancelled) setHealthLoading(false);
          });
        return () => {
          cancelled = true;
        };
      }
      setHealthData(null);
      setHealthLoading(false);
      return;
    }
    const controller = new AbortController();
    setHealthLoading(true);
    apiClient
      .getBackupHealth(token, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) setHealthData(result);
      })
      .catch(() => {
        // The overview panel owns the detailed health error state.
      })
      .finally(() => {
        if (!controller.signal.aborted) setHealthLoading(false);
      });
    return () => controller.abort();
  }, [token]);

  const subtitle = healthLoading ? (
    <Skeleton className="h-4 w-48" />
  ) : healthData ? (
    t("backups.pageSubtitle", {
      count: healthData.summary.policiesHealthy + healthData.summary.policiesDegraded,
      healthy: healthData.summary.policiesHealthy,
    })
  ) : null;

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
    navigate(target.path);
    requestAnimationFrame(() => tabRefs.current[targetIndex]?.focus());
  };

  return (
    <div className="animate-fade-in flex min-h-0 flex-col gap-4">
      <PageHero
        title={t("backups.pageTitle")}
        subtitle={subtitle}
        actions={
          <Button shape="pill" asChild>
            <Link to="/app/tasks">{t("backups.newBackup")}</Link>
          </Button>
        }
      />

      <div
        role="tablist"
        aria-label={t("backups.tabsAriaLabel")}
        className="flex min-h-10 items-end gap-1 overflow-x-auto border-b border-border"
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
              to={tab.path}
              role="tab"
              aria-selected={selected}
              aria-controls={`backups-${tab.page}-panel`}
              tabIndex={selected ? 0 : -1}
              onKeyDown={(event) => handleTabKeyDown(event, index)}
              className={cn(
                "relative flex h-10 shrink-0 items-center px-3 text-sm font-medium text-muted-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
                selected && "text-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:bg-primary"
              )}
            >
              {t(tab.labelKey)}
            </NavLink>
          );
        })}
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

function getActivePage(pathname: string): RouteTabPage {
  if (pathname === "/app/backups/data") return "data";
  if (pathname === "/app/backups/recovery") return "recovery";
  return "overview";
}
