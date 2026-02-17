import { Link, useLocation } from "react-router-dom";
import { Layers3, LogOut } from "lucide-react";
import { navItems } from "@/components/layout/navigation";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { ThemeToggle } from "@/components/theme-toggle";
import { DisplayPreferencesToggle } from "@/components/display-preferences-toggle";

type DesktopSidebarProps = {
  username: string | null;
  onLogout: () => void;
};

export function DesktopSidebar({ username, onLogout }: DesktopSidebarProps) {
  const location = useLocation();

  return (
    <aside className="fixed left-0 top-0 z-40 hidden h-screen flex-col border-r border-border/70 bg-card/65 backdrop-blur-xl md:flex md:w-20 md:p-3 lg:w-72 lg:p-4">
      <div className="flex items-center justify-center gap-2 px-1 py-3 lg:justify-between lg:px-2">
        <div className="flex items-center gap-2">
          <img
            src="/xirang-mark.svg"
            alt="XiRang"
            className="size-10 rounded-md border border-primary/35 bg-primary/10 p-1 shadow-sm lg:size-9"
          />
          <div className="hidden lg:block">
            <p className="text-sm text-muted-foreground">XiRang</p>
            <h1 className="text-lg font-semibold">集中备份中枢</h1>
          </div>
        </div>
        <div className="hidden lg:flex lg:items-center lg:gap-1">
          <DisplayPreferencesToggle />
          <ThemeToggle />
        </div>
      </div>

      <div className="mt-2 hidden rounded-lg border border-border/80 bg-background/70 p-3 shadow-sm backdrop-blur lg:block">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <Layers3 className="size-3.5" />
          监控面板已同步桌面/移动导航语义
        </div>
      </div>

      <Separator className="my-3" />

      <nav className="flex flex-1 flex-col gap-1">
        {navItems.map((item) => {
          const active = location.pathname === item.path;
          const Icon = item.icon;
          return (
            <Link
              key={item.path}
              to={item.path}
              title={item.title}
              aria-label={item.title}
              aria-current={active ? "page" : undefined}
              className={cn(
                "flex items-center rounded-lg border py-2 text-sm transition-all duration-200 md:justify-center md:px-2 lg:justify-start lg:gap-3 lg:px-3",
                active
                  ? "border-primary/35 bg-primary/15 text-foreground shadow-[0_0_0_1px_rgba(16,185,129,0.12)_inset]"
                  : "border-transparent text-muted-foreground hover:border-border/70 hover:bg-accent/70 hover:text-foreground"
              )}
            >
              <Icon className="size-4 shrink-0" />
              <span className="hidden lg:inline">{item.title}</span>
            </Link>
          );
        })}
      </nav>

      <Separator className="my-3" />

      <div className="space-y-2 px-1 lg:px-2">
        <p className="hidden text-xs text-muted-foreground lg:block">当前用户：{username ?? "未知"}</p>

        <div className="flex items-center justify-center gap-2 lg:block">
          <div className="lg:hidden">
            <ThemeToggle />
          </div>

          <Button variant="outline" className="hidden w-full lg:inline-flex" onClick={onLogout}>
            退出登录
          </Button>

          <Button
            variant="outline"
            size="icon"
            className="lg:hidden"
            aria-label="退出登录"
            title="退出登录"
            onClick={onLogout}
          >
            <LogOut className="size-4" />
          </Button>
        </div>
      </div>
    </aside>
  );
}
