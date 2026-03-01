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
  return (
    <section className={cn("grid gap-3 sm:grid-cols-2 lg:grid-cols-4", className)}>
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
          <CardHeader className="relative pb-2">
            <CardTitle className="text-sm font-medium tracking-wide text-muted-foreground/90 uppercase">
              {item.title}
            </CardTitle>
          </CardHeader>
          <CardContent className="relative">
            <p className={cn("text-4xl font-extrabold tracking-tight drop-shadow-sm", item.valueClassName)}>
              {item.value}
            </p>
            {item.description ? (
              <p className="mt-1.5 text-xs font-medium text-muted-foreground/80">{item.description}</p>
            ) : null}
          </CardContent>
        </Card>
      ))}
    </section>
  );
}

export type { StatCardItem, StatCardTone };
