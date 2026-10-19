import { useEffect, useLayoutEffect, useRef, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { useAuth } from "@/context/auth-context.hooks";
import { useNotificationContextOwner } from "@/hooks/use-notification-context-owner";
import {
  readNotificationConfigReturn,
  withoutNotificationConfigReturn,
} from "@/lib/notification-config-context";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { PageHero } from "@/components/ui/page-hero";

import { PersonalTab } from "./settings-page.personal";
import { AccountTab } from "./settings-page.account";
import { UsersTab } from "./settings-page.users";
import { ChannelsTab } from "./settings-page.channels";
import { SystemTab } from "./settings-page.system";
import { MaintenanceTab } from "./settings-page.maintenance";
import { SilencesPanel } from "./settings-page.silences";
import { SettingsPageEscalation } from "./settings-page.escalation";

const TABS = ["personal", "account", "users", "channels", "silences", "escalation", "system", "maintenance"] as const;
type TabId = (typeof TABS)[number];

export function SettingsPage() {
  const { t } = useTranslation();
  const { role } = useAuth();
  const location = useLocation();
  const navigate = useNavigate();
  const owner = useNotificationContextOwner();
  const [searchParams] = useSearchParams();
  const isAdmin = role === "admin";
  const isActiveAdmin = owner.active && isAdmin;
  const tabRefs = useRef<Partial<Record<TabId, HTMLButtonElement | null>>>({});

  const navigationState: unknown = location.state;
  const hasReturn =
    navigationState !== null &&
    typeof navigationState === "object" &&
    Object.hasOwn(navigationState, "notificationConfigReturn");

  const source =
    hasReturn && "notificationConfigReturn" in navigationState
      ? readNotificationConfigReturn(navigationState.notificationConfigReturn)
      : null;

  const validSource = isActiveAdmin ? source : null;

  useLayoutEffect(() => {
    if (location.pathname !== "/app/settings") return;
    if (hasReturn && !validSource) {
      void navigate(
        {
          pathname: location.pathname,
          search: location.search,
          hash: location.hash,
        },
        {
          replace: true,
          state: withoutNotificationConfigReturn(navigationState),
        }
      );
    }
  }, [hasReturn, validSource, navigate, location.pathname, location.search, location.hash, navigationState]);

  const visibleTabs: readonly TabId[] = isAdmin ? TABS : ["personal", "account"];
  const paramTab = searchParams.get("tab") as TabId | null;
  const activeTab: TabId = paramTab && visibleTabs.includes(paramTab as never) ? paramTab : "personal";

  const contextFocusAppliedRef = useRef(false);
  useEffect(() => {
    if (contextFocusAppliedRef.current || !validSource) {
      return;
    }
    contextFocusAppliedRef.current = true;
    tabRefs.current[activeTab]?.focus();
  }, [validSource, activeTab]);

  const handleTabChange = (tab: TabId) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", tab);
    const searchString = next.toString();
    void navigate(
      {
        pathname: location.pathname,
        search: searchString ? `?${searchString}` : "",
        hash: location.hash,
      },
      { replace: true, state: location.state }
    );
  };

  const handleTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, tab: TabId) => {
    const currentIndex = visibleTabs.indexOf(tab);
    if (currentIndex === -1) {
      return;
    }

    let nextIndex = currentIndex;
    switch (event.key) {
      case "ArrowRight":
      case "ArrowDown":
        nextIndex = (currentIndex + 1) % visibleTabs.length;
        break;
      case "ArrowLeft":
      case "ArrowUp":
        nextIndex = (currentIndex - 1 + visibleTabs.length) % visibleTabs.length;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = visibleTabs.length - 1;
        break;
      default:
        return;
    }

    event.preventDefault();
    const nextTab = visibleTabs[nextIndex];
    handleTabChange(nextTab);
    tabRefs.current[nextTab]?.focus();
  };

  const isConfigTab = activeTab === "channels" || activeTab === "silences" || activeTab === "escalation";
  const showReturnRegion = isActiveAdmin && validSource !== null && isConfigTab;

  const handleReturnClick = (event: React.MouseEvent<HTMLAnchorElement>) => {
    if (!owner.capture()() || !readNotificationConfigReturn(validSource)) {
      event.preventDefault();
    }
  };

  const tabLabels: Record<TabId, string> = {
    personal: t("settings.tabs.personal"),
    account: t("settings.tabs.account"),
    users: t("settings.tabs.users"),
    channels: t("settings.tabs.channels"),
    silences: t("settings.tabs.silences"),
    escalation: t("escalation.tabTitle"),
    system: t("settings.tabs.system"),
    maintenance: t("settings.tabs.maintenance"),
  };
  const scopeLabel = isAdmin ? t("users.roles.admin") : t("settings.tabs.personal");
  const visibleTabsLabel = `${visibleTabs.length} · ${t("settings.tabListLabel")}`;
  const adminContentKey = JSON.stringify([owner.epoch, owner.token, owner.role]);

  return (
    <div className="animate-fade-in space-y-5">
      <PageHero
        title={t("settings.title")}
        subtitle={t("settings.pageDesc")}
        meta={
          <>
            <Badge tone={isAdmin ? "success" : "neutral"}>
              {scopeLabel}
            </Badge>
            <Badge tone="info">{visibleTabsLabel}</Badge>
          </>
        }
      />

      {showReturnRegion && validSource ? (
        <section
          aria-label={
            validSource.alertId !== null
              ? t("notifications.configContext.fromAlert", { id: validSource.alertId })
              : t("notifications.configContext.fromList")
          }
          className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card p-3 text-sm"
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-foreground">
              {validSource.alertId !== null
                ? t("notifications.configContext.fromAlert", { id: validSource.alertId })
                : t("notifications.configContext.fromList")}
            </span>
            <span className="text-xs text-muted-foreground">
              {t("notifications.configContext.manualReturnHint")}
            </span>
          </div>
          <Button asChild variant="outline" size="sm">
            <Link
              to={
                validSource.alertId !== null
                  ? `/app/notifications?alert=${validSource.alertId}`
                  : "/app/notifications"
              }
              state={{ notificationConfigReturn: validSource }}
              onClick={handleReturnClick}
            >
              {validSource.alertId !== null
                ? t("notifications.configContext.returnAlert")
                : t("notifications.configContext.returnList")}
            </Link>
          </Button>
        </section>
      ) : null}

      <div className="overflow-x-auto pb-1">
        <div
          role="tablist"
          aria-label={t("settings.tabListLabel")}
          aria-orientation="horizontal"
          className="inline-flex min-w-max items-center gap-1 rounded-lg border border-border bg-background/70 p-1"
        >
          {visibleTabs.map((tab) => (
            <button
              key={tab}
              ref={(node) => {
                tabRefs.current[tab] = node;
              }}
              id={`settings-tab-${tab}`}
              role="tab"
              aria-selected={activeTab === tab}
              aria-controls={`settings-panel-${tab}`}
              tabIndex={activeTab === tab ? 0 : -1}
              onClick={() => handleTabChange(tab)}
              onKeyDown={(event) => handleTabKeyDown(event, tab)}
              className={cn(
                "rounded-md px-3 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
                activeTab === tab
                  ? "bg-primary text-primary-foreground shadow-sm"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground"
              )}
            >
              {tabLabels[tab]}
            </button>
          ))}
        </div>
      </div>

      <div role="tabpanel" id={`settings-panel-${activeTab}`} aria-labelledby={`settings-tab-${activeTab}`}>
        {activeTab === "personal" && <PersonalTab />}
        {activeTab === "account" && <AccountTab />}
        {activeTab === "users" && isAdmin && <UsersTab />}
        {activeTab === "channels" && isActiveAdmin && <ChannelsTab key={adminContentKey} />}
        {activeTab === "silences" && isActiveAdmin && <SilencesPanel key={adminContentKey} />}
        {activeTab === "escalation" && isActiveAdmin && <SettingsPageEscalation key={adminContentKey} />}
        {activeTab === "system" && isAdmin && <SystemTab />}
        {activeTab === "maintenance" && isAdmin && <MaintenanceTab />}
      </div>
    </div>
  );
}
