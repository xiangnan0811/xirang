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
}

export function LogsFullscreenDialog({
  open,
  onOpenChange,
  filteredLogs,
}: LogsFullscreenDialogProps) {
  const { t } = useTranslation();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        size="lg"
        className="flex max-h-[90vh] flex-col md:max-w-[calc(100vw-64px)]"
      >
        <DialogHeader>
          <DialogTitle>
            {t("logs.fullscreenTitle", { count: filteredLogs.length })}
          </DialogTitle>
          <DialogCloseButton />
        </DialogHeader>
        <div className="flex-1 overflow-y-auto px-6 pb-6">
          <div
            className="terminal-surface thin-scrollbar h-[calc(90vh-140px)] overflow-auto rounded-xl p-3 font-mono text-[12px] md:text-[13px]"
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
                  description={t("logs.emptyDesc")}
                />
              </div>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
