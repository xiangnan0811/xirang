import { useEffect, useRef, useState } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { RefreshCw, Search } from "lucide-react";
import { DesktopSidebar } from "@/components/layout/desktop-sidebar";
import { MobileNavigation } from "@/components/layout/mobile-navigation";
import { getVisibleNavItems } from "@/components/layout/navigation";
import { ScrollToTop } from "@/components/scroll-to-top";
import { ThemeToggle } from "@/components/theme-toggle";
import { DisplayPreferencesToggle } from "@/components/display-preferences-toggle";
import { OnboardingTour } from "@/components/onboarding-tour";

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
  const location = useLocation();
  const navigate = useNavigate();
  const { username, role, token, logout } = useAuth();
  const consoleData = useConsoleData(token);

  const navItems = getVisibleNavItems(role);
  const currentItem = navItems.find((item) => item.path === location.pathname);
  const globalSearchInputRef = useRef<HTMLInputElement | null>(null);
  const mobileSearchInputRef = useRef<HTMLInputElement | null>(null);
  const globalSearchValueRef = useRef(consoleData.globalSearch);
  const setGlobalSearchRef = useRef(consoleData.setGlobalSearch);
  const [mobileSearchOpen, setMobileSearchOpen] = useState(false);

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
        const isDesktop = window.matchMedia("(min-width: 768px)").matches;
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
    <div className="app-shell-bg min-h-screen md:pl-20 lg:pl-72">
      <a
        href="#main-content"
        className="sr-only absolute left-3 top-3 z-[70] rounded-md border border-border/80 bg-background/95 px-3 py-2 text-xs text-foreground shadow-sm focus:not-sr-only"
      >
        跳到主内容
      </a>
      <DesktopSidebar username={username} role={role} onLogout={handleLogout} />

      <div className="relative z-10 flex min-h-screen flex-1 flex-col">
        <header className="sticky top-0 z-30 border-b border-border/70 bg-background/75 backdrop-blur-xl">
          <div className="mx-auto w-full max-w-[1680px] px-4 py-3 md:px-6 lg:px-8">
            <div className="flex flex-col gap-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <p className="text-[11px] font-medium uppercase tracking-[0.18em] text-muted-foreground/85">
                    XiRang Console
                  </p>
                  <h1 className="mt-1 truncate text-xl font-semibold tracking-tight">
                    {currentItem?.title ?? "控制台"}
                  </h1>
                </div>

                <div className="mr-12 flex shrink-0 items-center gap-2 md:mr-0">
                  <div className="relative hidden md:block">
                    <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                    <Input
                      ref={globalSearchInputRef}
                      value={consoleData.globalSearch}
                      onChange={(event) => consoleData.setGlobalSearch(event.target.value)}
                      className="w-60 pl-9 pr-24 lg:w-72"
                      aria-label="全局搜索节点（名称、IP、标签、状态）"
                      aria-keyshortcuts="Control+K Meta+K /"
                      placeholder="搜索节点、IP、标签…"
                    />
                    <span className="pointer-events-none absolute right-3 top-1/2 hidden -translate-y-1/2 items-center gap-1 rounded-md border border-border/70 bg-background/80 px-2 py-0.5 text-[10px] text-muted-foreground lg:inline-flex">
                      ⌘/Ctrl + K
                    </span>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={consoleData.refresh}
                    className="hidden md:inline-flex"
                    disabled={consoleData.loading}
                    aria-busy={consoleData.loading}
                  >
                    <RefreshCw className={`mr-2 size-4 ${consoleData.loading ? "animate-spin" : ""}`} />
                    {consoleData.loading ? "刷新中" : "刷新"}
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="md:hidden"
                    onClick={() => setMobileSearchOpen(true)}
                    aria-label="打开全局搜索"
                    title="打开全局搜索"
                  >
                    <Search className="size-5" />
                  </Button>
                  <DisplayPreferencesToggle className="hidden md:flex" />
                  <ThemeToggle />
                </div>
              </div>

              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span className="rounded-full border border-border/70 bg-background/70 px-2.5 py-1">
                  最近同步：{consoleData.lastSyncedAt}
                </span>
                <span className="rounded-full border border-border/70 bg-background/70 px-2.5 py-1">
                  节点总数：{consoleData.nodes.length}
                </span>
                <div className="flex items-center gap-1.5">
                  <Badge variant="success">在线 {consoleData.overview.healthyNodes}</Badge>
                  <Badge variant="warning">运行中 {consoleData.overview.runningTasks}</Badge>
                  <Badge variant="danger">失败 24h {consoleData.overview.failedTasks24h}</Badge>
                </div>
              </div>
            </div>
          </div>

          {consoleData.warning ? (
            <div
              role="status"
              aria-live="polite"
              className="border-t border-warning/30 bg-warning/10 px-4 py-2 text-xs text-warning md:px-6 lg:px-8"
            >
              {consoleData.warning}
            </div>
          ) : null}
        </header>

        <Dialog open={mobileSearchOpen} onOpenChange={setMobileSearchOpen}>
          <DialogContent size="sm">
            <DialogHeader>
              <DialogTitle>全局搜索节点</DialogTitle>
              <DialogDescription>
                按名称、IP、标签或状态快速筛选节点。
              </DialogDescription>
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

        <ScrollToTop />
        <main id="main-content" className="mx-auto flex-1 w-full max-w-[1680px] px-4 py-4 pb-24 md:px-6 md:pb-8 lg:px-8">
          <ErrorBoundary>
            <Outlet context={consoleData as ConsoleOutletContext} />
          </ErrorBoundary>
        </main>
      </div>

      <MobileNavigation
        username={username}
        role={role}
        onLogout={handleLogout}
        onRefresh={consoleData.refresh}
      />

      <OnboardingTour />
    </div>
  );
}
