import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { cn } from "@/lib/utils";

import { PersonalTab } from "./settings-page.personal";
import { AccountTab } from "./settings-page.account";
import { UsersTab } from "./settings-page.users";
import { ChannelsTab } from "./settings-page.channels";
import { SystemTab } from "./settings-page.system";
import { MaintenanceTab } from "./settings-page.maintenance";

const TABS = ["personal", "account", "users", "channels", "system", "maintenance"] as const;
type TabId = (typeof TABS)[number];

export function SettingsPage() {
  const { t } = useTranslation();
  const { role } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const isAdmin = role === "admin";

  const visibleTabs = isAdmin ? TABS : (["personal", "account"] as const satisfies readonly TabId[]);
  const paramTab = searchParams.get("tab") as TabId | null;
  const [activeTab, setActiveTab] = useState<TabId>(
    paramTab && visibleTabs.includes(paramTab as never) ? paramTab : "personal"
  );

  const handleTabChange = (tab: TabId) => {
    setActiveTab(tab);
    setSearchParams({ tab }, { replace: true });
  };

  const tabLabels: Record<TabId, string> = {
    personal: t("settings.tabs.personal"),
    account: t("settings.tabs.account"),
    users: t("settings.tabs.users"),
    channels: t("settings.tabs.channels"),
    system: t("settings.tabs.system"),
    maintenance: t("settings.tabs.maintenance"),
  };

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">{t("settings.title")}</h1>

      <div role="tablist" className="flex gap-1 border-b border-border pb-px overflow-x-auto">
        {visibleTabs.map((tab) => (
          <button
            key={tab}
            id={`settings-tab-${tab}`}
            role="tab"
            aria-selected={activeTab === tab}
            aria-controls={`settings-panel-${tab}`}
            onClick={() => handleTabChange(tab)}
            className={cn(
              "px-4 py-2 text-sm font-medium transition-colors rounded-t-md -mb-px focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 focus-visible:ring-offset-2",
              activeTab === tab
                ? "border-b-2 border-primary text-foreground"
                : "text-muted-foreground hover:text-foreground"
            )}
          >
            {tabLabels[tab]}
          </button>
        ))}
      </div>

      <div role="tabpanel" id={`settings-panel-${activeTab}`} aria-labelledby={`settings-tab-${activeTab}`}>
        {activeTab === "personal" && <PersonalTab />}
        {activeTab === "account" && <AccountTab />}
        {activeTab === "users" && isAdmin && <UsersTab />}
        {activeTab === "channels" && isAdmin && <ChannelsTab />}
        {activeTab === "system" && isAdmin && <SystemTab />}
        {activeTab === "maintenance" && isAdmin && <MaintenanceTab />}
      </div>
    </div>
  );
}
