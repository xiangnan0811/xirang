import { useTranslation } from "react-i18next";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import type { LogEvent } from "@/types/domain";
import { LogEntry } from "./logs-page.log-entry";

interface LogsFullscreenDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  filteredLogs: LogEvent[];
  /** run = exact-run manual snapshot; empty copy must not mention live streams or selectors. */
  mode?: "task" | "run";
  /** Optional scope identity (e.g. "Task #T / Run #R") prefixed to the dialog title. */
  scopeLabel?: string;
  /** Optional close focus handler; omitted keeps Radix's default focus restoration. */
  onCloseAutoFocus?: (event: Event) => void;
}

export function LogsFullscreenDialog({
  open,
  onOpenChange,
  filteredLogs,
  mode = "task",
  scopeLabel,
  onCloseAutoFocus,
}: LogsFullscreenDialogProps) {
  const { t } = useTranslation();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        size="lg"
        className="flex max-h-[90vh] flex-col md:max-w-[calc(100vw-64px)]"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <DialogHeader>
          <DialogTitle className="min-w-0 pr-8 break-words [overflow-wrap:anywhere]">
            {scopeLabel ? (
              <>
                <span>{scopeLabel}</span>
                <span aria-hidden="true"> · </span>
              </>
            ) : null}
            {t("logs.fullscreenTitle", { count: filteredLogs.length })}
          </DialogTitle>
          <DialogCloseButton />
        </DialogHeader>
        <div className="flex-1 overflow-y-auto px-6 pb-6">
          <div
            className="terminal-surface thin-scrollbar h-[calc(90vh-140px)] overflow-auto rounded-xl p-3 font-mono text-[12px] md:text-nav"
            role="log"
            aria-live="polite"
            aria-relevant="additions text"
            aria-label={t("logs.fullscreenTerminalAriaLabel", {
              count: filteredLogs.length,
            })}
          >
            {filteredLogs.length > 0 ? (
              <div className="px-1">
                {filteredLogs.map((log) => (
                  <LogEntry
                    key={log.logId ? `fs-${log.logId}` : `fs-${log.id}`}
                    log={log}
                    hoverClass="hover:bg-white/5"
                  />
                ))}
              </div>
            ) : (
              <div className="px-2 py-10">
                <EmptyState
                  className="terminal-empty"
                  title={t("logs.emptyTitle")}
                  description={t(mode === "run" ? "logs.runEmptyDesc" : "logs.emptyDesc")}
                />
              </div>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
