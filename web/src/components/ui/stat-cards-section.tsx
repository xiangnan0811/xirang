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
  return (
    <section className={cn("grid gap-3 sm:grid-cols-2 lg:grid-cols-4", className)}>
      {items.map((item) => (
        <Card
          key={item.id ?? item.title}
          className={cn(toneClassMap[item.tone ?? "info"], cardClassName)}
        >
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">
              {item.title}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className={cn("text-3xl font-semibold", item.valueClassName)}>
              {item.value}
            </p>
            {item.description ? (
              <p className="mt-1 text-sm text-muted-foreground">{item.description}</p>
            ) : null}
          </CardContent>
        </Card>
      ))}
    </section>
  );
}

export type { StatCardItem, StatCardTone };
