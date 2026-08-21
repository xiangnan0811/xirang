import { useEffect, useState } from "react";
import { History } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { apiClient } from "@/lib/api/client";
import { mapBackupAssetsError, type BackupAssetsUIError } from "@/lib/api/backup-assets-error";
import type { AssetRef, BackupAsset, BackupAssetVersion, BackupRecoveryPoint } from "@/types/domain";

export interface AssetVersionsProps {
  token: string | null;
  asset: BackupAsset;
  recoveryPoint: BackupRecoveryPoint;
  onOpenVersion: (ref: AssetRef) => void;
}

export function AssetVersions({ token, asset, recoveryPoint, onOpenVersion }: AssetVersionsProps) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [items, setItems] = useState<BackupAssetVersion[]>([]);
  const [error, setError] = useState<BackupAssetsUIError>();

  useEffect(() => {
    if (!token) {
      setStatus("error");
      setItems([]);
      return;
    }
    const controller = new AbortController();
    setStatus("loading");
    setError(undefined);
    void apiClient
      .listAssetVersions(token, asset.ref, controller.signal)
      .then((projection) => {
        if (projection.status !== "available") {
          setStatus("error");
          setItems([]);
          return;
        }
        setItems(projection.value.items);
        setStatus("ready");
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        setError(mapBackupAssetsError(cause, "entry"));
        setStatus("error");
        setItems([]);
      });
    return () => controller.abort();
  }, [asset.ref.entryId, asset.ref.recoveryPointId, token]);

  return (
    <div className="space-y-4 p-3">
      <div className="min-w-0 border-b border-border pb-3">
        <div className="flex min-w-0 items-center gap-2">
          <History className="size-4 shrink-0 text-primary" aria-hidden />
          <span className="truncate text-sm font-medium" title={recoveryPoint.producingTaskName}>
            {recoveryPoint.producingTaskName}
          </span>
        </div>
        <dl className="mt-2 grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 text-xs">
          <dt className="text-muted-foreground">{t("backupAssets.versions.recoveryPoint")}</dt>
          <dd className="max-w-44 truncate font-mono" title={recoveryPoint.id}>
            {recoveryPoint.id}
          </dd>
          <dt className="text-muted-foreground">{t("backupAssets.versions.producedAt")}</dt>
          <dd>{formatTimestamp(recoveryPoint.capturedAt)}</dd>
        </dl>
      </div>

      {status === "loading" ? <LoadingState title={t("backupAssets.versions.loading")} rows={3} /> : null}
      {status === "error" ? (
        <InlineAlert tone="warning">
          {t(error?.translationKey ?? "backupAssets.versions.unavailable")}
        </InlineAlert>
      ) : null}
      {status === "ready" ? (
        <ul className="space-y-2" aria-label={t("backupAssets.versions.list")}>
          {items.map((item) => {
            const current =
              item.ref.recoveryPointId === asset.ref.recoveryPointId &&
              item.ref.entryId === asset.ref.entryId;
            return (
              <li key={`${item.ref.recoveryPointId}:${item.ref.entryId}`}>
                <Button
                  type="button"
                  variant={current ? "secondary" : "outline"}
                  className="h-auto w-full justify-between px-3 py-2 text-left"
                  onClick={() => onOpenVersion(item.ref)}
                >
                  <span className="truncate font-mono text-xs" title={item.ref.recoveryPointId}>
                    {item.ref.recoveryPointId}
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {formatTimestamp(item.capturedAt)}
                    {current ? ` · ${t("backupAssets.versions.current")}` : ""}
                  </span>
                </Button>
              </li>
            );
          })}
        </ul>
      ) : null}
    </div>
  );
}

function formatTimestamp(value: string | null): string {
  return value ? value.slice(0, 16).replace("T", " ") : "-";
}
