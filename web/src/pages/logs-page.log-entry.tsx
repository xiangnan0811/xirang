import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import type { LogEvent } from "@/types/domain";
import { formatLogTime, getLevelClass, highlightErrorCode } from "./logs-page.utils";

interface LogEntryProps {
  log: LogEvent;
  hoverClass?: string;
}

export function LogEntry({
  log,
  hoverClass = "hover:bg-white/10",
}: LogEntryProps) {
  const { t } = useTranslation();

  return (
    <div
      className={cn(
        "terminal-group-row mb-0.5 flex flex-col gap-1.5 border-b py-1.5 transition-colors md:flex-row md:items-start",
        hoverClass,
      )}
    >
      <div className="flex shrink-0 items-center gap-3 md:w-[260px]">
        <span className="terminal-time text-[11px] opacity-60 md:text-[12px]">
          {formatLogTime(log.timestamp)}
        </span>
        <span
          className={cn(
            "w-12 text-[11px] font-medium md:text-[12px]",
            getLevelClass(log.level),
          )}
        >
          {log.level.toUpperCase()}
        </span>
      </div>
      <div className="flex-1 break-all leading-relaxed">
        <span className="terminal-node-chip mr-2 inline-flex items-center rounded px-1.5 py-0.5 text-[10px] md:text-[11px]">
          {log.nodeName ?? t("logs.system")}
          {log.taskId ? (
            <span className="ml-1 opacity-70">
              | #{log.taskId}
            </span>
          ) : null}
        </span>
        <span>{highlightErrorCode(log.message)}</span>
      </div>
    </div>
  );
}
