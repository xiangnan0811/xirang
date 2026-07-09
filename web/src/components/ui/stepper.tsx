import * as React from "react";
import { Check } from "lucide-react";
import { cn } from "@/lib/utils";

export interface StepperProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Step labels. An empty string hides that step's label (number-only). */
  steps: string[];
  /** Zero-based index of the active step. */
  current: number;
}

/**
 * Shared horizontal step indicator used by wizard dialogs. Completed and active
 * steps use the brand accent; upcoming steps are muted. A check replaces the
 * number on completed steps. Connector lines fill as steps are completed.
 */
export function Stepper({ steps, current, className, ...props }: StepperProps) {
  return (
    <div
      role="navigation"
      aria-label={props["aria-label"] ?? "Steps"}
      className={cn("flex items-center gap-1.5", className)}
      {...props}
    >
      {steps.map((label, i) => {
        const done = i < current;
        const active = i === current;
        return (
          <div key={i} className="flex flex-1 items-center gap-2">
            <div
              aria-current={active ? "step" : undefined}
              className={cn(
                "flex size-6 shrink-0 items-center justify-center rounded-full text-micro font-semibold",
                done || active
                  ? "bg-primary text-primary-foreground"
                  : "bg-muted text-muted-foreground"
              )}
            >
              {done ? <Check className="size-3.5" aria-hidden="true" /> : i + 1}
            </div>
            {label ? (
              <span
                className={cn(
                  "truncate text-xs hidden sm:inline",
                  active || done ? "text-foreground" : "text-muted-foreground"
                )}
              >
                {label}
              </span>
            ) : null}
            {i < steps.length - 1 ? (
              <div
                className={cn("h-px flex-1", i < current ? "bg-primary" : "bg-border")}
              />
            ) : null}
          </div>
        );
      })}
    </div>
  );
}
