import * as React from "react";
import { cn } from "@/lib/utils";

export type MonoChipProps = React.HTMLAttributes<HTMLSpanElement>;

export function MonoChip({ className, ...props }: MonoChipProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-sm bg-secondary px-2 py-[2px] font-mono text-micro font-medium tracking-[0.02em] text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}
