import { History } from "lucide-react";
import { useTranslation } from "react-i18next";

import { InlineAlert } from "@/components/ui/inline-alert";
import type { BackupRecoveryPoint } from "@/types/domain";

export interface AssetVersionsProps {
  recoveryPoint: BackupRecoveryPoint;
}

export function AssetVersions({ recoveryPoint }: AssetVersionsProps) {
  const { t } = useTranslation();

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

      <InlineAlert tone="info">
        {t("backupAssets.versions.expansionUnavailable")}
      </InlineAlert>
    </div>
  );
}

function formatTimestamp(value: string | null): string {
  return value ? value.slice(0, 16).replace("T", " ") : "-";
}
