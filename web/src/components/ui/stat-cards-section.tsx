import type { ReactNode } from "react";
import { motion } from "framer-motion";
import { Link, useInRouterContext } from "react-router-dom";
import { cn } from "@/lib/utils";

const MotionLink = motion.create(Link);

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
  /** Optional destination URL; turns the card into an interactive link with hover lift. */
  href?: string;
  /** Optional click handler; turns the card into an interactive button with hover lift. */
  onClick?: () => void;
};

type StatCardsSectionProps = {
  items: StatCardItem[];
  className?: string;
  cardClassName?: string;
  compact?: boolean;
  /** Container element; use "div" when already wrapped inside a named <section>. */
  as?: "section" | "div";
};

function isInternalHref(href: string): boolean {
  return href.startsWith("/") && !href.startsWith("//");
}

function toneTextClass(tone: StatCardTone | undefined): string {
  switch (tone) {
    case "success":
      return "text-[hsl(var(--success-text))]";
    case "warning":
      return "text-[hsl(var(--warning-text))]";
    case "destructive":
      return "text-[hsl(var(--destructive-text))]";
    case "info":
      return "text-[hsl(var(--info-text))]";
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
  as = "section",
}: StatCardsSectionProps) {
  const inRouter = useInRouterContext();
  const Container = as === "div" ? motion.div : motion.section;

  return (
    <Container
      initial="initial"
      animate="animate"
      variants={staggerContainer}
      className={cn(
        "grid gap-2 sm:grid-cols-2 xl:grid-cols-4",
        compact && items.length > 1 ? "grid-cols-2" : "grid-cols-1",
        items.length >= 5 && "2xl:grid-cols-5",
        className
      )}
    >
      {items.map((item) => {
        const href = item.href?.trim();
        const isLink = Boolean(href);
        const isButton = !isLink && Boolean(item.onClick);
        const isInteractive = isLink || isButton;

        const sharedMotionProps = {
          variants: staggerItem,
          ...(isInteractive ? { whileHover: { y: -4 } } : {}),
          "data-tone": item.tone ?? "info",
          className: cn(
            "rounded-lg border border-border bg-card shadow-sm",
            compact ? "p-3" : "p-4",
            isInteractive &&
              "cursor-pointer transition-[border-color,box-shadow] duration-200 ease-out hover:border-primary/30 hover:shadow-[var(--shadow-panel-hover)] focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35",
            isLink && "block",
            isButton && "w-full text-left",
            cardClassName
          ),
        };

        const cardBody = (
          <>
            <span className="flex items-center justify-between">
              <span className="text-mini font-medium text-muted-foreground">
                {item.title}
              </span>
              {item.icon ? (
                <span className="text-muted-foreground">{item.icon}</span>
              ) : null}
            </span>
            <span
              className={cn(
                compact
                  ? "mt-2 block text-xl font-semibold tabular-nums leading-none text-foreground"
                  : "mt-3 block text-2xl font-semibold tabular-nums leading-none text-foreground",
                item.valueClassName
              )}
            >
              {item.value}
              {item.unit ? (
                <span className="ml-1 text-sm font-medium text-muted-foreground">
                  {item.unit}
                </span>
              ) : null}
            </span>
            {item.description ? (
              <span className={cn("mt-2 block text-xs font-medium", toneTextClass(item.tone))}>
                {item.description}
              </span>
            ) : null}
          </>
        );

        const cardKey = item.id ?? item.title;

        if (isLink && href) {
          if (inRouter && isInternalHref(href)) {
            return (
              <MotionLink key={cardKey} to={href} onClick={item.onClick} {...sharedMotionProps}>
                {cardBody}
              </MotionLink>
            );
          }
          return (
            <motion.a key={cardKey} href={href} onClick={item.onClick} {...sharedMotionProps}>
              {cardBody}
            </motion.a>
          );
        }

        if (isButton) {
          return (
            <motion.button key={cardKey} type="button" onClick={item.onClick} {...sharedMotionProps}>
              {cardBody}
            </motion.button>
          );
        }

        return (
          <motion.div key={cardKey} {...sharedMotionProps}>
            {cardBody}
          </motion.div>
        );
      })}
    </Container>
  );
}

export type { StatCardItem, StatCardTone };
