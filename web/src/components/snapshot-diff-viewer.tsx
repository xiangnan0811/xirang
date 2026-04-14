import { useEffect, useState } from "react";
import { ArrowLeftRight, File, FileMinus, FilePlus, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { apiClient } from "@/lib/api/client";
import { formatBytes, getErrorMessage } from "@/lib/utils";
import { formatTime } from "@/lib/api/core";
import type { ResticSnapshot } from "@/lib/api/snapshots-api";
import type { SnapshotDiff } from "@/lib/api/snapshot-diff-api";
import { toast } from "sonner";

interface SnapshotDiffViewerProps {
  taskId: number;
  token: string;
}

export function SnapshotDiffViewer({ taskId, token }: SnapshotDiffViewerProps) {
  const { t } = useTranslation();
  const [snapshots, setSnapshots] = useState<ResticSnapshot[]>([]);
  const [snapshotsLoading, setSnapshotsLoading] = useState(true);
  const [snap1, setSnap1] = useState("");
  const [snap2, setSnap2] = useState("");
  const [loading, setLoading] = useState(false);
  const [diff, setDiff] = useState<SnapshotDiff | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    setSnapshotsLoading(true);
    apiClient
      .listSnapshots(token, taskId)
      .then((data) => { if (!controller.signal.aborted) setSnapshots(data); })
      .catch((err) => { if (!controller.signal.aborted) toast.error(getErrorMessage(err, t('snapshots.loadFailed'))); })
      .finally(() => { if (!controller.signal.aborted) setSnapshotsLoading(false); });
    return () => controller.abort();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [token, taskId]);

  const handleCompare = async () => {
    if (!snap1 || !snap2 || snap1 === snap2) {
      toast.error(t('snapshots.selectTwoSnapshots'));
      return;
    }
    setLoading(true);
    setDiff(null);
    try {
      const result = await apiClient.diffSnapshots(token, taskId, snap1, snap2);
      setDiff(result);
    } catch (err) {
      toast.error(getErrorMessage(err, t('snapshots.compareFailed')));
    } finally {
      setLoading(false);
    }
  };

  if (snapshotsLoading) {
    return <LoadingState title={t('snapshots.loadingList')} rows={3} />;
  }

  if (snapshots.length < 2) {
    return <p className="text-xs text-muted-foreground">{t('snapshots.needTwoSnapshots')}</p>;
  }

  return (
    <div className="space-y-3">
      <div className="flex items-end gap-2 flex-wrap">
        <div>
          <label className="mb-1 block text-xs font-medium">{t('snapshots.snapshot1')}</label>
          <select
            className="rounded-md border border-border bg-background px-2 py-1.5 text-sm"
            value={snap1}
            onChange={(e) => setSnap1(e.target.value)}
          >
            <option value="">{t('snapshots.selectSnapshot')}</option>
            {snapshots.map((s) => (
              <option key={s.id} value={s.short_id}>
                {s.short_id} — {formatTime(s.time)}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="mb-1 block text-xs font-medium">{t('snapshots.snapshot2')}</label>
          <select
            className="rounded-md border border-border bg-background px-2 py-1.5 text-sm"
            value={snap2}
            onChange={(e) => setSnap2(e.target.value)}
          >
            <option value="">{t('snapshots.selectSnapshot')}</option>
            {snapshots.map((s) => (
              <option key={s.id} value={s.short_id}>
                {s.short_id} — {formatTime(s.time)}
              </option>
            ))}
          </select>
        </div>
        <Button size="sm" onClick={handleCompare} disabled={loading || !snap1 || !snap2 || snap1 === snap2}>
          {loading ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <ArrowLeftRight className="mr-1 size-3.5" />}
          {t('snapshots.compare')}
        </Button>
      </div>

      {diff && (
        <div className="space-y-2">
          <div className="flex gap-3 text-xs">
            <span className="text-success">+{diff.stats.added} {t('snapshots.added')}</span>
            <span className="text-destructive">-{diff.stats.removed} {t('snapshots.removed')}</span>
            <span className="text-warning">~{diff.stats.changed} {t('snapshots.changed')}</span>
          </div>

          <div className="rounded-md border border-border/60 divide-y divide-border/30 max-h-64 overflow-y-auto">
            {diff.changes.length === 0 && (
              <p className="px-3 py-4 text-sm text-muted-foreground text-center">{t('snapshots.noDifference')}</p>
            )}
            {diff.changes.map((change) => (
              <div key={change.path} className="flex items-center gap-2 px-3 py-1.5 text-sm">
                {change.type === "added" && <FilePlus className="size-3.5 text-success shrink-0" />}
                {change.type === "removed" && <FileMinus className="size-3.5 text-destructive shrink-0" />}
                {change.type === "changed" && <File className="size-3.5 text-warning shrink-0" />}
                <span className="truncate">{change.path}</span>
                {change.size_before != null && change.size_after != null && (
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                    {formatBytes(change.size_before)} → {formatBytes(change.size_after)}
                  </span>
                )}
                {change.size_before == null && change.size_after != null && (
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                    {formatBytes(change.size_after)}
                  </span>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
