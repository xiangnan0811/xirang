import * as React from "react";
import type { VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";
import { badgeVariants } from "@/components/ui/badge.variants";

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

export { Badge };
