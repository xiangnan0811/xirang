import type { ReactNode } from "react";
import { motion } from "framer-motion";
import { cn } from "@/lib/utils";

const staggerContainer = {
  animate: { transition: { staggerChildren: 0.06 } },
};

const staggerItem = {
  initial: { opacity: 0, y: 12 },
  animate: {
    opacity: 1,
    y: 0,
    transition: { duration: 0.3, ease: [0.22, 1, 0.36, 1] as const },
  },
};

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
  compact?: boolean;
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
  compact = false,
}: StatCardsSectionProps) {
  return (
    <motion.section
      initial="initial"
      animate="animate"
      variants={staggerContainer}
      className={cn(
        "grid gap-2 sm:grid-cols-2 xl:grid-cols-4",
        compact ? "grid-cols-2" : "grid-cols-1",
        items.length >= 5 && "2xl:grid-cols-5",
        className
      )}
    >
      {items.map((item) => (
        <motion.div
          key={item.id ?? item.title}
          variants={staggerItem}
          whileHover={{ y: -4 }}
          data-tone={item.tone ?? "info"}
          className={cn(
            "rounded-lg border border-border bg-card shadow-sm transition-[border-color,box-shadow] duration-200 ease-out hover:border-primary/30 hover:shadow-[var(--shadow-panel-hover)]",
            compact ? "p-3" : "p-4",
            cardClassName
          )}
        >
          <div className="flex items-center justify-between">
            <div className="text-mini font-medium text-muted-foreground">
              {item.title}
            </div>
            {item.icon ? (
              <div className="text-muted-foreground">{item.icon}</div>
            ) : null}
          </div>
          <div
            className={cn(
              compact
                ? "mt-2 text-xl font-semibold tabular-nums leading-none text-foreground"
                : "mt-3 text-2xl font-semibold tabular-nums leading-none text-foreground",
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
        </motion.div>
      ))}
    </motion.section>
  );
}

export type { StatCardItem, StatCardTone };
