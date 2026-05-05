import * as React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

export type SelectProps = React.SelectHTMLAttributes<HTMLSelectElement> & {
  /** Layout classes for the outer wrapper (width, grid spans, etc.). */
  containerClassName?: string;
};

const Select = React.forwardRef<HTMLSelectElement, SelectProps>(
  ({ className, containerClassName, children, ...props }, ref) => (
    <div className={cn("relative", containerClassName)}>
      <select
        ref={ref}
        className={cn(
          "h-9 w-full appearance-none rounded-md border border-input bg-card pl-3 pr-9 text-sm text-foreground",
          "focus-visible:outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/35",
          "aria-[invalid=true]:border-destructive aria-[invalid=true]:ring-destructive/30",
          "disabled:cursor-not-allowed disabled:opacity-50",
          "transition-[border-color,box-shadow] duration-150 ease-out",
          className
        )}
        {...props}
      >
        {children}
      </select>
      <ChevronDown
        aria-hidden="true"
        className="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
      />
    </div>
  )
);

Select.displayName = "Select";

export { Select };
