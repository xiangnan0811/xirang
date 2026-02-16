import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { RefreshCw, Search } from "lucide-react";
import { DesktopSidebar } from "@/components/layout/desktop-sidebar";
import { MobileNavigation } from "@/components/layout/mobile-navigation";
import { navItems } from "@/components/layout/navigation";
import { ScrollToTop } from "@/components/scroll-to-top";
import { ThemeToggle } from "@/components/theme-toggle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/context/auth-context";
import { useConsoleData } from "@/hooks/use-console-data";

export type ConsoleOutletContext = ReturnType<typeof useConsoleData>;

export function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const { username, token, logout } = useAuth();
  const consoleData = useConsoleData(token);

  const currentItem = navItems.find((item) => item.path === location.pathname);

  const handleLogout = () => {
    logout();
    navigate("/login", { replace: true });
  };

  return (
    <div className="min-h-screen md:pl-72">
      <DesktopSidebar username={username} onLogout={handleLogout} />

      <div className="flex min-h-screen flex-1 flex-col">
        <header className="sticky top-0 z-30 border-b bg-background/90 backdrop-blur">
          <div className="flex flex-col gap-3 px-4 py-3 md:px-8">
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                  <img src="/xirang-mark.svg" alt="XiRang" className="size-4 rounded-sm" />
                  XiRang 控制台
                </p>
                <h2 className="text-lg font-semibold">{currentItem?.title ?? "控制台"}</h2>
                <p className="mt-1 text-xs text-muted-foreground">
                  最近同步：{consoleData.lastSyncedAt} · 节点 {consoleData.nodes.length} 台
                </p>
              </div>

              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={consoleData.refresh}
                  className="hidden md:inline-flex"
                >
                  <RefreshCw className="mr-2 size-4" />
                  刷新数据
                </Button>
                <ThemeToggle />
              </div>
            </div>

            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={consoleData.globalSearch}
                onChange={(event) => consoleData.setGlobalSearch(event.target.value)}
                className="pl-9"
                placeholder="全局搜索节点（名称 / IP / 标签 / 状态）"
              />
            </div>
          </div>

          {consoleData.warning ? (
            <div className="border-t bg-amber-500/10 px-4 py-2 text-xs text-amber-600 md:px-8 dark:text-amber-300">
              {consoleData.warning}
            </div>
          ) : null}
        </header>

        <ScrollToTop />
        <main className="flex-1 px-4 py-4 pb-24 md:px-8 md:pb-8">
          <Outlet context={consoleData as ConsoleOutletContext} />
        </main>
      </div>

      <MobileNavigation
        username={username}
        onLogout={handleLogout}
        onRefresh={consoleData.refresh}
      />
    </div>
  );
}
