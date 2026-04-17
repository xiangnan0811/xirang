import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

type StatCardTone = "info" | "success" | "warning" | "destructive" | "primary";

type StatCardItem = {
  id?: string;
  title: string;
  value: ReactNode;
  /** Small label rendered right after the value (e.g. "%", "Mbps"). */
  unit?: string;
  /** Optional icon rendered next to the value. */
  icon?: ReactNode;
  description?: ReactNode;
  tone?: StatCardTone;
  valueClassName?: string;
};

type StatCardsSectionProps = {
  items: StatCardItem[];
  className?: string;
  cardClassName?: string;
};

function toneTextClass(tone: StatCardTone | undefined): string {
  switch (tone) {
    case "success":
      return "text-[hsl(var(--success))]";
    case "warning":
      return "text-[hsl(var(--warning))]";
    case "destructive":
      return "text-[hsl(var(--destructive))]";
    case "info":
      return "text-[hsl(var(--info))]";
    case "primary":
      return "text-[hsl(var(--primary))]";
    default:
      return "text-muted-foreground";
  }
}

export function StatCardsSection({
  items,
  className,
  cardClassName,
}: StatCardsSectionProps) {
  const columnCount = Math.max(items.length, 1);

  return (
    <section
      className={cn("grid gap-1.5 sm:gap-3", className)}
      style={{ gridTemplateColumns: `repeat(${columnCount}, minmax(0, 1fr))` }}
    >
      {items.map((item) => (
        <div
          key={item.id ?? item.title}
          data-tone={item.tone ?? "info"}
          className={cn(
            "rounded-lg bg-card p-5 shadow-sm dark:border dark:border-border",
            cardClassName
          )}
        >
          <div className="flex items-center justify-between">
            <div className="text-[10px] font-medium uppercase tracking-[0.06em] text-muted-foreground">
              {item.title}
            </div>
            {item.icon ? (
              <div className="text-muted-foreground">{item.icon}</div>
            ) : null}
          </div>
          <div
            className={cn(
              "mt-3 text-[28px] font-semibold tabular-nums leading-none tracking-[-0.025em] text-foreground",
              item.valueClassName
            )}
          >
            {item.value}
            {item.unit ? (
              <span className="ml-1 text-sm font-medium text-muted-foreground">
                {item.unit}
              </span>
            ) : null}
          </div>
          {item.description ? (
            <div className={cn("mt-2 text-xs font-medium", toneTextClass(item.tone))}>
              {item.description}
            </div>
          ) : null}
        </div>
      ))}
    </section>
  );
}

export type { StatCardItem, StatCardTone };
