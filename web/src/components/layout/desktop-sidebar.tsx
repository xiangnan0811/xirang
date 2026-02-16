import { Link, useLocation } from "react-router-dom";
import { Layers3 } from "lucide-react";
import { navItems } from "@/components/layout/navigation";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { ThemeToggle } from "@/components/theme-toggle";

type DesktopSidebarProps = {
  username: string | null;
  onLogout: () => void;
};

export function DesktopSidebar({ username, onLogout }: DesktopSidebarProps) {
  const location = useLocation();

  return (
    <aside className="fixed left-0 top-0 z-40 hidden h-screen w-72 flex-col border-r border-border/70 bg-card/65 p-4 backdrop-blur-xl md:flex">
      <div className="flex items-center justify-between gap-2 px-2 py-3">
        <div className="flex items-center gap-2">
          <img
            src="/xirang-mark.svg"
            alt="XiRang"
            className="size-9 rounded-md border border-primary/35 bg-primary/10 p-1 shadow-sm"
          />
          <div>
            <p className="text-sm text-muted-foreground">XiRang</p>
            <h1 className="text-lg font-semibold">集中备份中枢</h1>
          </div>
        </div>
        <ThemeToggle />
      </div>

      <div className="mt-2 rounded-lg border border-border/80 bg-background/70 p-3 shadow-sm backdrop-blur">
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
              aria-current={active ? "page" : undefined}
              className={cn(
                "flex items-center gap-3 rounded-lg border px-3 py-2 text-sm transition-all duration-200",
                active
                  ? "border-primary/35 bg-primary/15 text-foreground shadow-[0_0_0_1px_rgba(16,185,129,0.12)_inset]"
                  : "border-transparent text-muted-foreground hover:border-border/70 hover:bg-accent/70 hover:text-foreground"
              )}
            >
              <Icon className="size-4" />
              {item.title}
            </Link>
          );
        })}
      </nav>

      <Separator className="my-3" />

      <div className="space-y-2 px-2">
        <p className="text-xs text-muted-foreground">当前用户：{username ?? "未知"}</p>
        <Button variant="outline" className="w-full" onClick={onLogout}>
          退出登录
        </Button>
      </div>
    </aside>
  );
}
