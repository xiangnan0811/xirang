import { ReactNode } from "react";
import { AlertTriangle, Info, CheckCircle2 } from "lucide-react";
import { cn } from "@/lib/utils";

export type InlineAlertTone = "info" | "warning" | "success" | "critical";

interface InlineAlertProps {
  tone?: InlineAlertTone;
  title?: string;
  icon?: ReactNode;
  children: ReactNode;
  className?: string;
}

const toneMap = {
  info: { bg: "bg-info/10", text: "text-info", line: "bg-info", defaultIcon: Info },
  warning: { bg: "bg-warning/10", text: "text-warning", line: "bg-warning", defaultIcon: AlertTriangle },
  success: { bg: "bg-success/10", text: "text-success", line: "bg-success", defaultIcon: CheckCircle2 },
  critical: { bg: "bg-destructive/10", text: "text-destructive", line: "bg-destructive", defaultIcon: AlertTriangle },
};

export function InlineAlert({
  tone = "info",
  title,
  icon,
  children,
  className,
}: InlineAlertProps) {
  const s = toneMap[tone];
  const Icon = s.defaultIcon;

  return (
    <div className={cn("glass-panel overflow-hidden relative group p-3 transition-colors", className)}>
      <div className={cn("absolute top-0 left-0 w-1 h-full opacity-60 group-hover:opacity-100 transition-opacity", s.line)} />
      <div className="flex items-start gap-3 pl-2">
        <div className={cn("flex items-center justify-center rounded-lg p-2 shrink-0", s.bg, s.text)}>
          {icon || <Icon className="size-4" />}
        </div>
        <div className="flex flex-col gap-0.5 min-w-0 text-sm py-0.5">
          {title && <span className={cn("font-medium", s.text)}>{title}</span>}
          <div className={cn("text-muted-foreground leading-relaxed break-words", !title && s.text)}>{children}</div>
        </div>
      </div>
    </div>
  );
}
