import { useEffect, useState } from "react";
import { Loader2, HardDrive, FolderInput } from "lucide-react";
import { apiClient } from "@/lib/api/client";
import type { DockerVolume } from "@/lib/api/docker-api";

type Props = {
  nodeId: number;
  token: string;
  onSelectPath?: (path: string) => void;
};

export function DockerVolumesPanel({ nodeId, token, onSelectPath }: Props) {
  const [volumes, setVolumes] = useState<DockerVolume[]>([]);
  const [warning, setWarning] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(undefined);
    setWarning(undefined);

    apiClient
      .listDockerVolumes(token, nodeId)
      .then((res) => {
        if (cancelled) return;
        setVolumes(res.volumes);
        setWarning(res.warning);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "获取 Docker 卷失败");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [nodeId, token]);

  if (loading) {
    return (
      <div className="flex items-center justify-center py-6">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
        <span className="ml-2 text-sm text-muted-foreground">
          正在扫描 Docker 卷...
        </span>
      </div>
    );
  }

  if (error) {
    return (
      <p className="py-4 text-center text-sm text-destructive">{error}</p>
    );
  }

  if (volumes.length === 0) {
    return (
      <div className="py-4 text-center text-sm text-muted-foreground">
        <HardDrive className="mx-auto mb-2 size-5 opacity-40" />
        <p>未发现 Docker 卷（可能 Docker 未安装）</p>
        {warning && (
          <p className="mt-1 text-xs text-muted-foreground/70">{warning}</p>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {warning && (
        <p className="text-xs text-amber-600 dark:text-amber-400">{warning}</p>
      )}
      <div className="rounded-md border border-border/60">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border/40 text-left text-xs text-muted-foreground">
              <th className="px-3 py-2 font-medium">卷名称</th>
              <th className="px-3 py-2 font-medium">驱动</th>
              <th className="px-3 py-2 font-medium">挂载路径</th>
              {onSelectPath && (
                <th className="px-3 py-2 font-medium text-right">操作</th>
              )}
            </tr>
          </thead>
          <tbody>
            {volumes.map((vol) => (
              <tr
                key={vol.name}
                className="border-b border-border/20 last:border-b-0 hover:bg-muted/30 transition-colors"
              >
                <td className="px-3 py-2 font-mono text-xs">{vol.name}</td>
                <td className="px-3 py-2 text-muted-foreground">{vol.driver}</td>
                <td className="px-3 py-2 font-mono text-xs">
                  {vol.mountpoint || "-"}
                </td>
                {onSelectPath && (
                  <td className="px-3 py-2 text-right">
                    <button
                      type="button"
                      className="inline-flex items-center gap-1 rounded px-2 py-1 text-xs text-primary hover:bg-primary/10 transition-colors"
                      onClick={() => onSelectPath(vol.mountpoint)}
                      disabled={!vol.mountpoint}
                      aria-label={`使用 ${vol.name} 的挂载路径`}
                      title="使用此路径作为备份源"
                    >
                      <FolderInput className="size-3.5" />
                      使用此路径
                    </button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
