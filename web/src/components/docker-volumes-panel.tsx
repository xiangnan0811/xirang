import { useEffect, useState } from "react";
import { Loader2, HardDrive, FolderInput } from "lucide-react";
import { useTranslation } from "react-i18next";
import { apiClient } from "@/lib/api/client";
import type { DockerVolume } from "@/lib/api/docker-api";

type Props = {
  nodeId: number;
  token: string;
  onSelectPath?: (path: string) => void;
};

export function DockerVolumesPanel({ nodeId, token, onSelectPath }: Props) {
  const { t } = useTranslation();
  const [volumes, setVolumes] = useState<DockerVolume[]>([]);
  const [warning, setWarning] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();

  useEffect(() => {
    let cancelled = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setError(undefined);
    // eslint-disable-next-line react-hooks/set-state-in-effect
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
        setError(err instanceof Error ? err.message : t('dockerVolumes.loadFailed'));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [nodeId, token, t]);

  if (loading) {
    return (
      <div className="flex items-center justify-center py-6">
        <Loader2 className="size-4 animate-spin text-muted-foreground" aria-hidden="true" />
        <span className="ml-2 text-sm text-muted-foreground">
          {t('dockerVolumes.scanning')}
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
        <HardDrive className="mx-auto mb-2 size-5 opacity-40" aria-hidden="true" />
        <p>{t('dockerVolumes.noVolumes')}</p>
        {warning && (
          <p className="mt-1 text-xs text-muted-foreground/70">{warning}</p>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {warning && (
        <p className="text-xs text-warning">{warning}</p>
      )}
      <div className="rounded-md border border-border/60">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border/40 text-left text-xs text-muted-foreground">
              <th scope="col" className="px-3 py-2 font-medium">{t('dockerVolumes.colName')}</th>
              <th scope="col" className="px-3 py-2 font-medium">{t('dockerVolumes.colDriver')}</th>
              <th scope="col" className="px-3 py-2 font-medium">{t('dockerVolumes.colMountpoint')}</th>
              {onSelectPath && (
                <th scope="col" className="px-3 py-2 font-medium text-right">{t('common.actions')}</th>
              )}
            </tr>
          </thead>
          <tbody>
            {volumes.map((vol) => (
              <tr
                key={vol.name}
                className="border-b border-border/20 last:border-b-0 hover:bg-muted/40 transition-colors"
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
                      aria-label={t('dockerVolumes.usePathAriaLabel', { name: vol.name })}
                      title={t('dockerVolumes.useAsSource')}
                    >
                      <FolderInput className="size-3.5" aria-hidden="true" />
                      {t('dockerVolumes.usePath')}
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
