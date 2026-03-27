import type { LucideIcon } from "lucide-react";
import { Inbox } from "lucide-react";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  icon?: LucideIcon;
  title: string;
  description?: string;
  action?: React.ReactNode;
  className?: string;
}

function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "glass-panel px-6 py-16 text-center transition-[transform,opacity] duration-300",
        className
      )}
    >
      <div className="mx-auto mb-6 flex size-20 animate-float items-center justify-center rounded-3xl border border-primary/20 bg-gradient-to-br from-primary/10 to-primary/5 text-primary shadow-sm relative before:absolute before:-inset-4 before:-z-10 before:rounded-full before:bg-primary/5 before:blur-xl">
        <Icon className="size-8 opacity-80" />
      </div>
      <h3 className="text-lg font-semibold tracking-wide text-foreground">{title}</h3>
      {description ? (
        <p className="mx-auto mt-2.5 max-w-sm text-sm leading-relaxed text-muted-foreground/90">
          {description}
        </p>
      ) : null}
      {action ? <div className="mt-6 flex justify-center animate-in fade-in slide-in-from-bottom-2 duration-500 delay-150">{action}</div> : null}
    </div>
  );
}

export { EmptyState };
export type { EmptyStateProps };
