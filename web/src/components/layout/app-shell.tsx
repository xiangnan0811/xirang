import React, { Suspense } from "react";
import { useLocation, useNavigate, useOutlet } from "react-router-dom";
import { AnimatePresence, motion, useReducedMotion } from "framer-motion";
import { useTranslation } from "react-i18next";
import { RefreshCw, Search } from "lucide-react";
import { DesktopSidebar } from "@/components/layout/desktop-sidebar";
import { MobileNavigation } from "@/components/layout/mobile-navigation";
import { ScrollToTop } from "@/components/scroll-to-top";
import { ThemeToggle } from "@/components/theme-toggle";
import { DisplayPreferencesToggle } from "@/components/display-preferences-toggle";
import { LanguageSwitcher } from "@/components/language-switcher";
const SetupWizard = React.lazy(() =>
  import("@/components/setup-wizard").then(m => ({ default: m.SetupWizard }))
);
import { NotificationBell } from "@/components/notification-bell";
import { UserDropdown } from "@/components/user-dropdown";
import { VersionBanner } from "@/components/version-banner";

import { Button } from "@/components/ui/button";
import { ErrorBoundary } from "@/components/error-boundary";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { cn } from "@/lib/utils";
import { useAuth } from "@/context/auth-context";
import { SharedContextProvider } from "@/context/shared-context";
import { NodesContextProvider } from "@/context/nodes-context";
import { TasksContextProvider } from "@/context/tasks-context";
import { PoliciesContextProvider } from "@/context/policies-context";
import { AlertsContextProvider } from "@/context/alerts-context";
import { IntegrationsContextProvider } from "@/context/integrations-context";
import { SSHKeysContextProvider } from "@/context/ssh-keys-context";
import { useConsoleData } from "@/hooks/use-console-data";
import { apiClient } from "@/lib/api/client";
import { CommandPaletteProvider, useCommandPalette } from "@/context/command-palette-context";
import { CommandPalette } from "@/components/ui/command-palette";

function AnimatedOutlet() {
  const location = useLocation();
  const reduced = useReducedMotion();
  // useOutlet() captures the current outlet element so AnimatePresence can
  // hold on to the exiting page while the entering page mounts.
  const outlet = useOutlet();

  return (
    <AnimatePresence mode="wait" initial={false}>
      <motion.div
        key={location.pathname}
        initial={reduced ? false : { opacity: 0, y: 8 }}
        animate={{ opacity: 1, y: 0 }}
        exit={reduced ? { opacity: 0 } : { opacity: 0 }}
        transition={{ duration: reduced ? 0 : 0.2, ease: [0, 0, 0.2, 1] }}
      >
        {outlet}
      </motion.div>
    </AnimatePresence>
  );
}


