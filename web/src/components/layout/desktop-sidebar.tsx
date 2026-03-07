import { Link, useLocation } from "react-router-dom";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { getVisibleNavItems } from "@/components/layout/navigation";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import type { UserRecord } from "@/types/domain";

type DesktopSidebarProps = {
  role: UserRecord["role"] | null;
  isCollapsed: boolean;
  hasWarning?: boolean;
  onToggleCollapse: () => void;
};

export function DesktopSidebar({ role, isCollapsed, hasWarning = false, onToggleCollapse }: DesktopSidebarProps) {
  const location = useLocation();
  const navItems = getVisibleNavItems(role);
  const navItemBaseClass =
    "flex items-center rounded-lg border py-2 text-sm transition-all duration-200 md:justify-center md:px-2 lg:justify-start lg:gap-3 lg:px-3";
  const navItemActiveClass =
    "border-primary/35 bg-[hsl(var(--nav-active))] text-[hsl(var(--nav-active-foreground))] shadow-[inset_0_0_0_1px_rgba(16,185,129,0.22)]";
  const navItemIdleClass =
    "border-transparent text-muted-foreground transition-all duration-200 ease-out hover:border-border/70 hover:bg-background/70 hover:text-foreground";

  return (
    <aside
      className={cn(
        "fixed left-0 top-0 z-40 hidden h-screen flex-col border-r border-border/70 bg-card/65 backdrop-blur-xl md:flex pb-4 transition-[width] duration-200",
        hasWarning ? "pt-[92px]" : "pt-[60px]",
        isCollapsed ? "w-20 px-2" : "w-64 px-4"
      )}
    >
      <nav className="flex flex-1 flex-col gap-1.5 pt-2 overflow-y-auto thin-scrollbar pb-2">
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
                navItemBaseClass,
                isCollapsed ? "justify-center px-2" : "",
                active ? navItemActiveClass : navItemIdleClass
              )}
            >
              <Icon className={cn("shrink-0", isCollapsed ? "size-5" : "size-4")} />
              <span className={cn("hidden", isCollapsed ? "" : "md:inline text-[13px] font-medium")}>{item.title}</span>
            </Link>
          );
        })}
      </nav>

      <Separator className="my-3" />

      <div className={cn("shrink-0", isCollapsed ? "px-0 flex justify-center" : "px-0")}>
        <Button
          variant="ghost"
          size={isCollapsed ? "icon" : "default"}
          className={cn("text-muted-foreground", isCollapsed ? "size-10" : "w-full justify-start px-3 h-10")}
          onClick={onToggleCollapse}
          aria-label={isCollapsed ? "展开侧边栏" : "收起侧边栏"}
          title={isCollapsed ? "展开侧边栏" : "收起侧边栏"}
        >
          {isCollapsed ? (
            <PanelLeftOpen className="size-5" />
          ) : (
            <>
              <PanelLeftClose className="mr-3 size-[18px]" />
              <span className="text-[13px] font-medium">收起面板</span>
            </>
          )}
        </Button>
      </div>
    </aside>
  );
}
