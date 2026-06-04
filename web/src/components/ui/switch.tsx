import * as React from "react";
import * as SwitchPrimitive from "@radix-ui/react-switch";
import { motion, useReducedMotion } from "framer-motion";
import { cn } from "@/lib/utils";

type SwitchProps = React.ComponentPropsWithoutRef<typeof SwitchPrimitive.Root> & {
  /** 当提供时，自动生成 htmlFor/id 关联，并为 Switch 添加 aria-label */
  label?: string;
};

const Switch = React.forwardRef<
  React.ComponentRef<typeof SwitchPrimitive.Root>,
  SwitchProps
>(({ className, label, id, ...props }, ref) => {
  const reduced = useReducedMotion();
  const autoId = React.useId();
  const resolvedId = id ?? autoId;

  const switchElement = (
    <SwitchPrimitive.Root
      ref={ref}
      id={resolvedId}
      className={cn(
        "group peer inline-flex h-[22px] w-[38px] shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent p-[2px]",
        "bg-muted data-[state=checked]:bg-primary",
        "focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      aria-label={label ?? props["aria-label"]}
      {...props}
    >
      <SwitchPrimitive.Thumb asChild>
        <motion.span
          layout={!reduced}
          transition={reduced ? { duration: 0 } : { type: "spring", stiffness: 500, damping: 30 }}
          className="pointer-events-none block size-[18px] rounded-full bg-background shadow-sm ring-0 data-[state=checked]:translate-x-[16px]"
        />
      </SwitchPrimitive.Thumb>
    </SwitchPrimitive.Root>
  );

  if (label) {
    return (
      <label htmlFor={resolvedId} className="inline-flex items-center gap-2 cursor-pointer">
        {switchElement}
        <span className="text-sm select-none">{label}</span>
      </label>
    );
  }

  return switchElement;
});
Switch.displayName = SwitchPrimitive.Root.displayName;

export { Switch };
