import { useTranslation } from "react-i18next";
import type { NodeRecord } from "@/types/domain";

type ProfileTabProps = {
  /** Exact-id record from the page-level `useNodeRecord`; archived nodes included. */
  node: NodeRecord | null;
  isLoading: boolean;
  /** Record request failed for a reason other than 404. */
  loadFailed: boolean;
};

function formatRecordedTime(value: string): string {
  if (!value || value === "-") {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export default function ProfileTab({ node, isLoading, loadFailed }: ProfileTabProps) {
  const { t } = useTranslation();

  if (isLoading) {
    return <p className="text-sm text-muted-foreground">{t("nodes.nodeDetail.loading")}</p>;
  }
  if (loadFailed) {
    return (
      <p className="text-sm text-destructive" role="alert">
        {t("nodes.nodeDetail.profileError")}
      </p>
    );
  }
  if (node == null) {
    return <p className="text-sm text-muted-foreground">{t("nodes.nodeDetail.profileNotFound")}</p>;
  }

  return (
    <div className="grid grid-cols-1 gap-6 md:grid-cols-2" data-testid="profile-tab">
      <section className="rounded-md border border-border bg-card p-4">
        <h3 className="text-base font-medium">{t("nodes.nodeDetail.profileBasic")}</h3>
        <dl className="mt-3 grid grid-cols-[120px_1fr] gap-y-2 text-sm">
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileName")}</dt>
          <dd>{node.name}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileAddress")}</dt>
          <dd>
            {node.host}:{node.port}
          </dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileUsername")}</dt>
          <dd>{node.username}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileTags")}</dt>
          <dd className="min-w-0 [overflow-wrap:anywhere]">{node.tags.length > 0 ? node.tags.join(", ") : "-"}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileStatus")}</dt>
          <dd>{node.status}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileBackupDir")}</dt>
          <dd>{node.backupDir || "-"}</dd>
        </dl>
      </section>

      <section className="rounded-md border border-border bg-card p-4">
        <h3 className="text-base font-medium">{t("nodes.nodeDetail.profileTimeMaint")}</h3>
        <dl className="mt-3 grid grid-cols-[140px_1fr] gap-y-2 text-sm">
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileLastSeen")}</dt>
          <dd>{formatRecordedTime(node.lastSeenAt)}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileLatency")}</dt>
          <dd>{node.connectionLatencyMs != null ? `${node.connectionLatencyMs} ms` : "-"}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileLastBackup")}</dt>
          <dd>{formatRecordedTime(node.lastBackupAt)}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileMaintWindow")}</dt>
          <dd>
            {node.maintenanceStart || node.maintenanceEnd
              ? `${node.maintenanceStart ?? "?"} → ${node.maintenanceEnd ?? "?"}`
              : t("nodes.nodeDetail.profileMaintUnset")}
          </dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileArchived")}</dt>
          <dd>{node.archived ? t("nodes.nodeDetail.yes") : t("nodes.nodeDetail.no")}</dd>
        </dl>
      </section>
    </div>
  );
}
