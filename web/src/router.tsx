import { lazy, Suspense } from "react";
import { Navigate, createBrowserRouter } from "react-router-dom";
import { AppShell } from "@/components/layout/app-shell";
import { ProtectedRoute } from "@/components/protected-route";
import { LoginPage } from "@/pages/login-page";

const OverviewPage = lazy(() =>
  import("@/pages/overview-page").then((m) => ({ default: m.OverviewPage }))
);
const NodesPage = lazy(() =>
  import("@/pages/nodes-page").then((m) => ({ default: m.NodesPage }))
);
const SSHKeysPage = lazy(() =>
  import("@/pages/ssh-keys-page").then((m) => ({ default: m.SSHKeysPage }))
);
const BackupsPage = lazy(() =>
  import("@/pages/backups-page").then((m) => ({ default: m.BackupsPage }))
);
const PoliciesPage = lazy(() =>
  import("@/pages/policies-page").then((m) => ({ default: m.PoliciesPage }))
);
const LogsPage = lazy(() =>
  import("@/pages/logs/logs-page").then((m) => ({ default: m.LogsPage }))
);
const NotificationsPage = lazy(() =>
  import("@/pages/notifications-page").then((m) => ({
    default: m.NotificationsPage,
  }))
);
const TasksPage = lazy(() =>
  import("@/pages/tasks-page").then((m) => ({ default: m.TasksPage }))
);
const AuditPage = lazy(() =>
  import("@/pages/audit-page").then((m) => ({ default: m.AuditPage }))
);
const ReportsPage = lazy(() =>
  import("@/pages/reports-page").then((m) => ({ default: m.ReportsPage }))
);
const SettingsPage = lazy(() =>
  import("@/pages/settings-page").then((m) => ({ default: m.SettingsPage }))
);
const MorePage = lazy(() =>
  import("@/pages/more-page").then((m) => ({ default: m.MorePage }))
);
const NodesDetailPage = lazy(() =>
  import("@/pages/nodes-detail-page").then((m) => ({ default: m.NodesDetailPage }))
);

function PageLoader() {
  return (
    <div className="flex items-center justify-center py-16">
      <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent" />
    </div>
  );
}

function LazyPage({ children }: { children: React.ReactNode }) {
  return <Suspense fallback={<PageLoader />}>{children}</Suspense>;
}

export const AppRouter = createBrowserRouter([
  {
    path: "/",
    element: <Navigate to="/app/overview" replace />
  },
  {
    path: "/login",
    element: <LoginPage />
  },
  {
    path: "/app",
    element: (
      <ProtectedRoute>
        <AppShell />
      </ProtectedRoute>
    ),
    children: [
      {
        index: true,
        element: <Navigate to="overview" replace />
      },
      {
        path: "overview",
        element: <LazyPage><OverviewPage /></LazyPage>
      },
      {
        path: "nodes",
        element: <LazyPage><NodesPage /></LazyPage>
      },
      {
        path: "nodes/:id",
        element: <LazyPage><NodesDetailPage /></LazyPage>
      },
      {
        path: "ssh-keys",
        element: <LazyPage><SSHKeysPage /></LazyPage>
      },
      {
        path: "policies",
        element: <LazyPage><PoliciesPage /></LazyPage>
      },
      {
        path: "backups",
        element: <LazyPage><BackupsPage /></LazyPage>
      },
      {
        path: "logs",
        element: <LazyPage><LogsPage /></LazyPage>
      },
      {
        path: "notifications",
        element: <LazyPage><NotificationsPage /></LazyPage>
      },
      {
        path: "alert-center",
        element: <Navigate to="../notifications" replace />
      },
      {
        path: "tasks",
        element: <LazyPage><TasksPage /></LazyPage>
      },
      {
        path: "audit",
        element: <LazyPage><AuditPage /></LazyPage>
      },
      {
        path: "users",
        element: <Navigate to="../settings?tab=users" replace />
      },
      {
        path: "reports",
        element: <LazyPage><ReportsPage /></LazyPage>
      },
      {
        path: "settings",
        element: <LazyPage><SettingsPage /></LazyPage>
      },
      {
        path: "more",
        element: <LazyPage><MorePage /></LazyPage>
      }
    ]
  },
  {
    path: "*",
    element: <Navigate to="/app/overview" replace />
  }
]);
