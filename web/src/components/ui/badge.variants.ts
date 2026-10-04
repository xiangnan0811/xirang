import { cva } from "class-variance-authority";

export const badgeVariants = cva(
  "inline-flex items-center gap-1.5 rounded-full px-2.5 py-[3px] text-[10.5px] font-medium",
  {
    variants: {
      tone: {
        success:
          "bg-[hsl(var(--success)/0.14)] text-[hsl(var(--success-text))] dark:bg-[hsl(var(--success)/0.20)]",
        warning:
          "bg-[hsl(var(--warning)/0.18)] text-[hsl(var(--warning-text))] dark:bg-[hsl(var(--warning)/0.25)]",
        destructive:
          "bg-[hsl(var(--destructive)/0.14)] text-[hsl(var(--destructive-text))] dark:bg-[hsl(var(--destructive)/0.20)]",
        info:
          "bg-[hsl(var(--info)/0.14)] text-[hsl(var(--info-text))] dark:bg-[hsl(var(--info)/0.20)]",
        neutral: "bg-muted text-foreground/85",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);
