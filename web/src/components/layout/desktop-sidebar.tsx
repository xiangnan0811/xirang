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
    <aside className="fixed left-0 top-0 z-40 hidden h-screen w-72 flex-col border-r bg-card/70 p-4 backdrop-blur md:flex">
      <div className="flex items-center justify-between gap-2 px-2 py-3">
        <div className="flex items-center gap-2">
          <img src="/xirang-mark.svg" alt="XiRang" className="size-9 rounded-md border border-primary/30 bg-primary/5 p-1" />
          <div>
            <p className="text-sm text-muted-foreground">XiRang</p>
            <h1 className="text-lg font-semibold">集中备份中枢</h1>
          </div>
        </div>
        <ThemeToggle />
      </div>

      <div className="mt-2 rounded-lg border bg-background/80 p-3">
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
              className={cn(
                "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                active
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
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
