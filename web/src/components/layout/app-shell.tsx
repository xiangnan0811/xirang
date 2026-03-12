import { useEffect, useRef, useState } from "react";
import { Outlet, useNavigate } from "react-router-dom";
import { RefreshCw, Search, ShieldCheck, ShieldOff } from "lucide-react";
import { DesktopSidebar } from "@/components/layout/desktop-sidebar";
import { MobileNavigation } from "@/components/layout/mobile-navigation";
import { ScrollToTop } from "@/components/scroll-to-top";
import { ThemeToggle } from "@/components/theme-toggle";
import { DisplayPreferencesToggle } from "@/components/display-preferences-toggle";
import { OnboardingTour } from "@/components/onboarding-tour";
import { NotificationBell } from "@/components/notification-bell";
import { TOTPSetupDialog } from "@/components/totp-setup-dialog";
import { TOTPDisableDialog } from "@/components/totp-disable-dialog";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ErrorBoundary } from "@/components/error-boundary";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/context/auth-context";
import { useConsoleData } from "@/hooks/use-console-data";
import { apiClient } from "@/lib/api/client";

export type ConsoleOutletContext = ReturnType<typeof useConsoleData>;

function isTypingTarget(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  const tagName = target.tagName.toLowerCase();
  return (
    tagName === "input" ||
    tagName === "textarea" ||
    tagName === "select" ||
    target.isContentEditable
  );
}

