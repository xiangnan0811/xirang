import { lazy, Suspense, type ReactNode } from "react";

export const OverviewPage = lazy(() =>
  import("@/pages/overview-page").then((m) => ({ default: m.OverviewPage }))
);
export const NodesPage = lazy(() =>
  import("@/pages/nodes-page").then((m) => ({ default: m.NodesPage }))
);
export const SSHKeysPage = lazy(() =>
  import("@/pages/ssh-keys-page").then((m) => ({ default: m.SSHKeysPage }))
);
export const BackupsPage = lazy(() =>
  import("@/pages/backups-page").then((m) => ({ default: m.BackupsPage }))
);
export const PoliciesPage = lazy(() =>
  import("@/pages/policies-page").then((m) => ({ default: m.PoliciesPage }))
);
export const LogsPage = lazy(() =>
  import("@/pages/logs/logs-page").then((m) => ({ default: m.LogsPage }))
);
export const NotificationsPage = lazy(() =>
  import("@/pages/notifications-page").then((m) => ({
    default: m.NotificationsPage,
  }))
);
export const TasksPage = lazy(() =>
  import("@/pages/tasks-page").then((m) => ({ default: m.TasksPage }))
);
export const AuditPage = lazy(() =>
  import("@/pages/audit-page").then((m) => ({ default: m.AuditPage }))
);
export const ReportsPage = lazy(() =>
  import("@/pages/reports-page").then((m) => ({ default: m.ReportsPage }))
);
export const SettingsPage = lazy(() =>
  import("@/pages/settings-page").then((m) => ({ default: m.SettingsPage }))
);
export const CredentialsPage = lazy(() =>
  import("@/pages/credentials-page").then((m) => ({
    default: m.CredentialsPage,
  }))
);
export const MorePage = lazy(() =>
  import("@/pages/more-page").then((m) => ({ default: m.MorePage }))
);
export const NodesDetailPage = lazy(() =>
  import("@/pages/nodes-detail-page").then((m) => ({ default: m.NodesDetailPage }))
);
export const DashboardsPage = lazy(() =>
  import("@/pages/dashboards/dashboards-page").then((m) => ({ default: m.DashboardsPage }))
);
export const DashboardDetailPage = lazy(() =>
  import("@/pages/dashboards/dashboard-detail-page").then((m) => ({ default: m.DashboardDetailPage }))
);
export const AutomationRulesPage = lazy(() =>
  import("@/pages/automation-rules-page").then((m) => ({ default: m.AutomationRulesPage }))
);
export const ServiceMonitorsPage = lazy(() =>
  import("@/pages/service-monitors-page").then((m) => ({ default: m.ServiceMonitorsPage }))
);
export const StatusPage = lazy(() =>
  import("@/pages/status-page").then((m) => ({ default: m.StatusPage }))
);

export function PageLoader() {
  return (
    <div className="flex items-center justify-center py-16">
      <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent" />
    </div>
  );
}

export function LazyPage({ children }: { children: ReactNode }) {
  return <Suspense fallback={<PageLoader />}>{children}</Suspense>;
}
