import { useRef, type KeyboardEvent } from "react";
import { useParams, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import OverviewTab from "@/features/nodes-detail/overview-tab";
import TasksTab from "@/features/nodes-detail/tasks-tab";
import AlertsTab from "@/features/nodes-detail/alerts-tab";
import ProfileTab from "@/features/nodes-detail/profile-tab";
import AnomalyTab from "@/features/nodes-detail/anomaly-tab";
import { useNodeRecord } from "@/features/nodes-detail/use-node-record";
import { useNodeSummary } from "@/features/nodes-detail/use-node-summary";
import { useAuth } from "@/context/auth-context.hooks";
import { PageHero } from "@/components/ui/page-hero";

const TAB_IDS = ["overview", "tasks", "alerts", "profile", "anomaly"] as const;
type TabId = typeof TAB_IDS[number];

function isTabId(v: string | null): v is TabId {
  return TAB_IDS.includes(v as TabId);
}

export function NodesDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [params, setParams] = useSearchParams();
  const { t } = useTranslation();
  const { token } = useAuth();
  const parsedNodeId = Number(id);
  const nodeId = Number.isFinite(parsedNodeId) && parsedNodeId > 0 ? parsedNodeId : 0;
  const tabRefs = useRef<Record<TabId, HTMLButtonElement | null>>({
    overview: null,
    tasks: null,
    alerts: null,
    profile: null,
    anomaly: null,
  });

  const tabParam = params.get("tab");
  const activeTab: TabId = isTabId(tabParam) ? tabParam : "overview";
  const { data: node, isLoading: nodeLoading, error: nodeError } = useNodeRecord(nodeId, token);
  const { data: summary, isLoading: summaryLoading, error: summaryError } = useNodeSummary(nodeId, token);

  const setTab = (tab: TabId) => {
    const next = new URLSearchParams(params);
    next.set("tab", tab);
    setParams(next, { replace: true });
  };

  const TABS: { id: TabId; label: string }[] = [
    { id: "overview", label: t("nodes.nodeDetail.tabOverview") },
    { id: "tasks", label: t("nodes.nodeDetail.tabTasks") },
    { id: "alerts", label: t("nodes.nodeDetail.tabAlerts") },
    { id: "profile", label: t("nodes.nodeDetail.tabProfile") },
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

  const title = node?.name || (nodeLoading ? t("nodes.nodeDetail.loading") : t("nodes.nodeDetail.title"));
  const subtitle = node
    ? `${node.host}:${node.port}`
    : t("nodes.nodeDetail.subtitle", { id: nodeId });

  return (
    <div className="flex flex-col gap-6">
      <PageHero
        title={title}
        subtitle={subtitle}
        meta={
          nodeError ? (
            <span className="text-destructive" role="alert">
              {t("nodes.nodeDetail.profileError")}
            </span>
          ) : null
        }
      />

      <div
        role="tablist"
        aria-label={t("nodes.nodeDetail.tabsAriaLabel")}
        className="-mx-4 flex gap-1 overflow-x-auto overflow-y-hidden border-b border-border px-4 thin-scrollbar sm:mx-0 sm:px-0"
      >
        {TABS.map((tab, index) => {
          const isActive = activeTab === tab.id;
          const tabId = `node-detail-tab-${tab.id}`;
          const panelId = `node-detail-panel-${tab.id}`;
          return (
            <button
              key={tab.id}
              id={tabId}
              ref={(element) => {
                tabRefs.current[tab.id] = element;
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
              onClick={() => setTab(tab.id)}
              onKeyDown={(event) => handleTabKeyDown(event, index)}
            >
              {tab.label}
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
              summary={summary}
              summaryError={summaryError}
              summaryLoading={summaryLoading}
            />
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
          {activeTab === "anomaly" && tab.id === "anomaly" ? (
            <AnomalyTab key={`anomaly-${nodeId}`} nodeId={nodeId} token={token} />
          ) : null}
        </div>
      ))}
    </div>
  );
}