export function AppShell() {
  const navigate = useNavigate();
  const { username, role, token, logout, totpEnabled, setTotpEnabled } = useAuth();
  const consoleData = useConsoleData(token);

  const globalSearchInputRef = useRef<HTMLInputElement | null>(null);
  const mobileSearchInputRef = useRef<HTMLInputElement | null>(null);
  const globalSearchValueRef = useRef(consoleData.globalSearch);
  const setGlobalSearchRef = useRef(consoleData.setGlobalSearch);
  const [mobileSearchOpen, setMobileSearchOpen] = useState(false);
  const [totpSetupOpen, setTotpSetupOpen] = useState(false);
  const [totpDisableOpen, setTotpDisableOpen] = useState(false);
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

  useEffect(() => {
    globalSearchValueRef.current = consoleData.globalSearch;
    setGlobalSearchRef.current = consoleData.setGlobalSearch;
  }, [consoleData.globalSearch, consoleData.setGlobalSearch]);

  useEffect(() => {
    const handleGlobalSearchShortcut = (event: KeyboardEvent) => {
      const isQuickFocus =
        (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) ||
        (event.key === "/" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey && !isTypingTarget(event.target));

      if (isQuickFocus) {
        event.preventDefault();
        const isDesktop = window.matchMedia("(min-width: 1280px)").matches;
        if (isDesktop) {
          globalSearchInputRef.current?.focus();
          globalSearchInputRef.current?.select();
        } else {
          setMobileSearchOpen(true);
        }
        return;
      }

      const activeElement = document.activeElement;
      if (
        event.key === "Escape" &&
        activeElement === globalSearchInputRef.current &&
        globalSearchValueRef.current
      ) {
        event.preventDefault();
        setGlobalSearchRef.current("");
      }
    };

    window.addEventListener("keydown", handleGlobalSearchShortcut);
    return () => window.removeEventListener("keydown", handleGlobalSearchShortcut);
  }, []);

  useEffect(() => {
    if (!mobileSearchOpen) {
      return;
    }
    const frameId = window.requestAnimationFrame(() => {
      mobileSearchInputRef.current?.focus();
      mobileSearchInputRef.current?.select();
    });
    return () => window.cancelAnimationFrame(frameId);
  }, [mobileSearchOpen]);

  return (
    <div className="min-h-screen app-shell-bg overflow-x-hidden">
      <a
        href="#main-content"
        className="sr-only absolute left-3 top-3 z-[70] rounded-md border border-border/80 bg-background/95 px-3 py-2 text-xs text-foreground shadow-sm focus:not-sr-only"
      >
        跳到主内容
      </a>

      {/* 顶部固定导航栏 */}
      <header
        className={cn(
          "fixed top-0 left-0 right-0 z-50 border-b border-border/70 bg-background/75 backdrop-blur-xl",
          hasWarning ? "h-[92px]" : "h-[60px]"
        )}
      >
        <div className="flex h-[60px] items-center">
          <div className="flex items-center md:hidden px-4">
            <img
              src="/xirang-mark.svg"
              alt="XiRang"
              className="size-8 rounded-md border border-primary/35 bg-primary/10 p-1 shadow-sm"
            />
            <span className="ml-2 text-base font-semibold tracking-tight">息壤</span>
          </div>

          <div
            className={cn(
              "hidden h-full shrink-0 items-center border-r border-border/60 bg-background/35 transition-[width,padding] duration-200 md:flex",
              sidebarCollapsed ? "w-20 justify-center px-3" : "w-64 px-4"
            )}
          >
            <img
              src="/xirang-mark.svg"
              alt="XiRang"
              className="size-8 rounded-md border border-primary/35 bg-primary/10 p-1 shadow-sm"
            />
            {!sidebarCollapsed ? (
              <div className="ml-3 min-w-0 leading-none">
                <span className="block truncate text-base font-semibold tracking-tight">息壤</span>
              </div>
            ) : null}
          </div>

          <div className="flex min-w-0 flex-1 items-center justify-between px-4 lg:px-6">
            <div className="hidden min-w-0 xl:flex items-center gap-3">
              <div className="relative shrink-0">
                <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  ref={globalSearchInputRef}
                  value={consoleData.globalSearch}
                  onChange={(event) => consoleData.setGlobalSearch(event.target.value)}
                  className="h-8 w-60 pl-9 pr-16 bg-background/50 text-xs xl:w-72"
                  aria-label="全局搜索节点（名称、IP、标签、状态）"
                  aria-keyshortcuts="Control+K Meta+K /"
                  placeholder="搜索节点、IP、标签…"
                />
                <span className="pointer-events-none absolute right-2.5 top-1/2 hidden -translate-y-1/2 items-center gap-1 rounded bg-background/80 px-1.5 py-0.5 text-[10px] text-muted-foreground xl:inline-flex border border-border/50 shadow-sm font-mono">
                  ⌘ K
                </span>
              </div>
            </div>

            <div className="hidden min-w-0 md:flex items-center gap-2 mt-0.5 overflow-hidden">
              <Badge variant="success" className="h-6 shrink-0 px-2 text-[10px]">在线 {consoleData.overview.healthyNodes}</Badge>
              <Badge variant="warning" className="h-6 shrink-0 px-2 text-[10px]">运行中 {consoleData.overview.runningTasks}</Badge>
              <Badge variant="danger" className="h-6 shrink-0 px-2 text-[10px]">异常 {consoleData.overview.failedTasks24h}</Badge>
              <div className="h-4 w-px shrink-0 bg-border/50 mx-1" />
              <span className="truncate text-[11px] text-muted-foreground">总数 {consoleData.overview.totalNodes}</span>
            </div>

            <div className="flex items-center gap-1.5 sm:gap-2">
              <Button
                variant="ghost"
                size="icon"
                className="xl:hidden size-8"
                onClick={() => setMobileSearchOpen(true)}
                aria-label="打开全局搜索"
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
                title="刷新数据"
              >
                <RefreshCw className={`size-4 ${consoleData.loading ? "animate-spin" : ""}`} />
              </Button>

              <NotificationBell token={token} />
              <DisplayPreferencesToggle className="hidden md:flex items-center h-8 [&>button]:size-8 [&_svg]:size-4" />
              <div className="flex items-center h-8 [&>button]:size-8 [&_svg]:size-4">
                <ThemeToggle />
              </div>

              <div className="hidden md:flex items-center pl-1 gap-2">
                <span className="text-xs text-muted-foreground">{username ?? "未知"}</span>
                {totpEnabled ? (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-green-600 hover:text-green-700 dark:text-green-500 dark:hover:text-green-400"
                    onClick={() => setTotpDisableOpen(true)}
                    title="禁用两步验证"
                    aria-label="禁用两步验证"
                  >
                    <ShieldCheck className="size-4" />
                  </Button>
                ) : (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:text-foreground"
                    onClick={() => setTotpSetupOpen(true)}
                    title="启用两步验证"
                    aria-label="启用两步验证"
                  >
                    <ShieldOff className="size-4" />
                  </Button>
                )}
                <Button variant="outline" size="sm" className="h-8 text-xs px-3" onClick={handleLogout}>
                  退出登录
                </Button>
              </div>
            </div>
          </div>
        </div>

        {consoleData.warning ? (
          <div
            role="status"
            aria-live="polite"
            className="border-t border-warning/30 bg-warning/10 px-4 py-1.5 text-[11px] text-warning md:px-6"
          >
            {consoleData.warning}
          </div>
        ) : null}
      </header>

      {/* 侧边栏与主区包裹层 */}
      <div className={cn(
        "relative flex w-full transition-all duration-200",
        hasWarning ? "pt-[92px]" : "pt-[60px]",
        sidebarCollapsed ? "md:pl-20" : "md:pl-64"
      )}>
        <DesktopSidebar
          role={role}
          isCollapsed={sidebarCollapsed}
          hasWarning={hasWarning}
          onToggleCollapse={() => setSidebarCollapsed(!sidebarCollapsed)}
        />

        <div className={cn("flex-1 flex flex-col min-w-0", hasWarning ? "min-h-[calc(100vh-92px)]" : "min-h-[calc(100vh-60px)]")}>
          <ScrollToTop />
          <main id="main-content" className="flex-1 w-full max-w-[1680px] px-4 py-5 md:px-6 md:py-6 lg:px-8 pb-24 mx-auto">
            <ErrorBoundary>
              <Outlet context={consoleData as ConsoleOutletContext} />
            </ErrorBoundary>
          </main>
        </div>
      </div>

      <Dialog open={mobileSearchOpen} onOpenChange={setMobileSearchOpen}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>全局搜索节点</DialogTitle>
            <DialogDescription>按名称、IP、标签或状态快速筛选节点。</DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody className="space-y-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                ref={mobileSearchInputRef}
                value={consoleData.globalSearch}
                onChange={(event) => consoleData.setGlobalSearch(event.target.value)}
                className="pl-9"
                aria-label="全局搜索节点（名称、IP、标签、状态）"
                placeholder="搜索节点…"
              />
            </div>
          </DialogBody>
        </DialogContent>
      </Dialog>

      <MobileNavigation
        username={username}
        role={role}
        totpEnabled={totpEnabled}
        onLogout={handleLogout}
        onRefresh={consoleData.refresh}
        onTotpSetup={() => setTotpSetupOpen(true)}
        onTotpDisable={() => setTotpDisableOpen(true)}
      />

      <OnboardingTour />

      {token ? (
        <>
          <TOTPSetupDialog
            open={totpSetupOpen}
            onOpenChange={setTotpSetupOpen}
            token={token}
            onSuccess={() => setTotpEnabled(true)}
          />
          <TOTPDisableDialog
            open={totpDisableOpen}
            onOpenChange={setTotpDisableOpen}
            token={token}
            onSuccess={() => setTotpEnabled(false)}
          />
        </>
      ) : null}
    </div>
  );
}
