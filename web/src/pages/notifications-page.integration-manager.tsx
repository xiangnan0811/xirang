import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Loader2,
  Plus,
  Trash2,
  Wrench,
} from "lucide-react";
import { integrationIcon } from "@/pages/notifications-page.utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { getErrorMessage } from "@/lib/utils";
import type { IntegrationChannel } from "@/types/domain";

type IntegrationManagerProps = {
  integrations: IntegrationChannel[];
  toggleIntegration: (id: string) => Promise<void>;
  testIntegration: (id: string) => Promise<{ ok: boolean; message: string; latencyMs: number }>;
  removeIntegration: (id: string) => Promise<void>;
  onOpenCreate: () => void;
  onOpenEdit: (integration: IntegrationChannel) => void;
};

export function IntegrationManager({
  integrations,
  toggleIntegration,
  testIntegration,
  removeIntegration,
  onOpenCreate,
  onOpenEdit,
}: IntegrationManagerProps) {
  const { t } = useTranslation();
  const { confirm, dialog } = useConfirm();
  const [testingIntegrationMap, setTestingIntegrationMap] = useState<Record<string, number>>({});
  const [updatingIntegrationMap, setUpdatingIntegrationMap] = useState<Record<string, number>>({});

  const beginOp = useCallback((integrationId: string, type: "test" | "update") => {
    const setter = type === "test" ? setTestingIntegrationMap : setUpdatingIntegrationMap;
    setter((prev) => ({ ...prev, [integrationId]: (prev[integrationId] ?? 0) + 1 }));
  }, []);

  const endOp = useCallback((integrationId: string, type: "test" | "update") => {
    const setter = type === "test" ? setTestingIntegrationMap : setUpdatingIntegrationMap;
    setter((prev) => {
      const next = Math.max(0, (prev[integrationId] ?? 0) - 1);
      if (next === 0) {
        return Object.fromEntries(Object.entries(prev).filter(([key]) => key !== integrationId));
      }
      return { ...prev, [integrationId]: next };
    });
  }, []);

  const handleDelete = async (integration: IntegrationChannel) => {
    const ok = await confirm({
      title: t("notifications.deleteIntegration"),
      description: t("notifications.deleteIntegrationDesc", { name: integration.name }),
    });
    if (!ok) return;

    beginOp(integration.id, "update");
    try {
      await removeIntegration(integration.id);
      toast.success(t("notifications.deletedIntegration", { name: integration.name }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      endOp(integration.id, "update");
    }
  };

  return (
    <>
      <Card className="rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2 font-medium">
              {t("notifications.integrationSettingsTitle")}
            </div>
            <Button size="sm" onClick={onOpenCreate}>
              <Plus className="mr-1 size-3.5" />
              {t("notifications.addIntegration")}
            </Button>
          </div>
          {integrations.length ? (
            integrations.map((integration) => {
              const Icon = integrationIcon(integration.type);
              const isUpdating = (updatingIntegrationMap[integration.id] ?? 0) > 0;
              const isTesting = (testingIntegrationMap[integration.id] ?? 0) > 0;
              const busy = isUpdating || isTesting;

              return (
                <div key={integration.id} className="rounded-lg border border-border bg-card shadow-sm overflow-hidden relative group p-3 transition-colors hover:bg-muted/10">
                  <div className="absolute top-0 left-0 w-1 h-full bg-primary opacity-60 group-hover:opacity-100 transition-opacity" />
                  <div className="flex flex-wrap items-center justify-between gap-2 pl-2">
                    <div className="flex items-center gap-3">
                      <span className="flex items-center justify-center rounded-lg p-2.5 bg-primary/10 text-primary">
                        <Icon className="size-5" />
                      </span>
                      <div>
                        <p className="font-medium text-foreground/90">{integration.name}</p>
                        <p className="text-mini font-medium text-muted-foreground uppercase tracking-wider mt-0.5">{integration.type}</p>
                      </div>
                    </div>

                    <div className="flex flex-wrap items-center justify-end gap-2">
                      <Switch
                        checked={integration.enabled}
                        aria-label={t("notifications.toggleEnabled", { action: integration.enabled ? t("common.disable") : t("common.enable"), name: integration.name })}
                        disabled={busy}
                        onCheckedChange={() =>
                          void (async () => {
                            beginOp(integration.id, "update");
                            try {
                              await toggleIntegration(integration.id);
                              toast.success(t("notifications.toggledSuccess", { name: integration.name, action: integration.enabled ? t("common.disabled") : t("common.enabled") }));
                            } catch (error) {
                              toast.error(getErrorMessage(error));
                            } finally {
                              endOp(integration.id, "update");
                            }
                          })()
                        }
                      />
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={busy}
                        onClick={() => {
                          beginOp(integration.id, "test");
                          void testIntegration(integration.id)
                            .then((result) => {
                              if (result.ok) {
                                toast.success(t("notifications.testResultSuccess", { name: integration.name, message: result.message, latency: result.latencyMs }));
                              } else {
                                toast.error(result.message);
                              }
                            })
                            .catch((error) => toast.error(getErrorMessage(error)))
                            .finally(() => endOp(integration.id, "test"));
                        }}
                      >
                        {isTesting && <Loader2 className="mr-1 size-4 animate-spin" />}
                        {t("notifications.testSend")}
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={busy}
                        onClick={() => onOpenEdit(integration)}
                      >
                        <Wrench className="mr-1 size-4" />
                        {t("common.edit")}
                      </Button>
                      <Button
                        variant="destructive"
                        size="icon"
                        aria-label={t("notifications.deleteIntegrationAriaLabel", { name: integration.name })}
                        disabled={busy}
                        onClick={() => void handleDelete(integration)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </div>

                  <div className="mt-3 space-y-1 text-xs text-muted-foreground">
                    <p className="break-all">{t("notifications.endpointLabel", { endpoint: integration.endpoint })}</p>
                    <p>{t("notifications.failThresholdLabel", { count: integration.failThreshold })}</p>
                    <p>{t("notifications.cooldownLabel", { minutes: integration.cooldownMinutes })}</p>
                  </div>
                </div>
              );
            })
          ) : (
            <EmptyState title={t("notifications.noIntegrations")} description={t("notifications.noIntegrationsDesc")} />
          )}
        </CardContent>
      </Card>
      {dialog}
    </>
  );
}
