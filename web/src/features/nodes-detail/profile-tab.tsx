import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { apiClient } from "@/lib/api/client";
import type { NodeRecord } from "@/types/domain";
import type { NodeDetailTabProps } from "./types";

export default function ProfileTab({ nodeId, token }: NodeDetailTabProps) {
  const { t } = useTranslation();
  const [node, setNode] = useState<NodeRecord | null>(null);
  // Start loading when we expect a fetch so stale content cannot paint first frame.
  const [loading, setLoading] = useState(() => Boolean(token && nodeId > 0));
  const [error, setError] = useState(false);
  // nodeId that `node` was loaded for — ignore mismatched stale state.
  const [loadedForId, setLoadedForId] = useState<number | null>(null);

  const fetchNode = useCallback(async (signal: AbortSignal) => {
    if (!token || nodeId <= 0) {
      setLoading(false);
      setNode(null);
      setLoadedForId(null);
      return;
    }
    setLoading(true);
    setError(false);
    try {
      const nodes = await apiClient.getNodes(token, { signal });
      if (!signal.aborted) {
        setNode(nodes.find((n) => n.id === nodeId) ?? null);
        setLoadedForId(nodeId);
      }
    } catch {
      if (!signal.aborted) {
        setNode(null);
        setLoadedForId(nodeId);
        setError(true);
      }
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [nodeId, token]);

  useEffect(() => {
    // Drop previous identity immediately (also remounted via key={nodeId} on parent).
    setNode(null);
    setLoadedForId(null);
    setError(false);
    setLoading(Boolean(token && nodeId > 0));
    const controller = new AbortController();
    void fetchNode(controller.signal);
    return () => controller.abort();
  }, [fetchNode, nodeId, token]);

  if (loading || (token && nodeId > 0 && loadedForId !== nodeId && !error)) {
    return <p className="text-sm text-muted-foreground">{t("nodes.nodeDetail.loading")}</p>;
  }
  if (error) {
    return (
      <p className="text-sm text-destructive" role="alert">
        {t("nodes.nodeDetail.profileError")}
      </p>
    );
  }
  if (node == null || node.id !== nodeId || loadedForId !== nodeId) {
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
          <dd>{node.tags.length > 0 ? node.tags.join(", ") : "-"}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileStatus")}</dt>
          <dd>{node.status}</dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileBackupDir")}</dt>
          <dd>{node.backupDir || "-"}</dd>
        </dl>
      </section>

      <section className="rounded-md border border-border bg-card p-4">
        <h3 className="text-base font-medium">{t("nodes.nodeDetail.profileTimeMaint")}</h3>
        <dl className="mt-3 grid grid-cols-[140px_1fr] gap-y-2 text-sm">
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileLastProbe")}</dt>
          <dd>
            {node.lastProbeAt ? new Date(node.lastProbeAt).toLocaleString() : "-"}
          </dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileLastSeen")}</dt>
          <dd>
            {node.lastSeenAt ? new Date(node.lastSeenAt).toLocaleString() : "-"}
          </dd>
          <dt className="text-muted-foreground">{t("nodes.nodeDetail.profileLastBackup")}</dt>
          <dd>
            {node.lastBackupAt ? new Date(node.lastBackupAt).toLocaleString() : "-"}
          </dd>
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
