import * as React from "react";
import { cn } from "@/lib/utils";

export interface StepperProps extends React.HTMLAttributes<HTMLDivElement> {
  steps: string[];
  current: number;
}

export function Stepper({ steps, current, className, ...props }: StepperProps) {
  return (
    <div className={cn("flex items-center gap-1.5", className)} {...props}>
      {steps.map((label, i) => (
        <div key={i} className="flex flex-1 items-center gap-2">
          <div
            className={cn(
              "flex size-5 shrink-0 items-center justify-center rounded-full text-micro font-semibold",
              i < current && "bg-primary text-primary-foreground",
              i === current && "bg-[hsl(var(--accent-brand))] text-[hsl(var(--background))]",
              i > current && "bg-muted text-muted-foreground",
            )}
          >
            {i < current ? "✓" : i + 1}
          </div>
          <span className="text-xs text-muted-foreground">{label}</span>
          {i < steps.length - 1 ? <div className="h-px flex-1 bg-border" /> : null}
        </div>
      ))}
    </div>
  );
}
