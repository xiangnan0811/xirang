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
 *
 * Indicators are noninteractive. Below `sm` they stay compact (`size-6`) so
 * seven-step progress fits 320px/375px dialogs without clipping; `sm+`
 * restores the larger indicator. Labels remain in the DOM (`max-sm:sr-only`)
 * and the active step keeps `aria-current="step"`.
 */
export function Stepper({ steps, current, className, ...props }: StepperProps) {
  return (
    <div
      role="navigation"
      aria-label={props["aria-label"] ?? "Steps"}
      className={cn("flex w-full min-w-0 items-center sm:gap-1.5", className)}
      {...props}
    >
      {steps.map((label, i) => {
        const done = i < current;
        const active = i === current;
        const last = i === steps.length - 1;
        return (
          <div key={i} className="flex min-w-0 flex-1 items-center sm:gap-2">
            <div
              aria-current={active ? "step" : undefined}
              aria-label={label || undefined}
              className={cn(
                "flex size-6 shrink-0 items-center justify-center rounded-lg text-[10px] font-semibold sm:size-11 sm:text-xs",
                done || active
                  ? "bg-primary text-primary-foreground"
                  : "bg-muted text-muted-foreground"
              )}
            >
              {done ? <Check className="size-3 sm:size-3.5" aria-hidden="true" /> : i + 1}
            </div>
            {label ? (
              <span
                className={cn(
                  "truncate text-xs max-sm:sr-only sm:inline",
                  active || done ? "text-foreground" : "text-muted-foreground"
                )}
              >
                {label}
              </span>
            ) : null}
            {last ? null : (
              <div
                aria-hidden="true"
                className={cn(
                  "h-px min-w-1 flex-1 sm:min-w-2",
                  i < current ? "bg-primary" : "bg-border"
                )}
              />
            )}
          </div>
        );
      })}
    </div>
  );
}
