import { cn } from "@/lib/utils";
import type { NodeStatus } from "@/types/domain";

type StatusPulseProps = {
  tone: NodeStatus;
  className?: string;
};

export function StatusPulse({ tone, className }: StatusPulseProps) {
  return (
    <span
      className={cn(
        "inline-flex size-2 rounded-full",
        tone === "online" ? "pulse-online" : tone === "warning" ? "pulse-warning" : "pulse-offline",
        className
      )}
    />
  );
}
