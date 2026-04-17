import * as React from "react";
import { cn } from "@/lib/utils";

export function Skeleton({ className, style, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      aria-hidden
      className={cn(
        "rounded-md bg-[linear-gradient(90deg,hsl(var(--muted))_0%,hsl(var(--secondary))_50%,hsl(var(--muted))_100%)] bg-[length:200%_100%] motion-reduce:animate-none",
        className,
      )}
      style={{
        animationName: "shimmer",
        animationDuration: "1.5s",
        animationTimingFunction: "ease-in-out",
        animationIterationCount: "infinite",
        ...style,
      }}
      {...props}
    />
  );
}
