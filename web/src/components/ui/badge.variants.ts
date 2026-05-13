import { cva } from "class-variance-authority";

export const badgeVariants = cva(
  "inline-flex items-center gap-1.5 rounded-full px-2.5 py-[3px] text-[10.5px] font-medium",
  {
    variants: {
      tone: {
        success: "bg-[hsl(var(--success)/0.16)] text-[hsl(var(--success))]",
        warning: "bg-[hsl(var(--warning)/0.22)] text-warning-foreground dark:text-warning",
        destructive: "bg-[hsl(var(--destructive)/0.18)] text-[hsl(var(--destructive))]",
        info: "bg-[hsl(var(--info)/0.18)] text-[hsl(var(--info))]",
        neutral: "bg-muted text-muted-foreground",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);
