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
import { waitForRecoveryPointCatalogReady } from "@/lib/recovery-point-catalog-readiness";
import { apiClient } from "@/lib/api/client";
import type { TaskRecord } from "@/types/domain";

export interface TaskPreviewConnectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: TaskRecord | null;
  token: string | null;
}

type TaskPreviewConnectDialogContentProps = Omit<TaskPreviewConnectDialogProps, "task"> & {
  task: TaskRecord;
};

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
      const readiness = await waitForRecoveryPointCatalogReady({
        token,
        recoveryPointId: mutablePoint.id,
        signal: controller.signal,
      });
      if (controller.signal.aborted || readiness.status === "aborted") return;
      if (readiness.status === "ready") {
        setPhase("ready");
        return;
      }
      if (readiness.status === "blocked") {
        setNotice("catalogBlocked");
        setPhase("blocked");
        return;
      }
      if (readiness.status === "terminal") {
        setNotice(readiness.notice);
        setPhase(readiness.notice === "catalogBlocked" ? "blocked" : "failed");
        return;
      }
      setPhase("timed_out_background");
    } catch {
      if (controller.signal.aborted) return;
      setNotice("requestFailed");
      setPhase("failed");
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
