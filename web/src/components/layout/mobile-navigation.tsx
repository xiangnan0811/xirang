import { useMemo, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { LogOut, Menu, RefreshCw, X } from "lucide-react";
import { navItems } from "@/components/layout/navigation";
import { ThemeToggle } from "@/components/theme-toggle";
import { DisplayPreferencesToggle } from "@/components/display-preferences-toggle";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type MobileNavigationProps = {
  username: string | null;
  onLogout: () => void;
  onRefresh: () => void;
};

export function MobileNavigation({ username, onLogout, onRefresh }: MobileNavigationProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const [drawerOpen, setDrawerOpen] = useState(false);

  const mobileTabs = useMemo(() => navItems.filter((item) => item.mobileTab !== false), []);

  return (
    <>
      <nav className="fixed inset-x-0 bottom-0 z-40 border-t border-border/75 bg-background/88 pb-[env(safe-area-inset-bottom)] backdrop-blur-xl md:hidden">
        <div
          className="grid h-[68px]"
          style={{ gridTemplateColumns: `repeat(${Math.max(1, mobileTabs.length)}, minmax(0, 1fr))` }}
        >
          {mobileTabs.map((item) => {
            const Icon = item.icon;
            const active = location.pathname === item.path;
            return (
              <button
                key={item.path}
                className={cn(
                  "flex min-h-14 flex-col items-center justify-center gap-1 px-1 text-[11px] transition-colors",
                  active ? "text-primary" : "text-muted-foreground"
                )}
                onClick={() => navigate(item.path)}
                aria-label={`切换到${item.title}`}
                aria-current={active ? "page" : undefined}
              >
                <span className={cn("rounded-full px-2 py-0.5", active ? "bg-primary/15" : "bg-transparent")}>
                  <Icon className="size-4" />
                </span>
                {item.title}
              </button>
            );
          })}
        </div>
      </nav>

      <button
        className="fixed right-3 top-[calc(env(safe-area-inset-top)+0.75rem)] z-50 rounded-full border border-border/80 bg-background/85 p-2.5 shadow-panel md:hidden"
        onClick={() => setDrawerOpen(true)}
        aria-label="打开快捷菜单"
      >
        <Menu className="size-5" />
      </button>

      {drawerOpen ? (
        <div className="fixed inset-0 z-50 md:hidden">
          <button
            className="absolute inset-0 bg-black/50"
            aria-label="关闭抽屉"
            onClick={() => setDrawerOpen(false)}
          />

          <section className="absolute right-0 top-0 flex h-full w-[84%] flex-col border-l border-border/75 bg-background/95 p-4 shadow-panel thin-scrollbar overflow-y-auto">
            <div className="mb-4 flex items-center justify-between">
              <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
                <img src="/xirang-mark.svg" alt="XiRang" className="size-5 rounded-sm" />
                运维快捷操作
              </p>
              <Button variant="ghost" size="icon" aria-label="关闭快捷菜单" title="关闭快捷菜单" onClick={() => setDrawerOpen(false)}>
                <X className="size-5" />
              </Button>
            </div>

            <div className="space-y-2">
              {navItems.map((item) => {
                const Icon = item.icon;
                const active = location.pathname === item.path;
                return (
                  <Link
                    key={item.path}
                    to={item.path}
                    onClick={() => setDrawerOpen(false)}
                    className={cn(
                      "flex items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-all duration-200",
                      active
                        ? "border-primary/35 bg-primary/15 text-foreground"
                        : "border-transparent hover:border-border/70 hover:bg-accent/65"
                    )}
                  >
                    <Icon className="size-4" />
                    {item.title}
                  </Link>
                );
              })}
            </div>

            <div className="mt-6 grid grid-cols-2 gap-2">
              <Button variant="outline" className="h-10" onClick={onRefresh}>
                <RefreshCw className="mr-1 size-4" />
                刷新
              </Button>
              <Button
                variant="destructive"
                className="h-10"
                onClick={() => {
                  onLogout();
                  setDrawerOpen(false);
                }}
              >
                <LogOut className="mr-1 size-4" />
                退出
              </Button>
            </div>

            <div className="mt-auto flex items-center justify-between border-t border-border/80 pt-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
              <p className="text-xs text-muted-foreground">当前用户：{username ?? "未知"}</p>
              <div className="flex items-center gap-1">
                <DisplayPreferencesToggle />
                <ThemeToggle />
              </div>
            </div>
          </section>
        </div>
      ) : null}
    </>
  );
}
