import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold transition-colors",
  {
    variants: {
      variant: {
        default: "border-primary/25 bg-primary/15 text-primary",
        secondary: "border-border/80 bg-secondary/65 text-secondary-foreground",
        success: "border-emerald-500/35 bg-emerald-500/15 text-emerald-600 dark:text-emerald-300",
        warning: "border-amber-500/35 bg-amber-500/15 text-amber-600 dark:text-amber-300",
        danger: "border-red-500/35 bg-red-500/15 text-red-600 dark:text-red-300",
        outline: "border-border/80 bg-background/60 text-foreground"
      }
    },
    defaultVariants: {
      variant: "default"
    }
  }
);

export interface BadgeProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return <div className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { Badge, badgeVariants };
