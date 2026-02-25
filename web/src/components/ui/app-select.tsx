import * as React from "react";
import { cn } from "@/lib/utils";

export type AppSelectProps = React.SelectHTMLAttributes<HTMLSelectElement>;

const AppSelect = React.forwardRef<HTMLSelectElement, AppSelectProps>(
  ({ className, children, ...props }, ref) => (
    <select ref={ref} className={cn("app-select", className)} {...props}>
      {children}
    </select>
  )
);

AppSelect.displayName = "AppSelect";

export { AppSelect };
