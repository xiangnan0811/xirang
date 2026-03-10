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

const toneClassMap: Record<StatCardTone, string> = {
  info: "border-info/30 bg-gradient-to-br from-info/10 via-transparent to-transparent",
  success: "border-success/30 bg-gradient-to-br from-success/10 via-transparent to-transparent",
  warning: "border-warning/30 bg-gradient-to-br from-warning/10 via-transparent to-transparent",
  destructive:
    "border-destructive/30 bg-gradient-to-br from-destructive/10 via-transparent to-transparent",
  primary: "border-primary/30 bg-gradient-to-br from-primary/10 via-transparent to-transparent",
};

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
          className={cn(
            "glass-panel text-card-foreground",
            toneClassMap[item.tone ?? "info"],
            cardClassName
          )}
        >
          <div className="flex flex-col space-y-1.5 pb-1 sm:pb-2 px-3 pt-3 sm:px-6 sm:pt-5">
            <h3 className="text-[10px] sm:text-sm font-medium text-muted-foreground truncate">
              {item.title}
            </h3>
          </div>
          <div className="px-3 pb-3 sm:px-6 sm:pb-5">
            <div className="flex items-center gap-1 sm:gap-2">
              <p className={cn("text-lg sm:text-3xl font-semibold", item.valueClassName)}>
                {item.value}
                {item.unit ? (
                  <span className="text-[10px] sm:text-sm font-normal text-muted-foreground ml-0.5 sm:ml-1">
                    {item.unit}
                  </span>
                ) : null}
              </p>
              {item.icon ?? null}
            </div>
            {item.description ? (
              <p className="mt-1 text-sm text-muted-foreground hidden sm:block">{item.description}</p>
            ) : null}
          </div>
        </div>
      ))}
    </section>
  );
}

export type { StatCardItem, StatCardTone };
