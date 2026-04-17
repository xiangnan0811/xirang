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
            <Button size="sm" shape="pill" onClick={onOpenCreate}>
              <Plus className="mr-1 size-3.5" />
              {t("notifications.addIntegration")}
            </Button>
          </div>

          {integrations.length ? (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
              {integrations.map((integration) => {
                const Icon = integrationIcon(integration.type);
                const isUpdating = (updatingIntegrationMap[integration.id] ?? 0) > 0;
                const isTesting = (testingIntegrationMap[integration.id] ?? 0) > 0;
                const busy = isUpdating || isTesting;

                return (
                  <div
                    key={integration.id}
                    className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 shadow-sm transition-colors hover:bg-muted/10"
                  >
                    {/* Card header: icon + name + switch */}
                    <div className="flex items-start justify-between gap-3">
                      <div className="flex items-center gap-3 min-w-0">
                        <span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-accent text-accent-foreground">
                          <Icon className="size-5" />
                        </span>
                        <div className="min-w-0">
                          <p className="truncate font-semibold text-foreground/90">{integration.name}</p>
                          <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground mt-0.5">
                            {integration.type}
                          </p>
                        </div>
                      </div>
                      <Switch
                        checked={integration.enabled}
                        aria-label={t("notifications.toggleEnabled", {
                          action: integration.enabled ? t("common.disable") : t("common.enable"),
                          name: integration.name,
                        })}
                        disabled={busy}
                        onCheckedChange={() =>
                          void (async () => {
                            beginOp(integration.id, "update");
                            try {
                              await toggleIntegration(integration.id);
                              toast.success(
                                t("notifications.toggledSuccess", {
                                  name: integration.name,
                                  action: integration.enabled ? t("common.disabled") : t("common.enabled"),
                                })
                              );
                            } catch (error) {
                              toast.error(getErrorMessage(error));
                            } finally {
                              endOp(integration.id, "update");
                            }
                          })()
                        }
                      />
                    </div>

                    {/* Last delivered muted line */}
                    <div className="space-y-0.5 text-xs text-muted-foreground">
                      <p className="break-all">{t("notifications.endpointLabel", { endpoint: integration.endpoint })}</p>
                      <p>{t("notifications.failThresholdLabel", { count: integration.failThreshold })}</p>
                      <p>{t("notifications.cooldownLabel", { minutes: integration.cooldownMinutes })}</p>
                    </div>

                    {/* Actions row */}
                    <div className="flex items-center gap-2 pt-1">
                      <Button
                        variant="outline"
                        size="sm"
                        className="flex-1"
                        disabled={busy}
                        onClick={() => {
                          beginOp(integration.id, "test");
                          void testIntegration(integration.id)
                            .then((result) => {
                              if (result.ok) {
                                toast.success(
                                  t("notifications.testResultSuccess", {
                                    name: integration.name,
                                    message: result.message,
                                    latency: result.latencyMs,
                                  })
                                );
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
                        aria-label={t("common.edit")}
                      >
                        <Wrench className="size-4" />
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
                );
              })}
            </div>
          ) : (
            <EmptyState title={t("notifications.noIntegrations")} description={t("notifications.noIntegrationsDesc")} />
          )}
        </CardContent>
      </Card>
      {dialog}
    </>
  );
}
