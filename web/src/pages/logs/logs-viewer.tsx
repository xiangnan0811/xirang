import { useRef } from "react";
import { useTranslation } from "react-i18next";
import type { LogEvent } from "@/types/domain";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { cn } from "@/lib/utils";
import { LogEntry } from "../logs-page.log-entry";

export interface LogsViewerProps {
  filteredLogs: LogEvent[];
  historyLoading: boolean;
  onReset: () => void;
}

export function LogsViewer({
  filteredLogs,
  historyLoading,
  onReset,
}: LogsViewerProps) {
  const { t } = useTranslation();
  const terminalRef = useRef<HTMLDivElement | null>(null);

  if (historyLoading && filteredLogs.length === 0) {
    return (
      <LoadingState
        title={t("logs.loadingTitle")}
        description={t("logs.loadingDesc")}
        rows={4}
      />
    );
  }

  return (
    <div
      ref={terminalRef}
      className={cn(
        "terminal-surface thin-scrollbar overflow-auto rounded-xl p-3 font-mono text-[12px] md:text-[13px]",
        "h-[62vh]",
      )}
      role="log"
      aria-live="polite"
      aria-relevant="additions text"
      aria-label={t("logs.terminalAriaLabel", {
        count: filteredLogs.length,
      })}
    >
      {filteredLogs.length > 0 ? (
        <div className="px-1">
          {filteredLogs.map((log) => (
            <LogEntry
              key={log.logId ? `line-${log.logId}` : log.id}
              log={log}
              hoverClass="hover:bg-white/10"
            />
          ))}
        </div>
      ) : (
        <div className="px-2 py-10">
          <EmptyState
            className="terminal-empty"
            title={t("logs.emptyTitle")}
            description={t("logs.emptyDesc")}
            action={
              <Button size="sm" variant="outline" onClick={onReset}>
                {t("logs.resetFilter")}
              </Button>
            }
          />
        </div>
      )}
    </div>
  );
}
