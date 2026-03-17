import { useEffect, useState } from "react";
import { ArrowLeftRight, File, FileMinus, FilePlus, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { ResticSnapshot } from "@/lib/api/snapshots-api";
import type { SnapshotDiff } from "@/lib/api/snapshot-diff-api";
import { toast } from "sonner";

interface SnapshotDiffViewerProps {
  taskId: number;
  token: string;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

export function SnapshotDiffViewer({ taskId, token }: SnapshotDiffViewerProps) {
  const [snapshots, setSnapshots] = useState<ResticSnapshot[]>([]);
  const [snapshotsLoading, setSnapshotsLoading] = useState(true);
  const [snap1, setSnap1] = useState("");
  const [snap2, setSnap2] = useState("");
  const [loading, setLoading] = useState(false);
  const [diff, setDiff] = useState<SnapshotDiff | null>(null);

  useEffect(() => {
    setSnapshotsLoading(true);
    apiClient
      .listSnapshots(token, taskId)
      .then(setSnapshots)
      .catch((err) => toast.error(getErrorMessage(err, "加载快照列表失败")))
      .finally(() => setSnapshotsLoading(false));
  }, [token, taskId]);

  const handleCompare = async () => {
    if (!snap1 || !snap2 || snap1 === snap2) {
      toast.error("请选择两个不同的快照");
      return;
    }
    setLoading(true);
    setDiff(null);
    try {
      const result = await apiClient.diffSnapshots(token, taskId, snap1, snap2);
      setDiff(result);
    } catch (err) {
      toast.error(getErrorMessage(err, "比较失败"));
    } finally {
      setLoading(false);
    }
  };

  if (snapshotsLoading) {
    return <LoadingState title="加载快照列表..." rows={3} />;
  }

  if (snapshots.length < 2) {
    return <p className="text-xs text-muted-foreground">至少需要 2 个快照才能进行比较。</p>;
  }

  return (
    <div className="space-y-3">
      <div className="flex items-end gap-2 flex-wrap">
        <div>
          <label className="mb-1 block text-xs font-medium">快照 1</label>
          <select
            className="rounded-md border border-border bg-background px-2 py-1.5 text-sm"
            value={snap1}
            onChange={(e) => setSnap1(e.target.value)}
          >
            <option value="">选择快照...</option>
            {snapshots.map((s) => (
              <option key={s.id} value={s.short_id}>
                {s.short_id} — {new Date(s.time).toLocaleString("zh-CN")}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="mb-1 block text-xs font-medium">快照 2</label>
          <select
            className="rounded-md border border-border bg-background px-2 py-1.5 text-sm"
            value={snap2}
            onChange={(e) => setSnap2(e.target.value)}
          >
            <option value="">选择快照...</option>
            {snapshots.map((s) => (
              <option key={s.id} value={s.short_id}>
                {s.short_id} — {new Date(s.time).toLocaleString("zh-CN")}
              </option>
            ))}
          </select>
        </div>
        <Button size="sm" onClick={handleCompare} disabled={loading || !snap1 || !snap2 || snap1 === snap2}>
          {loading ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <ArrowLeftRight className="mr-1 size-3.5" />}
          比较
        </Button>
      </div>

      {diff && (
        <div className="space-y-2">
          <div className="flex gap-3 text-xs">
            <span className="text-green-600">+{diff.stats.added} 新增</span>
            <span className="text-red-600">-{diff.stats.removed} 删除</span>
            <span className="text-yellow-600">~{diff.stats.changed} 变更</span>
          </div>

          <div className="rounded-md border border-border/60 divide-y divide-border/30 max-h-64 overflow-y-auto">
            {diff.changes.length === 0 && (
              <p className="px-3 py-4 text-sm text-muted-foreground text-center">两个快照之间没有差异</p>
            )}
            {diff.changes.map((change) => (
              <div key={change.path} className="flex items-center gap-2 px-3 py-1.5 text-sm">
                {change.type === "added" && <FilePlus className="size-3.5 text-green-600 shrink-0" />}
                {change.type === "removed" && <FileMinus className="size-3.5 text-red-600 shrink-0" />}
                {change.type === "changed" && <File className="size-3.5 text-yellow-600 shrink-0" />}
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
