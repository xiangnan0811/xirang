import { useState } from "react";
import { GitCompareArrows, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { Select } from "@/components/ui/select";
import type {
  BackupRecoveryPoint,
  EvidenceLayerStatus,
  RecoveryPointDiff,
  RecoveryPointEvidence,
} from "@/types/domain";

import type { BackupAssetsValueResource } from "./use-backup-assets-state";
import { presentBackupAssetsCode } from "./backup-assets-presenters";

export interface AssetEvidenceProps {
  mode: "evidence" | "diff";
  recoveryPoints: BackupRecoveryPoint[];
  selectedRecoveryPoint: BackupRecoveryPoint;
  evidence: BackupAssetsValueResource<RecoveryPointEvidence>;
  diff: BackupAssetsValueResource<RecoveryPointDiff>;
  onCompare: (baseRecoveryPointId: string, compareRecoveryPointId: string) => void;
}

export function AssetEvidence({
  mode,
  recoveryPoints,
  selectedRecoveryPoint,
  evidence,
  diff,
  onCompare,
}: AssetEvidenceProps) {
  return mode === "evidence" ? (
    <EvidenceLayers resource={evidence} />
  ) : (
    <RecoveryPointComparison
      recoveryPoints={recoveryPoints}
      selectedRecoveryPoint={selectedRecoveryPoint}
      resource={diff}
      onCompare={onCompare}
    />
  );
}

function EvidenceLayers({ resource }: { resource: BackupAssetsValueResource<RecoveryPointEvidence> }) {
  const { t } = useTranslation();
  if (resource.status === "loading") {
    return <LoadingState title={t("backupAssets.evidence.loading")} rows={4} />;
  }
  if (resource.status === "blocked" || resource.status === "error") {
    return (
      <div className="p-3">
        <InlineAlert tone={resource.status === "blocked" ? "warning" : "critical"}>
          {t(resource.error?.translationKey ?? "backupAssets.errors.unknown")}
        </InlineAlert>
      </div>
    );
  }
  if (resource.status !== "ready" || !resource.value) {
    return <EvidenceEmpty text={t("backupAssets.evidence.notLoaded")} />;
  }

  const value = resource.value;
  const layers = [
    {
      key: "lineage",
      label: t("backupAssets.evidence.lineage"),
      status: value.lineage.status,
      detail: value.lineage.taskName || value.lineage.nodeName || "-",
    },
    {
      key: "manifest",
      label: t("backupAssets.evidence.manifest"),
      status: value.manifest.status,
      detail: value.manifest.completeness
        ? t(`backupAssets.evidence.completeness.${value.manifest.completeness}`)
        : "-",
    },
    {
      key: "publication",
      label: t("backupAssets.evidence.providerVerification"),
      status: value.publicationVerification.status,
      detail: value.publicationVerification.completion
        ? t(`backupAssets.evidence.completion.${value.publicationVerification.completion}`)
        : "-",
    },
    {
      key: "drills",
      label: t("backupAssets.evidence.restoreDrills"),
      status: value.restoreDrills.status,
      detail: t("backupAssets.evidence.drillCount", { count: value.restoreDrills.items.length }),
    },
  ];

  return (
    <div className="divide-y divide-border px-3">
      {layers.map((layer) => (
        <section key={layer.key} className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-3">
          <div className="min-w-0">
            <h3 className="truncate text-sm font-medium">{layer.label}</h3>
            <p className="mt-1 break-words text-xs text-muted-foreground">{layer.detail}</p>
          </div>
          <EvidenceStatus status={layer.status} />
        </section>
      ))}
    </div>
  );
}

function RecoveryPointComparison({
  recoveryPoints,
  selectedRecoveryPoint,
  resource,
  onCompare,
}: {
  recoveryPoints: BackupRecoveryPoint[];
  selectedRecoveryPoint: BackupRecoveryPoint;
  resource: BackupAssetsValueResource<RecoveryPointDiff>;
  onCompare: (baseRecoveryPointId: string, compareRecoveryPointId: string) => void;
}) {
  const { t } = useTranslation();
  const candidates = recoveryPoints.filter((point) => point.id !== selectedRecoveryPoint.id);
  const [compareId, setCompareId] = useState("");

  return (
    <div className="space-y-4 p-3">
      <div className="flex flex-wrap items-end gap-2 border-b border-border pb-3">
        <div className="min-w-48 flex-1 space-y-1.5">
          <label htmlFor="backup-assets-compare-point" className="text-xs font-medium text-muted-foreground">
            {t("backupAssets.diff.comparePoint")}
          </label>
          <Select
            id="backup-assets-compare-point"
            aria-label={t("backupAssets.diff.comparePoint")}
            value={compareId}
            onChange={(event) => setCompareId(event.target.value)}
          >
            <option value="">{t("backupAssets.diff.selectPoint")}</option>
            {candidates.map((point) => (
              <option key={point.id} value={point.id}>
                {point.producingTaskName} · {formatTimestamp(point.capturedAt)}
              </option>
            ))}
          </Select>
        </div>
        <Button
          type="button"
          size="sm"
          disabled={compareId === ""}
          onClick={() => onCompare(selectedRecoveryPoint.id, compareId)}
        >
          <GitCompareArrows className="size-4" aria-hidden />
          {t("backupAssets.actions.compareRecoveryPoints")}
        </Button>
      </div>

      <section aria-labelledby="backup-assets-catalog-diff-title">
        <h3 id="backup-assets-catalog-diff-title" className="text-sm font-medium">
          {t("backupAssets.diff.catalog")}
        </h3>
        <DiffBody resource={resource} />
      </section>
    </div>
  );
}

function DiffBody({ resource }: { resource: BackupAssetsValueResource<RecoveryPointDiff> }) {
  const { t } = useTranslation();
  if (resource.status === "loading") {
    return <LoadingState title={t("backupAssets.diff.loading")} rows={3} />;
  }
  if (resource.status === "blocked" || resource.status === "error") {
    return (
      <InlineAlert tone={resource.status === "blocked" ? "warning" : "critical"} className="mt-2">
        {t(resource.error?.translationKey ?? "backupAssets.errors.unknown")}
      </InlineAlert>
    );
  }
  if (resource.status !== "ready" || !resource.value) {
    return <p className="mt-2 text-xs text-muted-foreground">{t("backupAssets.diff.notLoaded")}</p>;
  }

  const providerReason = resource.value.providerEvidence.reason
    ? presentBackupAssetsCode("capability", resource.value.providerEvidence.reason.code)
    : null;

  return (
    <div className="mt-2 space-y-3">
      <p className="text-xs text-muted-foreground">
        {t("backupAssets.diff.changeCount", { count: resource.value.items.length })}
      </p>
      {resource.value.items.length > 0 ? (
        <ul aria-label={t("backupAssets.diff.items")} className="divide-y divide-border border-y border-border">
          {resource.value.items.map((item, index) => {
            const name = item.compare?.name ?? item.base?.name ?? "-";
            const fields = item.changedFields
              .map((field) => t(`backupAssets.diff.field.${field}`))
              .join(", ");
            return (
              <li
                key={`${item.kind}:${item.base?.ref.entryId ?? "none"}:${item.compare?.ref.entryId ?? "none"}:${index}`}
                className="min-w-0 py-2.5"
              >
                <div className="flex min-w-0 items-center gap-2">
                  <Badge
                    tone={item.kind === "added" ? "success" : item.kind === "removed" ? "destructive" : "warning"}
                  >
                    {t(`backupAssets.diff.kind.${item.kind}`)}
                  </Badge>
                  <span className="min-w-0 flex-1 truncate text-sm font-medium" title={name}>
                    {name}
                  </span>
                </div>
                <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <span>{t("backupAssets.diff.changedFields", { fields })}</span>
                  <span>{t(`backupAssets.diff.contentEquality.${item.contentEquality}`)}</span>
                </div>
              </li>
            );
          })}
        </ul>
      ) : null}
      <div className="flex items-start gap-2 border-t border-border pt-3 text-sm">
        <ShieldCheck className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
        <div>
          <div className="font-medium">{t("backupAssets.diff.providerEvidence")}</div>
          <div className="mt-1 text-xs text-muted-foreground">
            {t(`backupAssets.diff.providerStatus.${resource.value.providerEvidence.status}`)}
          </div>
          {providerReason ? (
            <div className="mt-1 text-xs text-muted-foreground">{t(providerReason.translationKey)}</div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function EvidenceStatus({ status }: { status: EvidenceLayerStatus }) {
  const { t } = useTranslation();
  return (
    <span className="whitespace-nowrap text-xs font-medium" role="status">
      {t(`backupAssets.evidence.status.${status}`)}
    </span>
  );
}

function EvidenceEmpty({ text }: { text: string }) {
  return (
    <div className="flex min-h-40 items-center justify-center px-4 text-center text-sm text-muted-foreground">
      {text}
    </div>
  );
}

function formatTimestamp(value: string | null): string {
  return value ? value.slice(0, 16).replace("T", " ") : "-";
}
