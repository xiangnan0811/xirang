import { useMemo, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { LogOut, Menu, RefreshCw, X } from "lucide-react";
import { navItems } from "@/components/layout/navigation";
import { ThemeToggle } from "@/components/theme-toggle";
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
      <nav className="fixed inset-x-0 bottom-0 z-40 border-t bg-background/95 backdrop-blur md:hidden">
        <div
          className="grid h-16"
          style={{ gridTemplateColumns: `repeat(${Math.max(1, mobileTabs.length)}, minmax(0, 1fr))` }}
        >
          {mobileTabs.map((item) => {
            const Icon = item.icon;
            const active = location.pathname === item.path;
            return (
              <button
                key={item.path}
                className={cn(
                  "flex flex-col items-center justify-center gap-1 text-[11px]",
                  active ? "text-primary" : "text-muted-foreground"
                )}
                onClick={() => navigate(item.path)}
              >
                <Icon className="size-4" />
                {item.title}
              </button>
            );
          })}
        </div>
      </nav>

      <button
        className="fixed right-3 top-3 z-50 rounded-full border bg-background/90 p-2 shadow md:hidden"
        onClick={() => setDrawerOpen(true)}
        aria-label="打开快捷菜单"
      >
        <Menu className="size-4" />
      </button>

      {drawerOpen ? (
        <div className="fixed inset-0 z-50 md:hidden">
          <button
            className="absolute inset-0 bg-black/50"
            aria-label="关闭抽屉"
            onClick={() => setDrawerOpen(false)}
          />

          <section className="absolute right-0 top-0 flex h-full w-[84%] flex-col border-l bg-background p-4 shadow-xl">
            <div className="mb-4 flex items-center justify-between">
              <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
                <img src="/xirang-mark.svg" alt="XiRang" className="size-5 rounded-sm" />
                运维快捷操作
              </p>
              <Button variant="ghost" size="icon" onClick={() => setDrawerOpen(false)}>
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
                      "flex items-center gap-2 rounded-md px-3 py-2 text-sm",
                      active ? "bg-primary text-primary-foreground" : "hover:bg-accent"
                    )}
                  >
                    <Icon className="size-4" />
                    {item.title}
                  </Link>
                );
              })}
            </div>

            <div className="mt-6 grid grid-cols-2 gap-2">
              <Button variant="outline" onClick={onRefresh}>
                <RefreshCw className="mr-1 size-4" />
                刷新
              </Button>
              <Button
                variant="destructive"
                onClick={() => {
                  onLogout();
                  setDrawerOpen(false);
                }}
              >
                <LogOut className="mr-1 size-4" />
                退出
              </Button>
            </div>

            <div className="mt-auto flex items-center justify-between border-t pt-4">
              <p className="text-xs text-muted-foreground">当前用户：{username ?? "未知"}</p>
              <ThemeToggle />
            </div>
          </section>
        </div>
      ) : null}
    </>
  );
}
