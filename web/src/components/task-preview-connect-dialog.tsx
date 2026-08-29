import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { FolderSearch, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { backupAssetsRecoveryPointHref } from "@/features/backup-assets/backup-assets-route-state";
import { apiClient } from "@/lib/api/client";
import type { CatalogStatus, TaskRecord } from "@/types/domain";

const POLL_INTERVAL_MS = 2_000;
const MAX_POLL_ATTEMPTS = 60;
const POLL_DEADLINE_MS = 120_000;

type PreviewPhase =
  | "idle"
  | "connecting"
  | "indexing"
  | "ready"
  | "blocked"
  | "failed"
  | "timed_out_background";

type SafeNotice =
  | "connectBlocked"
  | "missingPoint"
  | "catalogBlocked"
  | "catalogFailed"
  | "catalogPartial"
  | "catalogUnavailable"
  | "requestFailed";

export interface TaskPreviewConnectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: TaskRecord | null;
  token: string | null;
}

type TaskPreviewConnectDialogContentProps = Omit<TaskPreviewConnectDialogProps, "task"> & {
  task: TaskRecord;
};

function waitForNextPoll(signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, POLL_INTERVAL_MS);
    const onAbort = () => {
      window.clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function awaitAbortable<T>(operation: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = (callback: () => void) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", onAbort);
      callback();
    };
    const onAbort = () => finish(() => reject(new DOMException("Aborted", "AbortError")));
    if (signal.aborted) {
      onAbort();
      return;
    }
    signal.addEventListener("abort", onAbort, { once: true });
    operation.then(
      (value) => finish(() => resolve(value)),
      (error: unknown) => finish(() => reject(error)),
    );
  });
}

function catalogTerminalNotice(status: CatalogStatus): SafeNotice | null {
  if (status.generation?.state === "failed" || status.coverage.status === "failed") {
    return "catalogFailed";
  }
  if (status.generation?.state === "partial" || status.coverage.status === "partial") {
    return "catalogPartial";
  }
  if (status.coverage.status === "unavailable") {
    return "catalogUnavailable";
  }
  if (status.generation?.state === "complete" && status.coverage.status === "complete" &&
    (!status.contentAvailability.available || !status.permissions.list)) {
    return "catalogBlocked";
  }
  return null;
}

function isCatalogReady(status: CatalogStatus): boolean {
  return status.generation?.state === "complete" &&
    status.coverage.status === "complete" &&
    status.contentAvailability.available &&
    status.permissions.list;
}

export function TaskPreviewConnectDialog({
  open,
  onOpenChange,
  task,
  token,
}: TaskPreviewConnectDialogProps) {
  if (!task) {
    return null;
  }
  return (
    <TaskPreviewConnectDialogContent
      key={`${open}:${task.id}:${token ?? ""}`}
      open={open}
      onOpenChange={onOpenChange}
      task={task}
      token={token}
    />
  );
}

function TaskPreviewConnectDialogContent({
  open,
  onOpenChange,
  task,
  token,
}: TaskPreviewConnectDialogContentProps) {
  const { t } = useTranslation();
  const [phase, setPhase] = useState<PreviewPhase>("idle");
  const [notice, setNotice] = useState<SafeNotice | null>(null);
  const [pointId, setPointId] = useState<string | null>(null);
  const controllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    return () => {
      controllerRef.current?.abort();
      controllerRef.current = null;
    };
  }, []);

  const busy = phase === "connecting" || phase === "indexing";

  const connect = async () => {
    if (!token || busy) {
      return;
    }
    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    setPhase("connecting");
    setNotice(null);
    setPointId(null);
    let pollDeadline: number | null = null;
    let pollDeadlineExpired = false;
    try {
      const result = await apiClient.connectBackupRepository(
        token,
        { taskId: task.id },
        controller.signal,
      );
      if (controller.signal.aborted) return;
      if (result.status !== "available") {
        setNotice("connectBlocked");
        setPhase("blocked");
        return;
      }
      const mutablePoint = result.value.mutablePoint;
      if (!mutablePoint) {
        setNotice("missingPoint");
        setPhase("failed");
        return;
      }

      setPointId(mutablePoint.id);
      setPhase("indexing");
      pollDeadline = window.setTimeout(() => {
        pollDeadlineExpired = true;
        controller.abort();
      }, POLL_DEADLINE_MS);
      for (let attempt = 0; attempt < MAX_POLL_ATTEMPTS; attempt += 1) {
        const catalog = await awaitAbortable(
          apiClient.getRecoveryPointCatalogStatus(token, mutablePoint.id, controller.signal),
          controller.signal,
        );
        if (controller.signal.aborted) return;
        if (catalog.status !== "available") {
          setNotice("catalogBlocked");
          setPhase("blocked");
          return;
        }
        if (isCatalogReady(catalog.value)) {
          setPhase("ready");
          return;
        }
        const terminalNotice = catalogTerminalNotice(catalog.value);
        if (terminalNotice !== null) {
          setNotice(terminalNotice);
          setPhase(terminalNotice === "catalogBlocked" ? "blocked" : "failed");
          return;
        }
        if (attempt + 1 < MAX_POLL_ATTEMPTS) {
          await waitForNextPoll(controller.signal);
        }
      }
      if (!controller.signal.aborted) {
        setPhase("timed_out_background");
      }
    } catch {
      if (pollDeadlineExpired) {
        setPhase("timed_out_background");
        return;
      }
      if (controller.signal.aborted) return;
      setNotice("requestFailed");
      setPhase("failed");
    } finally {
      if (pollDeadline !== null) {
        window.clearTimeout(pollDeadline);
      }
    }
  };

  const statusText = phase === "idle"
    ? t("taskPreviewConnect.status.idle")
    : phase === "connecting"
      ? t("taskPreviewConnect.status.connecting")
      : phase === "indexing"
        ? t("taskPreviewConnect.status.indexing")
        : phase === "ready"
          ? t("taskPreviewConnect.status.ready")
          : phase === "timed_out_background"
            ? t("taskPreviewConnect.status.timedOut")
            : notice ? t(`taskPreviewConnect.notice.${notice}`) : t("taskPreviewConnect.notice.requestFailed");

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <FolderSearch className="size-5 text-primary" aria-hidden />
            {t("taskPreviewConnect.title", { name: task.name || task.policyName })}
          </DialogTitle>
          <DialogDescription>{t("taskPreviewConnect.description")}</DialogDescription>
          <DialogCloseButton aria-label={t("common.close")} />
        </DialogHeader>

        <DialogBody className="space-y-4">
          <InlineAlert tone="info">
            <p>{t("taskPreviewConnect.safetyProbe")}</p>
            <p className="mt-1">{t("taskPreviewConnect.safetyMutation")}</p>
          </InlineAlert>

          <div className="rounded-lg border border-border/70 bg-muted/30 p-3" role="status" aria-live="polite" aria-atomic="true">
            <div className="flex items-center gap-2">
              {busy ? <Loader2 className="size-4 animate-spin text-primary" aria-hidden /> : null}
              <p className="font-medium">{statusText}</p>
            </div>
          </div>
        </DialogBody>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.close")}
          </Button>
          {phase === "ready" && pointId ? (
            <Button asChild>
              <a href={backupAssetsRecoveryPointHref(task.id, pointId)}>
                {t("taskPreviewConnect.openPreview")}
              </a>
            </Button>
          ) : (
            <Button type="button" onClick={() => void connect()} disabled={busy || !token}>
              {busy ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <FolderSearch className="mr-1.5 size-4" aria-hidden />}
              {busy ? t("taskPreviewConnect.working") : t("taskPreviewConnect.confirm")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
