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

const toneClassMap: Record<StatCardTone, { text: string; bg: string; line: string }> = {
  info: { text: "text-info", bg: "bg-info/10", line: "bg-info" },
  success: { text: "text-success", bg: "bg-success/10", line: "bg-success" },
  warning: { text: "text-warning", bg: "bg-warning/10", line: "bg-warning" },
  destructive: { text: "text-destructive", bg: "bg-destructive/10", line: "bg-destructive" },
  primary: { text: "text-primary", bg: "bg-primary/10", line: "bg-primary" },
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
      {items.map((item) => {
        const s = toneClassMap[item.tone ?? "info"];
        return (
          <div
            key={item.id ?? item.title}
            data-tone={item.tone ?? "info"}
            className={cn(
              "glass-panel border-border/70 overflow-hidden relative group",
              cardClassName
            )}
          >
            <div className={`absolute top-0 left-0 w-1 h-full ${s.line} opacity-60 group-hover:opacity-100 transition-opacity`} />
            <div className="p-3 sm:p-4 flex items-center gap-2.5 sm:gap-3 pl-4 sm:pl-5">
              {item.icon && (
                <div className={`flex items-center justify-center rounded-lg p-2 sm:p-2.5 ${s.bg} ${s.text}`}>
                  {item.icon}
                </div>
              )}
              <div className="min-w-0 flex-1">
                <div className="flex items-baseline gap-1.5">
                  <div className={cn("text-xl sm:text-2xl font-bold font-mono tracking-tight text-foreground/90", item.valueClassName)}>
                    {item.value}
                  </div>
                  {item.unit ? (
                    <span className="text-[10px] sm:text-xs font-medium text-muted-foreground uppercase">
                      {item.unit}
                    </span>
                  ) : null}
                </div>
                <div className="text-[10px] sm:text-[11px] font-medium text-muted-foreground uppercase tracking-wider truncate" title={item.title}>
                  {item.title}
                </div>
                {item.description ? (
                  <div className="text-[10px] text-muted-foreground/80 truncate mt-0.5 sm:mt-1 hidden sm:block">
                    {item.description}
                  </div>
                ) : null}
              </div>
            </div>
          </div>
        );
      })}
    </section>
  );
}

export type { StatCardItem, StatCardTone };
