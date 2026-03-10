import * as React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

export type AppSelectProps = React.SelectHTMLAttributes<HTMLSelectElement> & {
  /** Layout classes for the outer wrapper (width, grid spans, etc.). */
  containerClassName?: string;
};

const AppSelect = React.forwardRef<HTMLSelectElement, AppSelectProps>(
  ({ className, containerClassName, children, ...props }, ref) => (
    <div className={cn("relative", containerClassName)}>
      <select ref={ref} className={cn("app-select w-full", className)} {...props}>
        {children}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
    </div>
  )
);

AppSelect.displayName = "AppSelect";

export { AppSelect };
