import { useEffect, useState } from "react";
import { ArrowLeft, Download, File, Folder, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { apiClient } from "@/lib/api/client";
import { formatBytes, getErrorMessage, getLocale } from "@/lib/utils";
import type { ResticSnapshot, ResticEntry } from "@/lib/api/snapshots-api";
import { toast } from "sonner";

interface SnapshotBrowserProps {
  taskId: number;
  token: string;
}

export function SnapshotBrowser({ taskId, token }: SnapshotBrowserProps) {
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

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    apiClient
      .listSnapshots(token, taskId)
      .then((data) => { if (!controller.signal.aborted) setSnapshots(data); })
      .catch((err) => { if (!controller.signal.aborted) setError(getErrorMessage(err, t('snapshots.loadFailed'))); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
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

  const handleRestore = async () => {
    if (!selectedSnapshot || selectedPaths.size === 0) return;
    setRestoring(true);
    try {
      await apiClient.restoreSnapshot(
        token,
        taskId,
        selectedSnapshot.id,
        Array.from(selectedPaths),
        restoreTarget
      );
      toast.success(t('snapshots.restoreSuccess', { count: selectedPaths.size, target: restoreTarget }));
      setSelectedPaths(new Set());
    } catch (err) {
      toast.error(getErrorMessage(err, t('snapshots.restoreFailed')));
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
      <div className="space-y-3">
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={() => setSelectedSnapshot(null)}>
            <ArrowLeft className="mr-1 size-3.5" />
            {t('snapshots.backToList')}
          </Button>
          <span className="text-xs text-muted-foreground">
            {t('snapshots.snapshotLabel', { id: selectedSnapshot.short_id, time: new Date(selectedSnapshot.time).toLocaleString(getLocale()) })}
          </span>
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
                  <Folder className="size-3.5 text-primary shrink-0" />
                ) : (
                  <File className="size-3.5 text-muted-foreground shrink-0" />
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
              className="flex-1 rounded-md border border-border bg-background px-2 py-1 text-sm"
            />
            <Button size="sm" onClick={handleRestore} disabled={restoring}>
              {restoring ? (
                <Loader2 className="mr-1 size-3.5 animate-spin" />
              ) : (
                <Download className="mr-1 size-3.5" />
              )}
              {t('snapshots.restoreCount', { count: selectedPaths.size })}
            </Button>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-2">
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
              <Folder className="size-4 text-primary shrink-0" />
              <div className="flex-1 min-w-0">
                <div className="font-medium truncate">{snap.short_id}</div>
                <div className="text-xs text-muted-foreground">
                  {new Date(snap.time).toLocaleString(getLocale())}
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

