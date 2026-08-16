import { useTranslation } from "react-i18next";

import { InlineAlert } from "@/components/ui/inline-alert";
import type { RecoveryPreflight } from "@/lib/api/backup-recovery-api";
import { formatBytes } from "@/lib/utils";

export interface RecoveryImpactPanelProps {
  preflight: RecoveryPreflight;
}

export function RecoveryImpactPanel({ preflight }: RecoveryImpactPanelProps) {
  const { t } = useTranslation();
  const impact = preflight.impact;

  return (
    <section aria-labelledby="recovery-impact-title" className="space-y-3">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h3 id="recovery-impact-title" className="text-sm font-semibold">
            {t("backupAssets.recovery.impact.title")}
          </h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("backupAssets.recovery.impact.authoritative")}
          </p>
        </div>
        <output
          data-testid="recovery-impact-bytes"
          className="font-mono text-sm tabular-nums"
          aria-label={t("backupAssets.recovery.impact.estimatedBytes")}
        >
          {formatBytes(impact.estimatedBytes)}
        </output>
      </div>

      <dl
        data-testid="recovery-impact-counts"
        className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-border bg-border sm:grid-cols-4"
      >
        {(["createCount", "overwriteCount", "skipCount", "deleteCount"] as const).map((key) => (
          <div key={key} className="bg-card px-3 py-2.5">
            <dt className="text-[11px] text-muted-foreground">
              {t(`backupAssets.recovery.impact.${key}`)}
            </dt>
            <dd className="mt-1 font-mono text-base font-semibold tabular-nums">{impact[key]}</dd>
          </div>
        ))}
      </dl>

      {impact.deleteCount > 0 ? (
        <InlineAlert tone="critical" live={false} title={t("backupAssets.recovery.impact.destructiveTitle")}>
          {t("backupAssets.recovery.impact.destructiveBody", { count: impact.deleteCount })}
        </InlineAlert>
      ) : impact.overwriteCount > 0 ? (
        <InlineAlert tone="warning" live={false}>
          {t("backupAssets.recovery.impact.overwriteBody", { count: impact.overwriteCount })}
        </InlineAlert>
      ) : null}

      {preflight.expiresAt !== null ? (
        <p data-testid="recovery-preflight-expiry" className="text-xs text-muted-foreground">
          {t("backupAssets.recovery.impact.expiresAt")}{" "}
          <time dateTime={preflight.expiresAt} className="font-mono tabular-nums">
            {new Date(preflight.expiresAt).toLocaleString()}
          </time>
        </p>
      ) : null}
    </section>
  );
}
