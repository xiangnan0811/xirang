import * as React from "react";
import { cn } from "@/lib/utils";

export function TableShell({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "overflow-hidden rounded-lg border border-border bg-card shadow-sm",
        className,
      )}
      {...p}
    />
  );
}

export function TableHeaderRow({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "grid border-b border-border px-4 py-2.5 text-micro font-medium uppercase tracking-[0.06em] text-muted-foreground",
        className,
      )}
      {...p}
    />
  );
}

export function TableRow({ className, onClick, onKeyDown, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if ((e.key === "Enter" || e.key === " ") && onClick) {
      e.preventDefault();
      onClick(e as unknown as React.MouseEvent<HTMLDivElement>);
    }
    onKeyDown?.(e);
  };

  return (
    <div
      tabIndex={0}
      role="button"
      onKeyDown={handleKeyDown}
      className={cn(
        "grid items-center border-b border-border/70 px-4 py-2.5 text-sm text-foreground transition-colors hover:bg-secondary/60 last:border-b-0",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
        className,
      )}
      {...p}
    />
  );
}

export function TableRowOffline({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <TableRow className={cn("opacity-55", className)} {...p} />;
}
