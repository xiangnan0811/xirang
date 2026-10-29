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
  const [partial, setPartial] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();

  useEffect(() => {
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);

    setError(undefined);

    setWarning(undefined);

    apiClient
      .listDockerVolumes(token, nodeId, controller.signal)
      .then((res) => {
        if (controller.signal.aborted) return;
        setVolumes(res.volumes);
        setWarning(res.warning);
        setPartial(res.partial);
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        setError(err instanceof Error ? err.message : t('dockerVolumes.loadFailed'));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });

    return () => {
      controller.abort();
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
        {!partial && <p>{t("dockerVolumes.noVolumes")}</p>}
        {warning && (
          <p className="mt-1 text-xs text-[hsl(var(--warning-text))]">{warning}</p>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {warning && (
        <p className="text-xs text-[hsl(var(--warning-text))]">{warning}</p>
      )}
      <div
        role="region"
        aria-label={t("nodes.tabDockerVolumes")}
        // Keyboard focus enables native horizontal scrolling.
        // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
        tabIndex={0}
        className="overflow-x-auto rounded-md border border-border/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      >
        <table className="w-full min-w-[480px] text-sm">
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
                <td className="px-3 py-2 font-mono text-xs [overflow-wrap:anywhere]">{vol.name}</td>
                <td className="px-3 py-2 text-muted-foreground">{vol.driver}</td>
                <td className="px-3 py-2 font-mono text-xs [overflow-wrap:anywhere]">
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
