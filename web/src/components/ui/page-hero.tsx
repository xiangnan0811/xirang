import * as React from "react";
import { cn } from "@/lib/utils";

export interface PageHeroProps {
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
}

export function PageHero({ title, subtitle, actions, className }: PageHeroProps) {
  return (
    <header className={cn("flex flex-col gap-1 sm:flex-row sm:items-start sm:justify-between sm:gap-4", className)}>
      <div className="min-w-0">
        <h1 className="text-[28px] font-semibold leading-tight tracking-[-0.025em] text-foreground">
          {title}
        </h1>
        {subtitle ? (
          <p className="mt-1 text-sm text-muted-foreground">{subtitle}</p>
        ) : null}
      </div>
      {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
    </header>
  );
}
