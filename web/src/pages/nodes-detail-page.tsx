import { useParams, useSearchParams } from "react-router-dom";
import OverviewTab from "@/features/nodes-detail/overview-tab";
import MetricsTab from "@/features/nodes-detail/metrics-tab";
import TasksTab from "@/features/nodes-detail/tasks-tab";
import AlertsTab from "@/features/nodes-detail/alerts-tab";
import ProfileTab from "@/features/nodes-detail/profile-tab";
import { useNodeStatus } from "@/features/nodes-detail/use-node-status";

const TABS = [
  { id: "overview", label: "概览" },
  { id: "metrics", label: "指标" },
  { id: "tasks", label: "任务" },
  { id: "alerts", label: "告警" },
  { id: "profile", label: "属性" },
] as const;
type TabId = typeof TABS[number]["id"];

function isTabId(v: string | null): v is TabId {
  return TABS.some((t) => t.id === v);
}

export function NodesDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [params, setParams] = useSearchParams();
  const nodeId = Number(id ?? 0);

  const tabParam = params.get("tab");
  const activeTab: TabId = isTabId(tabParam) ? tabParam : "overview";
  const { data: status, isLoading } = useNodeStatus(nodeId);

  const setTab = (t: TabId) => {
    const next = new URLSearchParams(params);
    next.set("tab", t);
    setParams(next, { replace: true });
  };

  const statusBadge = isLoading ? "加载中" : status?.online ? "在线" : "离线";
  const badgeClass = status?.online
    ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
    : "bg-stone-500/10 text-stone-500 dark:text-stone-400";

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <div className="text-sm text-muted-foreground">节点 / #{nodeId}</div>
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-semibold">节点详情</h1>
          <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${badgeClass}`}>{statusBadge}</span>
        </div>
        {status?.probed_at && (
          <p className="text-sm text-muted-foreground">最近一次探测：{new Date(status.probed_at).toLocaleString()}</p>
        )}
      </header>

      <div role="tablist" className="flex gap-1 border-b border-border">
        {TABS.map((t) => {
          const isActive = activeTab === t.id;
          return (
            <button
              key={t.id}
              role="tab"
              type="button"
              aria-selected={isActive}
              data-state={isActive ? "active" : "inactive"}
              className={`px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors ${
                isActive
                  ? "border-primary text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              }`}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          );
        })}
      </div>

      <div role="tabpanel">
        {activeTab === "overview" && <OverviewTab nodeId={nodeId} />}
        {activeTab === "metrics" && <MetricsTab nodeId={nodeId} />}
        {activeTab === "tasks" && <TasksTab nodeId={nodeId} />}
        {activeTab === "alerts" && <AlertsTab nodeId={nodeId} />}
        {activeTab === "profile" && <ProfileTab nodeId={nodeId} />}
      </div>
    </div>
  );
}
