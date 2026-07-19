import { type FormEvent, useEffect, useRef, useState } from "react";
import { ArrowLeft, Download, File, Folder, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { LoadingState } from "@/components/ui/loading-state";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { formatBytes, getErrorMessage } from "@/lib/utils";
import { formatTime } from "@/lib/api/core";
import type { ResticSnapshot, ResticEntry } from "@/lib/api/snapshots-api";
import { toast } from "sonner";
import { BackupAssetsTaskContextLink } from "@/features/backup-assets/backup-assets-task-context-link";

const SNAPSHOT_RESTORE_GRANT_MAX_REASON_LENGTH = 240;
const SNAPSHOT_RESTORE_GRANT_TTL_SECONDS = 600;

interface SnapshotBrowserProps {
  taskId: number;
  token: string;
  /** 初始要浏览的快照 ID，用于从搜索结果跳转 */
  initialSnapshotId?: string;
  /** 初始文件路径，配合 initialSnapshotId 使用 */
  initialPath?: string;
}

export function SnapshotBrowser({ taskId, token, initialSnapshotId, initialPath }: SnapshotBrowserProps) {
  const { t } = useTranslation();
  const [snapshots, setSnapshots] = useState<ResticSnapshot[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedSnapshot, setSelectedSnapshot] = useState<ResticSnapshot | null>(null);
  const [files, setFiles] = useState<ResticEntry[]>([]);
  const [filesLoading, setFilesLoading] = useState(false);
  const [currentPath, setCurrentPath] = useState("/");
  const [selectedPaths, setSelectedPaths] = useState<Set<string>>(new Set());
  const [restoring, setRestoring] = useState(false);
  const [restoreTarget, setRestoreTarget] = useState("/tmp/xirang-restore");
  const [grantDialogOpen, setGrantDialogOpen] = useState(false);
  const [grantReason, setGrantReason] = useState("");
  const [grantError, setGrantError] = useState<string | null>(null);
  const autoNavigated = useRef(false);
  const { ensureStepUpProof } = useAuth();

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    apiClient
      .listSnapshots(token, taskId)
      .then((data) => {
        if (!controller.signal.aborted) {
          setSnapshots(data);
          // 自动导航到初始快照（仅首次）
          if (initialSnapshotId && !autoNavigated.current) {
            autoNavigated.current = true;
            const target = data.find((s) => s.id === initialSnapshotId || s.short_id === initialSnapshotId);
            if (target) {
              browseSnapshot(target, initialPath ?? "/");
            }
          }
        }
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setError(getErrorMessage(err, t('snapshots.loadFailed')));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [token, taskId]);

  const browseSnapshot = (snapshot: ResticSnapshot, path = "/") => {
    setSelectedSnapshot(snapshot);
    setCurrentPath(path);
    setFilesLoading(true);
    setSelectedPaths(new Set());
    apiClient
      .listSnapshotFiles(token, taskId, snapshot.id, path)
      .then(setFiles)
      .catch((err) => toast.error(getErrorMessage(err, t('snapshots.fileLoadFailed'))))
      .finally(() => setFilesLoading(false));
  };

  const navigateTo = (path: string) => {
    if (!selectedSnapshot) return;
    browseSnapshot(selectedSnapshot, path);
  };

  const togglePath = (path: string) => {
    setSelectedPaths((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  const resetGrantPromptState = () => {
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(false);
  };

  const handleGrantDialogChange = (open: boolean) => {
    if (restoring) return;
    if (!open) {
      resetGrantPromptState();
      return;
    }
    setGrantDialogOpen(true);
  };

  const handleRestore = () => {
    if (!selectedSnapshot || selectedPaths.size === 0) return;
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(true);
  };

  const handleRestoreGrantSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!selectedSnapshot || selectedPaths.size === 0) return;
    const reason = grantReason.trim();
    if (!reason) {
      setGrantError(t('snapshots.grantReasonRequired'));
      return;
    }
    if (Array.from(reason).length > SNAPSHOT_RESTORE_GRANT_MAX_REASON_LENGTH) {
      setGrantError(t('snapshots.grantReasonTooLong', { max: SNAPSHOT_RESTORE_GRANT_MAX_REASON_LENGTH }));
      return;
    }

    const snapshotId = selectedSnapshot.id;
    const includes = Array.from(selectedPaths);
    const targetPath = restoreTarget;
    setRestoring(true);
    setGrantError(null);
    try {
      const proof = await ensureStepUpProof(
        STEP_UP_ACTIONS.snapshotRestore,
        { persist: false, reuseCached: false },
      );
      await apiClient.requestSnapshotRestoreCredentialGrant(
        token,
        {
          taskId,
          reason,
          requestedTtlSeconds: SNAPSHOT_RESTORE_GRANT_TTL_SECONDS,
        },
        proof,
      );
      await apiClient.restoreSnapshot(token, taskId, snapshotId, includes, targetPath, proof);
      toast.success(t('snapshots.restoreSuccess', { count: includes.length, target: targetPath }));
      setSelectedPaths(new Set());
      resetGrantPromptState();
    } catch (err) {
      setGrantError(getErrorMessage(err, t('snapshots.restoreFailed')));
    } finally {
      setRestoring(false);
    }
  };

  if (loading) {
    return <LoadingState title={t('snapshots.loadingList')} rows={3} />;
  }

  if (error) {
    return <p className="text-sm text-destructive">{error}</p>;
  }

  if (selectedSnapshot) {
    const breadcrumbs = currentPath.split("/").filter(Boolean);
    return (
      <>
        <div className="space-y-3">
          <div className="flex items-center gap-2">
            <Button size="sm" variant="ghost" onClick={() => setSelectedSnapshot(null)}>
              <ArrowLeft className="mr-1 size-3.5" aria-hidden />
              {t('snapshots.backToList')}
            </Button>
            <span className="text-xs text-muted-foreground">
              {t('snapshots.snapshotLabel', { id: selectedSnapshot.short_id, time: formatTime(selectedSnapshot.time) })}
            </span>
            <BackupAssetsTaskContextLink taskId={taskId} className="ml-auto" />
          </div>

          {/* Breadcrumb navigation */}
          <div className="flex items-center gap-1 text-xs text-muted-foreground flex-wrap">
            <button
              type="button"
              className="hover:text-foreground underline-offset-2 hover:underline"
              onClick={() => navigateTo("/")}
            >
              /
            </button>
            {breadcrumbs.map((part, i) => {
              const path = "/" + breadcrumbs.slice(0, i + 1).join("/");
              return (
                <span key={path} className="flex items-center gap-1">
                  <span>/</span>
                  <button
                    type="button"
                    className="hover:text-foreground underline-offset-2 hover:underline"
                    onClick={() => navigateTo(path)}
                  >
                    {part}
                  </button>
                </span>
              );
            })}
          </div>

          {filesLoading ? (
            <LoadingState title={t('snapshots.loadingFiles')} rows={3} />
          ) : (
            <div className="rounded-md border border-border/60 divide-y divide-border/30 max-h-64 overflow-y-auto">
              {files.length === 0 && (
                <p className="px-3 py-4 text-sm text-muted-foreground text-center">{t('common.noData')}</p>
              )}
              {files.map((entry) => (
                <label
                  key={entry.path}
                  className="flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-muted/40 cursor-pointer"
                >
                  <input
                    type="checkbox"
                    checked={selectedPaths.has(entry.path)}
                    onChange={() => togglePath(entry.path)}
                    className="size-3.5"
                  />
                  {entry.type === "dir" ? (
                    <Folder className="size-3.5 text-primary shrink-0" aria-hidden />
                  ) : (
                    <File className="size-3.5 text-muted-foreground shrink-0" aria-hidden />
                  )}
                  {entry.type === "dir" ? (
                    <button
                      type="button"
                      className="text-left truncate hover:underline underline-offset-2"
                      onClick={(e) => {
                        e.preventDefault();
                        navigateTo(entry.path);
                      }}
                    >
                      {entry.name}
                    </button>
                  ) : (
                    <span className="truncate">{entry.name}</span>
                  )}
                  {entry.type !== "dir" && entry.size > 0 && (
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                      {formatBytes(entry.size)}
                    </span>
                  )}
                </label>
              ))}
            </div>
          )}

          {selectedPaths.size > 0 && (
            <div className="flex items-center gap-2 pt-1">
              <input
                type="text"
                value={restoreTarget}
                onChange={(e) => setRestoreTarget(e.target.value)}
                placeholder={t('snapshots.restoreTargetPlaceholder')}
                aria-label={t('snapshots.restoreTargetPlaceholder')}
                className="flex-1 rounded-md border border-border bg-background px-2 py-1 text-sm"
              />
              <Button size="sm" onClick={handleRestore} disabled={restoring}>
                {restoring ? (
                  <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden />
                ) : (
                  <Download className="mr-1 size-3.5" aria-hidden />
                )}
                {t('snapshots.restoreCount', { count: selectedPaths.size })}
              </Button>
            </div>
          )}
        </div>

        <Dialog open={grantDialogOpen} onOpenChange={handleGrantDialogChange}>
          <DialogContent size="sm">
            <form onSubmit={handleRestoreGrantSubmit}>
              <DialogHeader>
                <DialogTitle>{t('snapshots.grantTitle')}</DialogTitle>
                <DialogDescription>{t('snapshots.grantDescription')}</DialogDescription>
              </DialogHeader>
              <DialogBody className="space-y-3">
                <div className="space-y-1.5">
                  <label className="text-sm font-medium" htmlFor="snapshot-restore-grant-reason">
                    {t('snapshots.grantReasonLabel')}
                  </label>
                  <Textarea
                    id="snapshot-restore-grant-reason"
                    value={grantReason}
                    onChange={(event) => setGrantReason(event.target.value)}
                    maxLength={SNAPSHOT_RESTORE_GRANT_MAX_REASON_LENGTH}
                    placeholder={t('snapshots.grantReasonPlaceholder')}
                    disabled={restoring}
                    aria-describedby="snapshot-restore-grant-reason-hint"
                    aria-invalid={grantError ? true : undefined}
                  />
                  <p id="snapshot-restore-grant-reason-hint" className="text-xs text-muted-foreground">
                    {t('snapshots.grantReasonHint', { max: SNAPSHOT_RESTORE_GRANT_MAX_REASON_LENGTH })}
                  </p>
                </div>
                {grantError ? (
                  <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive" role="alert">
                    {grantError}
                  </p>
                ) : null}
              </DialogBody>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={resetGrantPromptState} disabled={restoring}>
                  {t('common.cancel')}
                </Button>
                <Button type="submit" loading={restoring}>
                  {t('snapshots.grantSubmit')}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
      </>
    );
  }

  return (
    <div className="space-y-2">
      <div className="flex justify-end">
        <BackupAssetsTaskContextLink taskId={taskId} />
      </div>
      {snapshots.length === 0 ? (
        <p className="text-sm text-muted-foreground text-center py-4">{t('common.noData')}</p>
      ) : (
        <div className="rounded-md border border-border/60 divide-y divide-border/30 max-h-64 overflow-y-auto">
          {snapshots.map((snap) => (
            <button
              key={snap.id}
              type="button"
              className="w-full flex items-center gap-3 px-3 py-2 text-sm hover:bg-muted/40 text-left"
              onClick={() => browseSnapshot(snap)}
            >
              <Folder className="size-4 text-primary shrink-0" aria-hidden />
              <div className="flex-1 min-w-0">
                <div className="font-medium truncate">{snap.short_id}</div>
                <div className="text-xs text-muted-foreground">
                  {formatTime(snap.time)}
                  {snap.hostname && ` · ${snap.hostname}`}
                </div>
              </div>
              {snap.paths?.length > 0 && (
                <span className="text-xs text-muted-foreground shrink-0">
                  {snap.paths[0]}
                </span>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
