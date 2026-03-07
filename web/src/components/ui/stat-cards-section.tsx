import type { ReactNode } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

type StatCardTone = "info" | "success" | "warning" | "destructive" | "primary";

type StatCardItem = {
  id?: string;
  title: string;
  value: ReactNode;
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
  info: "border-info/30 bg-gradient-to-br from-info/10 via-transparent to-transparent text-info group-hover:border-info/50",
  success: "border-success/30 bg-gradient-to-br from-success/10 via-transparent to-transparent text-success group-hover:border-success/50",
  warning: "border-warning/30 bg-gradient-to-br from-warning/10 via-transparent to-transparent text-warning group-hover:border-warning/50",
  destructive:
    "border-destructive/30 bg-gradient-to-br from-destructive/10 via-transparent to-transparent text-destructive group-hover:border-destructive/50",
  primary: "border-primary/30 bg-gradient-to-br from-primary/10 via-transparent to-transparent text-primary group-hover:border-primary/50",
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
        <Card
          key={item.id ?? item.title}
          className={cn(
            "group relative overflow-hidden transition-all duration-300 ease-out hover:-translate-y-0.5 hover:shadow-panel",
            toneClassMap[item.tone ?? "info"],
            cardClassName
          )}
        >
          <div className="absolute inset-0 bg-gradient-to-t from-background/40 to-transparent pointer-events-none" />
          <CardHeader className="relative px-3 pb-1 sm:px-5 sm:pb-2">
            <CardTitle className="truncate text-[10px] font-medium uppercase tracking-wide text-muted-foreground/90 sm:text-sm">
              {item.title}
            </CardTitle>
          </CardHeader>
          <CardContent className="relative px-3 pt-0 sm:px-5">
            <p className={cn("text-lg font-extrabold tracking-tight drop-shadow-sm sm:text-4xl", item.valueClassName)}>
              {item.value}
            </p>
            {item.description ? (
              <p className="mt-1 hidden text-xs font-medium text-muted-foreground/80 sm:block">{item.description}</p>
            ) : null}
          </CardContent>
        </Card>
      ))}
    </section>
  );
}

export type { StatCardItem, StatCardTone };
