import { Navigate, createBrowserRouter } from "react-router-dom";
import { canonicalizeBackupLocation } from "@/lib/backup-navigation";
import { AppShell } from "@/components/layout/app-shell";
import { ProtectedRoute } from "@/components/protected-route";
import { LoginPage } from "@/pages/login-page";
import {
  AuditPage,
  AutomationRulesPage,
  CredentialAuditPage,
  CredentialAccessGrantsPage,
  BackupsPage,
  BackupsDataPage,
  BackupsOverviewPage,
  BackupsRecoveryPage,
  CredentialsPage,
  DashboardDetailPage,
  DashboardsPage,
  LazyPage,
  LogsPage,
  MorePage,
  NodesDetailPage,
  NodesPage,
  NotFoundPage,
  NotificationsPage,
  OverviewPage,
  PoliciesPage,
  ReportsPage,
  ServiceMonitorsPage,
  SettingsPage,
  SSHKeysPage,
  StatusPage,
  TasksPage,
} from "@/router-pages";

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
    path: "/status",
    element: <LazyPage><StatusPage /></LazyPage>
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
        path: "dashboards",
        element: <LazyPage><DashboardsPage /></LazyPage>
      },
      {
        path: "dashboards/:id",
        element: <LazyPage><DashboardDetailPage /></LazyPage>
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
        loader: canonicalizeBackupLocation,
        element: <LazyPage><BackupsPage /></LazyPage>,
        children: [
          {
            path: "overview",
            element: <LazyPage><BackupsOverviewPage /></LazyPage>
          },
          {
            path: "data",
            element: <LazyPage><BackupsDataPage /></LazyPage>
          },
          {
            path: "recovery",
            element: <LazyPage><BackupsRecoveryPage /></LazyPage>
          }
        ]
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
        path: "tasks",
        element: <LazyPage><TasksPage /></LazyPage>
      },
      {
        path: "audit",
        element: <LazyPage><AuditPage /></LazyPage>
      },
      {
        path: "credential-audit",
        element: <LazyPage><CredentialAuditPage /></LazyPage>
      },
      {
        path: "credential-access-grants",
        element: <LazyPage><CredentialAccessGrantsPage /></LazyPage>
      },
      {
        path: "reports",
        element: <LazyPage><ReportsPage /></LazyPage>
      },
      {
        path: "credentials",
        element: <LazyPage><CredentialsPage /></LazyPage>
      },
      {
        path: "settings",
        element: <LazyPage><SettingsPage /></LazyPage>
      },
      {
        path: "more",
        element: <LazyPage><MorePage /></LazyPage>
      },
      {
        path: "automation-rules",
        element: <LazyPage><AutomationRulesPage /></LazyPage>
      },
      {
        path: "service-monitors",
        element: <LazyPage><ServiceMonitorsPage /></LazyPage>
      },
      {
        path: "*",
        element: <LazyPage><NotFoundPage /></LazyPage>
      }
    ]
  },
  {
    path: "*",
    element: <LazyPage><NotFoundPage /></LazyPage>
  }
]);