function AppShellInner() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { username, role, token, logout } = useAuth();
  const consoleData = useConsoleData(token);
  const cmdPalette = useCommandPalette();

  const [sidebarCollapsed, setSidebarCollapsed] = usePersistentState<boolean>("xirang.sidebar.collapsed", false);
  const hasWarning = Boolean(consoleData.warning);

  const handleLogout = async () => {
    if (token) {
      try {
        await apiClient.logout(token);
      } catch {
        // 即便服务端注销失败，也执行本地会话清理，避免前端残留登录态。
      }
    }
    logout();
    navigate("/login", { replace: true });
  };

  return (
    <div className="min-h-screen app-shell-bg overflow-x-hidden">
      <VersionBanner />
      <a
        href="#main-content"
        className="sr-only absolute left-3 top-3 z-[70] rounded-md border border-border/80 bg-background/95 px-3 py-2 text-xs text-foreground shadow-sm focus:not-sr-only"
      >
        {t('appShell.skipToContent')}
      </a>

      {/* 顶部固定导航栏 */}
      <header
        className={cn(
          "fixed top-0 left-0 right-0 z-50 border-b border-border bg-background",
          hasWarning ? "h-[88px]" : "h-14"
        )}
      >
        <div className="flex h-14 items-center">
          <div className="flex items-center md:hidden px-4">
            <img
              src="/xirang-mark.svg"
              alt="XiRang"
              className="size-8 rounded-md border border-primary/35 bg-primary/10 p-1 shadow-sm"
            />
            <span className="ml-2 text-base font-semibold tracking-tight">{t('appShell.brandName')}</span>
          </div>

          <div
            className={cn(
              "hidden h-full shrink-0 items-center border-r border-border transition-[width,padding] duration-200 md:flex",
              sidebarCollapsed ? "w-16 justify-center px-3" : "w-60 px-4"
            )}
          >
            <img
              src="/xirang-mark.svg"
              alt="XiRang"
              className="size-8 rounded-md border border-primary/35 bg-primary/10 p-1 shadow-sm"
            />
            {!sidebarCollapsed ? (
              <div className="ml-3 min-w-0 leading-none">
                <span className="block truncate text-base font-semibold tracking-tight">{t('appShell.brandName')}</span>
              </div>
            ) : null}
          </div>

          <div className="flex min-w-0 flex-1 items-center justify-between px-4 lg:px-6">
            {/* 搜索按钮（md+ 可见），点击打开命令面板 */}
            <div className="hidden min-w-0 md:flex items-center gap-3">
              <button
                type="button"
                onClick={cmdPalette.toggle}
                aria-label={t('search.openLabel')}
                className="hidden w-[280px] items-center gap-2 rounded-md border border-border bg-card/80 px-3 py-1.5 text-xs text-muted-foreground hover:bg-accent md:flex"
              >
                <Search className="size-3.5 shrink-0" aria-hidden />
                <span className="flex-1 text-left">{t('search.placeholder')}</span>
                <kbd className="rounded border border-border bg-background px-1.5 py-[2px] font-mono text-micro">
                  {t('search.kbd')}
                </kbd>
              </button>
            </div>

            <div className="flex items-center gap-1.5 sm:gap-2">
              {/* 移动端搜索图标按钮 */}
              <Button
                variant="ghost"
                size="icon"
                className="md:hidden size-8"
                onClick={cmdPalette.toggle}
                aria-label={t('search.openLabel')}
              >
                <Search className="size-4" />
              </Button>

              <Button
                variant="ghost"
                size="icon"
                onClick={consoleData.refresh}
                className="size-8 text-muted-foreground hover:text-foreground"
                disabled={consoleData.loading}
                aria-busy={consoleData.loading}
                title={t('appShell.refreshData')}
                aria-label={t('appShell.refreshData')}
              >
                <RefreshCw className={`size-4 ${consoleData.loading ? "animate-spin" : ""}`} />
              </Button>

              <NotificationBell token={token} />
              <DisplayPreferencesToggle className="hidden md:flex items-center h-8 [&>button]:size-8 [&_svg]:size-4" />
              <div className="flex items-center h-8 [&>button]:size-8 [&_svg]:size-4">
                <ThemeToggle />
              </div>
              <LanguageSwitcher className="size-8" />

              <div className="hidden md:flex items-center pl-1">
                <UserDropdown />
              </div>
            </div>
          </div>
        </div>

        {consoleData.warning ? (
          <div
            role="status"
            aria-live="polite"
            className="border-t border-warning/30 bg-warning/10 px-4 py-1.5 text-mini text-warning md:px-6"
          >
            {consoleData.warning}
          </div>
        ) : null}
      </header>

      {/* 侧边栏与主区包裹层 */}
      <div className={cn(
        "relative flex w-full transition-[padding,margin] duration-200",
        hasWarning ? "pt-[88px]" : "pt-14",
        sidebarCollapsed ? "md:pl-16" : "md:pl-60"
      )}>
        <DesktopSidebar
          role={role}
          isCollapsed={sidebarCollapsed}
          hasWarning={hasWarning}
          onToggleCollapse={() => setSidebarCollapsed(!sidebarCollapsed)}
        />

        <div className={cn("flex-1 flex flex-col min-w-0", hasWarning ? "min-h-[calc(100vh-88px)]" : "min-h-[calc(100vh-56px)]")}>
          <ScrollToTop />
          <main id="main-content" className="flex-1 w-full max-w-[1680px] px-4 py-5 md:px-6 md:py-6 lg:px-8 pb-24 mx-auto">
            <SharedContextProvider value={{
              loading: consoleData.loading,
              warning: consoleData.warning,
              lastSyncedAt: consoleData.lastSyncedAt,
              refreshVersion: consoleData.refreshVersion,
              globalSearch: consoleData.globalSearch,
              setGlobalSearch: consoleData.setGlobalSearch,
              refresh: consoleData.refresh,
              overview: consoleData.overview,
              fetchOverviewTraffic: consoleData.fetchOverviewTraffic,
            }}>
            <NodesContextProvider value={{
              nodes: consoleData.nodes,
              refreshNodes: consoleData.refreshNodes,
              createNode: consoleData.createNode,
              updateNode: consoleData.updateNode,
              deleteNode: consoleData.deleteNode,
              deleteNodes: consoleData.deleteNodes,
              testNodeConnection: consoleData.testNodeConnection,
              triggerNodeBackup: consoleData.triggerNodeBackup,
            }}>
            <TasksContextProvider value={{
              tasks: consoleData.tasks,
              refreshTasks: consoleData.refreshTasks,
              createTask: consoleData.createTask,
              updateTask: consoleData.updateTask,
              deleteTask: consoleData.deleteTask,
              triggerTask: consoleData.triggerTask,
              cancelTask: consoleData.cancelTask,
              retryTask: consoleData.retryTask,
              pauseTask: consoleData.pauseTask,
              resumeTask: consoleData.resumeTask,
              skipNextTask: consoleData.skipNextTask,
              refreshTask: consoleData.refreshTask,
              fetchTaskLogs: consoleData.fetchTaskLogs,
            }}>
            <PoliciesContextProvider value={{
              policies: consoleData.policies,
              refreshPolicies: consoleData.refreshPolicies,
              createPolicy: consoleData.createPolicy,
              updatePolicy: consoleData.updatePolicy,
              deletePolicy: consoleData.deletePolicy,
              togglePolicy: consoleData.togglePolicy,
              updatePolicySchedule: consoleData.updatePolicySchedule,
            }}>
            <AlertsContextProvider value={{
              alerts: consoleData.alerts,
              retryAlert: consoleData.retryAlert,
              acknowledgeAlert: consoleData.acknowledgeAlert,
              resolveAlert: consoleData.resolveAlert,
              fetchAlertDeliveries: consoleData.fetchAlertDeliveries,
              fetchAlertDeliveryStats: consoleData.fetchAlertDeliveryStats,
              retryAlertDelivery: consoleData.retryAlertDelivery,
              retryFailedAlertDeliveries: consoleData.retryFailedAlertDeliveries,
            }}>
            <IntegrationsContextProvider value={{
              integrations: consoleData.integrations,
              refreshIntegrations: consoleData.refreshIntegrations,
              addIntegration: consoleData.addIntegration,
              removeIntegration: consoleData.removeIntegration,
              toggleIntegration: consoleData.toggleIntegration,
              updateIntegration: consoleData.updateIntegration,
              patchIntegration: consoleData.patchIntegration,
              testIntegration: consoleData.testIntegration,
            }}>
            <SSHKeysContextProvider value={{
              sshKeys: consoleData.sshKeys,
              refreshSSHKeys: consoleData.refreshSSHKeys,
              createSSHKey: consoleData.createSSHKey,
              updateSSHKey: consoleData.updateSSHKey,
              deleteSSHKey: consoleData.deleteSSHKey,
            }}>
              <ErrorBoundary>
                <AnimatedOutlet />
              </ErrorBoundary>
            </SSHKeysContextProvider>
            </IntegrationsContextProvider>
            </AlertsContextProvider>
            </PoliciesContextProvider>
            </TasksContextProvider>
            </NodesContextProvider>
            </SharedContextProvider>
          </main>
        </div>
      </div>

      <MobileNavigation
        username={username}
        role={role}
        onLogout={handleLogout}
        onRefresh={consoleData.refresh}
      />

      <Suspense fallback={null}>
        <SetupWizard />
      </Suspense>

      <CommandPalette />
    </div>
  );
}

export function AppShell() {
  return (
    <CommandPaletteProvider>
      <AppShellInner />
    </CommandPaletteProvider>
  );
}
