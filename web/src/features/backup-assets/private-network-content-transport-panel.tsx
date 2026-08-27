import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/confirm-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Switch } from "@/components/ui/switch";
import {
  createBackupContentTransportApi,
  type BackupContentTransportSetting,
} from "@/lib/api/backup-content-transport-api";

const TARGET_ID = "backup-assets-content-transport";

type BackupContentTransportApi = ReturnType<typeof createBackupContentTransportApi>;

interface PrivateNetworkContentTransportPanelProps {
  token: string;
  api?: BackupContentTransportApi;
}

export function PrivateNetworkContentTransportPanel({
  token,
  api,
}: PrivateNetworkContentTransportPanelProps) {
  const { t } = useTranslation();
  const defaultApiRef = useRef<BackupContentTransportApi | null>(null);
  if (defaultApiRef.current === null) defaultApiRef.current = createBackupContentTransportApi();
  const transportApi = api ?? defaultApiRef.current;
  const [setting, setSetting] = useState<BackupContentTransportSetting | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState(false);
  const savePendingRef = useRef(false);
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    const controller = new AbortController();
    setSetting(null);
    setError(false);
    void transportApi.get(token, controller.signal)
      .then((next) => {
        if (!controller.signal.aborted) setSetting(next);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [token, transportApi]);

  useEffect(() => {
    if (setting === null || window.location.hash !== `#${TARGET_ID}`) return;
    const frame = window.requestAnimationFrame(() => headingRef.current?.focus());
    return () => window.cancelAnimationFrame(frame);
  }, [setting]);

  const save = useCallback(async (enabled: boolean) => {
    if (setting === null || savePendingRef.current) return;
    savePendingRef.current = true;
    setPending(true);
    setSaved(false);
    setError(false);
    const controller = new AbortController();
    try {
      await transportApi.update(token, enabled, controller.signal);
      setSetting({ enabled, source: "db" });
      setSaved(true);
    } catch {
      setError(true);
    } finally {
      savePendingRef.current = false;
      setPending(false);
    }
  }, [setting, token, transportApi]);

  const toggle = (enabled: boolean) => {
    if (pending || setting === null) return;
    if (enabled) setConfirming(true);
    else void save(false);
  };

  return (
    <section id={TARGET_ID} aria-labelledby={`${TARGET_ID}-heading`}>
      <Card>
        <CardHeader>
          <CardTitle
            as="h3"
            id={`${TARGET_ID}-heading`}
            ref={headingRef}
            tabIndex={-1}
            className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {t("backupAssets.transport.title")}
          </CardTitle>
          <CardDescription>{t("backupAssets.transport.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {setting === null && !error ? (
            <p role="status" aria-live="polite" className="text-sm text-muted-foreground">
              {t("backupAssets.transport.loading")}
            </p>
          ) : null}

          {setting ? (
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border p-3">
              <div>
                <p className="text-sm font-medium">{t("backupAssets.transport.switchLabel")}</p>
                <p className="text-xs text-muted-foreground">
                  {t("backupAssets.transport.source", {
                    source: t(`backupAssets.transport.sources.${setting.source}`),
                  })}
                </p>
              </div>
              <Switch
                aria-label={t("backupAssets.transport.switchLabel")}
                checked={setting.enabled}
                disabled={pending}
                onCheckedChange={toggle}
              />
            </div>
          ) : null}

          {setting?.enabled ? (
            <InlineAlert tone="warning" title={t("backupAssets.transport.warningTitle")}>
              {t("backupAssets.transport.warningBody")}
            </InlineAlert>
          ) : null}
          {error ? (
            <InlineAlert tone="critical" title={t("backupAssets.transport.errorTitle")}>
              {t("backupAssets.transport.errorBody")}
            </InlineAlert>
          ) : null}
          {saved ? (
            <p role="status" aria-live="polite" className="text-sm text-success">
              {t("backupAssets.transport.saved")}
            </p>
          ) : null}
        </CardContent>
      </Card>

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("backupAssets.transport.confirmTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("backupAssets.transport.confirmBody")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => { setConfirming(false); void save(true); }}>
              {t("backupAssets.transport.enable")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
