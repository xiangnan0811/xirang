import { useRef, type KeyboardEvent } from "react";
import { useParams, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import OverviewTab from "@/features/nodes-detail/overview-tab";
import MetricsTab from "@/features/nodes-detail/metrics-tab";
import TasksTab from "@/features/nodes-detail/tasks-tab";
import AlertsTab from "@/features/nodes-detail/alerts-tab";
import ProfileTab from "@/features/nodes-detail/profile-tab";
import LogConfigTab from "@/features/nodes-detail/log-config-tab";
import AnomalyTab from "@/features/nodes-detail/anomaly-tab";
import { useNodeStatus } from "@/features/nodes-detail/use-node-status";
import { useAuth } from "@/context/auth-context.hooks";
import { PageHero } from "@/components/ui/page-hero";

const TAB_IDS = ["overview", "metrics", "tasks", "alerts", "profile", "log-config", "anomaly"] as const;
type TabId = typeof TAB_IDS[number];

function isTabId(v: string | null): v is TabId {
  return TAB_IDS.includes(v as TabId);
}

export function NodesDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [params, setParams] = useSearchParams();
  const { t } = useTranslation();
  const { token } = useAuth();
  const nodeId = Number(id ?? 0);
  const tabRefs = useRef<Record<TabId, HTMLButtonElement | null>>({
    overview: null,
    metrics: null,
    tasks: null,
    alerts: null,
    profile: null,
    "log-config": null,
    anomaly: null,
  });

  const tabParam = params.get("tab");
  const activeTab: TabId = isTabId(tabParam) ? tabParam : "overview";
  // Single shared poll for page header + overview tab (avoid double /status).
  const { data: status, isLoading, error: statusError } = useNodeStatus(nodeId, token);

  // Prefer last known status while background-refreshing. Only show loading on
  // first fetch / after clear; never paint failed/empty/never-probed as "offline".
  let statusBadge: string;
  let badgeClass = "bg-muted text-muted-foreground";
  if (status?.probedAt) {
    // Online/offline only meaningful after at least one probe sample.
    if (status.online) {
      statusBadge = t("nodes.statusOnline");
      badgeClass = "bg-success/10 text-success";
    } else {
      statusBadge = t("nodes.statusOffline");
    }
  } else if (isLoading) {
    statusBadge = t("nodes.nodeDetail.statusLoading");
  } else {
    // No sample yet (or status poll failed after clear) → unknown, not offline.
    statusBadge = t("nodes.nodeDetail.statusUnknown");
    badgeClass = "bg-warning/10 text-warning";
  }

  const setTab = (tab: TabId) => {
    const next = new URLSearchParams(params);
    next.set("tab", tab);
    setParams(next, { replace: true });
  };

  const TABS: { id: TabId; label: string }[] = [
    { id: "overview", label: t("nodes.nodeDetail.tabOverview") },
    { id: "metrics", label: t("nodes.nodeDetail.tabMetrics") },
    { id: "tasks", label: t("nodes.nodeDetail.tabTasks") },
    { id: "alerts", label: t("nodes.nodeDetail.tabAlerts") },
    { id: "profile", label: t("nodes.nodeDetail.tabProfile") },
    { id: "log-config", label: t("nodeLogs.nodeConfig.tab") },
    { id: "anomaly", label: t("anomaly.tab.title") },
  ];

  const focusTab = (tab: TabId) => {
    window.requestAnimationFrame(() => tabRefs.current[tab]?.focus());
  };

  const handleTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let nextIndex: number | null = null;

    if (event.key === "ArrowRight") {
      nextIndex = (index + 1) % TABS.length;
    } else if (event.key === "ArrowLeft") {
      nextIndex = (index - 1 + TABS.length) % TABS.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = TABS.length - 1;
    }

    if (nextIndex === null) return;

    event.preventDefault();
    const nextTab = TABS[nextIndex].id;
    setTab(nextTab);
    focusTab(nextTab);
  };

  return (
    <div className="flex flex-col gap-6">
      <PageHero
        title={t("nodes.nodeDetail.title")}
        subtitle={t("nodes.nodeDetail.subtitle", { id: nodeId })}
        meta={
          <div className="flex flex-wrap items-center gap-2">
            <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${badgeClass}`}>
              {statusBadge}
            </span>
            {statusError ? (
              <span className="text-destructive" role="alert">
                {t("nodes.nodeDetail.statusLoadFailed")}
              </span>
            ) : null}
            {!statusError && status?.probedAt ? (
              <span>{t("nodes.nodeDetail.probedAt", { time: new Date(status.probedAt).toLocaleString() })}</span>
            ) : null}
          </div>
        }
      />

      <div
        role="tablist"
        aria-label={t("nodes.nodeDetail.tabsAriaLabel")}
        className="-mx-4 flex gap-1 overflow-x-auto overflow-y-hidden border-b border-border px-4 thin-scrollbar sm:mx-0 sm:px-0"
      >
        {TABS.map((t, index) => {
          const isActive = activeTab === t.id;
          const tabId = `node-detail-tab-${t.id}`;
          const panelId = `node-detail-panel-${t.id}`;
          return (
            <button
              key={t.id}
              id={tabId}
              ref={(element) => {
                tabRefs.current[t.id] = element;
              }}
              role="tab"
              type="button"
              aria-selected={isActive}
              aria-controls={panelId}
              tabIndex={isActive ? 0 : -1}
              data-state={isActive ? "active" : "inactive"}
              className={`shrink-0 whitespace-nowrap px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background ${
                isActive
                  ? "border-primary text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              }`}
              onClick={() => setTab(t.id)}
              onKeyDown={(event) => handleTabKeyDown(event, index)}
            >
              {t.label}
            </button>
          );
        })}
      </div>

      {TABS.map((tab) => (
        <div
          key={tab.id}
          id={`node-detail-panel-${tab.id}`}
          role="tabpanel"
          aria-labelledby={`node-detail-tab-${tab.id}`}
          hidden={activeTab !== tab.id}
        >
          {activeTab === "overview" && tab.id === "overview" ? (
            <OverviewTab
              key={`overview-${nodeId}`}
              nodeId={nodeId}
              token={token}
              status={status}
              statusError={statusError}
            />
          ) : null}
          {activeTab === "metrics" && tab.id === "metrics" ? (
            <MetricsTab key={`metrics-${nodeId}`} nodeId={nodeId} token={token} />
          ) : null}
          {activeTab === "tasks" && tab.id === "tasks" ? (
            <TasksTab key={`tasks-${nodeId}`} nodeId={nodeId} token={token} />
          ) : null}
          {activeTab === "alerts" && tab.id === "alerts" ? (
            <AlertsTab key={`alerts-${nodeId}`} nodeId={nodeId} token={token} />
          ) : null}
          {activeTab === "profile" && tab.id === "profile" ? (
            <ProfileTab key={`profile-${nodeId}`} nodeId={nodeId} token={token} />
          ) : null}
          {activeTab === "log-config" && tab.id === "log-config" ? (
            <LogConfigTab key={`log-config-${nodeId}`} nodeId={nodeId} token={token} />
          ) : null}
          {activeTab === "anomaly" && tab.id === "anomaly" ? (
            <AnomalyTab key={`anomaly-${nodeId}`} nodeId={nodeId} token={token} />
          ) : null}
        </div>
      ))}
    </div>
  );
}
