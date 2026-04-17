import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center whitespace-nowrap text-sm font-medium transition-[background,color,transform,box-shadow] duration-150 ease-out focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35 disabled:pointer-events-none disabled:opacity-50 active:scale-[0.95] active:duration-100 gap-2",
  {
    variants: {
      variant: {
        default:      "bg-primary text-primary-foreground shadow-sm hover:bg-primary/90",
        secondary:    "border border-border bg-card text-foreground hover:bg-accent",
        ghost:        "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
        outline:      "border border-border bg-transparent text-foreground hover:bg-accent",
        destructive:  "bg-destructive text-destructive-foreground shadow-sm hover:bg-destructive/90",
        link:         "text-foreground underline-offset-4 hover:underline",
      },
      size: {
        default: "h-9 px-4 py-2",
        sm:      "h-8 px-3 text-xs",
        lg:      "h-11 px-6",
        icon:    "h-9 w-9",
      },
      shape: {
        rect: "rounded-md",
        pill: "rounded-full font-semibold",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
      shape: "rect",
    },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
  loading?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, shape, asChild = false, loading, disabled, children, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, shape, className }))}
        ref={ref}
        disabled={disabled || loading}
        {...props}
      >
        {loading ? <Loader2 className="size-4 animate-spin" aria-hidden /> : null}
        {children}
      </Comp>
    );
  },
);
Button.displayName = "Button";

export { Button, buttonVariants };
