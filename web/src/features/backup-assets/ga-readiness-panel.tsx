import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import {
  createBackupGaApi,
  type BackupGaApi,
  type BackupGaReadiness,
  type BackupGaReadinessStatus,
} from "@/lib/api/backup-ga-api";
import type { AuthContextValue } from "@/context/auth-context.shared";

export interface GaReadinessPanelProps {
  token: string;
  role: AuthContextValue["role"];
  api?: Partial<BackupGaApi>;
}

function canEnable(snapshot: BackupGaReadiness): boolean {
  return (
    (snapshot.class === "fresh" && snapshot.status === "ready") ||
    snapshot.status === "acknowledged"
  );
}

function statusTone(status: BackupGaReadinessStatus): "info" | "warning" | "success" {
  if (status === "ready" || status === "acknowledged") {
    return "success";
  }
  if (status === "blocked") {
    return "warning";
  }
  return "info";
}

export function GaReadinessPanel({ token, role, api }: GaReadinessPanelProps) {
  const { t } = useTranslation();
  const client = useMemo(() => ({ ...createBackupGaApi(), ...api }), [api]);
  const [snapshot, setSnapshot] = useState<BackupGaReadiness | null>(null);
  const [busy, setBusy] = useState<"inventory" | "acknowledge" | "enable" | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (role !== "admin" || !token) {
      return;
    }
    const controller = new AbortController();
    void client.getReadiness(token, controller.signal).then(setSnapshot).catch(() => {
      if (!controller.signal.aborted) {
        setError(t("backupAssets.ga.loadFailed"));
      }
    });
    return () => controller.abort();
  }, [client, role, t, token]);

  if (role !== "admin") {
    return null;
  }
  if (snapshot === null && error) {
    return <InlineAlert tone="critical">{error}</InlineAlert>;
  }
  if (snapshot === null) {
    return <LoadingState title={t("backupAssets.ga.loading")} rows={4} />;
  }

  const run = async (action: "inventory" | "acknowledge" | "enable") => {
    if (!snapshot && action !== "inventory") {
      return;
    }
    setBusy(action);
    setError(null);
    try {
      if (action === "inventory") {
        setSnapshot(await client.runInventory(token));
        return;
      }
      if (action === "acknowledge" && snapshot) {
        setSnapshot(await client.acknowledge(token, snapshot.inventoryDigest));
        return;
      }
      await client.enable?.(token);
    } catch {
      setError(t("backupAssets.ga.actionFailed"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Card id="backup-assets-ga" role="region" aria-labelledby="backup-assets-ga-title">
      <CardHeader>
        <CardTitle id="backup-assets-ga-title" as="h2">
          {t("backupAssets.ga.title")}
        </CardTitle>
        <CardDescription>{t("backupAssets.ga.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone={snapshot.class === "fresh" ? "info" : "warning"}>
            {snapshot.class === "fresh" ? t("backupAssets.ga.classFresh") : t("backupAssets.ga.classExisting")}
          </Badge>
          <InlineAlert tone={statusTone(snapshot.status)}>
            {t("backupAssets.ga.statusAnnouncement", { status: t(`backupAssets.ga.status.${snapshot.status}`) })}
          </InlineAlert>
        </div>
        <p className="text-sm text-muted-foreground">{t("backupAssets.ga.workerOptional")}</p>
        <dl className="grid grid-cols-1 gap-2 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">{t("backupAssets.ga.inventoryComplete")}</dt>
            <dd>{snapshot.inventoryComplete ? t("backupAssets.ga.ok") : t("backupAssets.ga.missing")}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("backupAssets.ga.exportRoot")}</dt>
            <dd>{snapshot.exportRootValid ? t("backupAssets.ga.ok") : t("backupAssets.ga.missing")}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("backupAssets.ga.keyDomains")}</dt>
            <dd>{snapshot.keyDomainsReady ? t("backupAssets.ga.ok") : t("backupAssets.ga.missing")}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("backupAssets.ga.counts")}</dt>
            <dd>
              {t("backupAssets.ga.countSummary", {
                candidates: snapshot.counts.candidates,
                conflicts: snapshot.counts.conflicts,
                unsupported: snapshot.counts.unsupported,
              })}
            </dd>
          </div>
        </dl>
        {snapshot.conflicts.length > 0 ? (
          <ul className="space-y-1 text-sm" aria-label={t("backupAssets.ga.conflicts")}>
            {snapshot.conflicts.map((conflict) => (
              <li key={`${conflict.kind}-${conflict.taskIds.join(",")}-${conflict.repositoryId}`}>
                <span>{conflict.kind}</span>
                <span className="text-muted-foreground">
                  {" "}
                  {t(`backupAssets.ga.conflictKind.${conflict.kind}`)}
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground">{t("backupAssets.ga.noConflicts")}</p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            size="sm"
            disabled={busy !== null}
            onClick={() => void run("inventory")}
          >
            {t("backupAssets.ga.runInventory")}
          </Button>
          {snapshot.class === "existing" && snapshot.status !== "acknowledged" ? (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={busy !== null || !snapshot.inventoryComplete}
              onClick={() => void run("acknowledge")}
            >
              {t("backupAssets.ga.acknowledge")}
            </Button>
          ) : null}
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={busy !== null || !canEnable(snapshot)}
            onClick={() => void run("enable")}
          >
            {t("backupAssets.ga.enable")}
          </Button>
        </div>
        {error ? <InlineAlert tone="critical">{error}</InlineAlert> : null}
      </CardContent>
    </Card>
  );
}
