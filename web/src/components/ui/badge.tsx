import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1.5 rounded-full px-2.5 py-[3px] text-[10.5px] font-medium",
  {
    variants: {
      tone: {
        success: "bg-[hsl(var(--success)/0.16)] text-[hsl(var(--success))]",
        warning: "bg-[hsl(var(--warning)/0.22)] text-[hsl(38_50%_28%)] dark:text-[hsl(var(--warning))]",
        destructive: "bg-[hsl(var(--destructive)/0.18)] text-[hsl(var(--destructive))]",
        info: "bg-[hsl(var(--info)/0.18)] text-[hsl(var(--info))]",
        neutral: "bg-muted text-muted-foreground",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {
  dot?: boolean;
}

function Badge({ className, tone, dot = true, children, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ tone }), className)} {...props}>
      {dot ? (
        <span
          className={cn(
            "size-[5px] rounded-full",
            tone === "success" && "bg-[hsl(var(--success))]",
            tone === "warning" && "bg-[hsl(var(--warning))]",
            tone === "destructive" && "bg-[hsl(var(--destructive))]",
            tone === "info" && "bg-[hsl(var(--info))]",
            (!tone || tone === "neutral") && "bg-muted-foreground",
          )}
          aria-hidden
        />
      ) : null}
      {children}
    </span>
  );
}

export { Badge, badgeVariants };
