import { Navigate, createBrowserRouter } from "react-router-dom";
import { AppShell } from "@/components/layout/app-shell";
import { AlertCenterPage } from "@/pages/alert-center-page";
import { ProtectedRoute } from "@/components/protected-route";
import { AuditPage } from "@/pages/audit-page";
import { LoginPage } from "@/pages/login-page";
import { LogsPage } from "@/pages/logs-page";
import { NodesPage } from "@/pages/nodes-page";
import { NotificationsPage } from "@/pages/notifications-page";
import { OverviewPage } from "@/pages/overview-page";
import { PoliciesPage } from "@/pages/policies-page";
import { SSHKeysPage } from "@/pages/ssh-keys-page";
import { TasksPage } from "@/pages/tasks-page";

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
        element: <OverviewPage />
      },
      {
        path: "nodes",
        element: <NodesPage />
      },
      {
        path: "ssh-keys",
        element: <SSHKeysPage />
      },
      {
        path: "policies",
        element: <PoliciesPage />
      },
      {
        path: "logs",
        element: <LogsPage />
      },
      {
        path: "notifications",
        element: <NotificationsPage />
      },
      {
        path: "alert-center",
        element: <AlertCenterPage />
      },
      {
        path: "tasks",
        element: <TasksPage />
      },
      {
        path: "audit",
        element: <AuditPage />
      }
    ]
  },
  {
    path: "*",
    element: <Navigate to="/app/overview" replace />
  }
]);
