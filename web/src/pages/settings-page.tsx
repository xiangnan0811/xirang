import * as React from "react";
import * as Tabs from "@radix-ui/react-tabs";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  User,
  ShieldCheck,
  Users,
  Bell,
  Settings2,
  Wrench,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useAuth } from "@/context/auth-context";
import { PageHero } from "@/components/ui/page-hero";

import { PersonalTab } from "./settings-page.personal";
import { AccountTab } from "./settings-page.account";
import { UsersTab } from "./settings-page.users";
import { ChannelsTab } from "./settings-page.channels";
import { SystemTab } from "./settings-page.system";
import { MaintenanceTab } from "./settings-page.maintenance";

const ALL_TABS = ["personal", "account", "users", "channels", "system", "maintenance"] as const;
type TabId = (typeof ALL_TABS)[number];

const TAB_ICONS: Record<TabId, React.ElementType> = {
  personal: User,
  account: ShieldCheck,
  users: Users,
  channels: Bell,
  system: Settings2,
  maintenance: Wrench,
};

export function SettingsPage() {
  const { t } = useTranslation();
  const { role } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const isAdmin = role === "admin";

  const visibleTabs: readonly TabId[] = isAdmin
    ? ALL_TABS
    : (["personal", "account"] as const);

  const paramTab = searchParams.get("tab") as TabId | null;
  const activeTab: TabId =
    paramTab && visibleTabs.includes(paramTab as never) ? paramTab : "personal";

  const handleTabChange = (value: string) => {
    setSearchParams({ tab: value }, { replace: true });
  };

  const tabLabelKey = (id: TabId) => `settings.tabs.${id}` as const;

  return (
    <div className="animate-fade-in space-y-5">
      <PageHero title={t("settings.pageTitle")} />

      <Tabs.Root
        orientation="vertical"
        value={activeTab}
        onValueChange={handleTabChange}
        className="grid grid-cols-[220px_1fr] gap-6 items-start"
      >
        {/* ── Left nav ── */}
        <Tabs.List
          className="flex flex-col gap-1 rounded-xl border border-border bg-card p-2 shadow-sm"
          aria-label={t("settings.pageTitle")}
        >
          {visibleTabs.map((id) => {
            const Icon = TAB_ICONS[id];
            return (
              <Tabs.Trigger
                key={id}
                value={id}
                className={cn(
                  "flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-sm transition-colors",
                  "text-muted-foreground hover:bg-accent hover:text-foreground",
                  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40",
                  "data-[state=active]:bg-accent data-[state=active]:text-foreground data-[state=active]:font-semibold",
                )}
              >
                <Icon className="size-4 shrink-0" aria-hidden />
                {t(tabLabelKey(id))}
              </Tabs.Trigger>
            );
          })}
        </Tabs.List>

        {/* ── Right content ── */}
        <div className="min-w-0">
          <Tabs.Content value="personal" className="outline-none">
            <PersonalTab />
          </Tabs.Content>
          <Tabs.Content value="account" className="outline-none">
            <AccountTab />
          </Tabs.Content>
          {isAdmin && (
            <>
              <Tabs.Content value="users" className="outline-none">
                <UsersTab />
              </Tabs.Content>
              <Tabs.Content value="channels" className="outline-none">
                <ChannelsTab />
              </Tabs.Content>
              <Tabs.Content value="system" className="outline-none">
                <SystemTab />
              </Tabs.Content>
              <Tabs.Content value="maintenance" className="outline-none">
                <MaintenanceTab />
              </Tabs.Content>
            </>
          )}
        </div>
      </Tabs.Root>
    </div>
  );
}
