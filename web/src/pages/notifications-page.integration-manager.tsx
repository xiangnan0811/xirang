import { useCallback, useState } from "react";
import {
  Loader2,
  Plus,
  Trash2,
  Wrench,
} from "lucide-react";
import { integrationIcon } from "@/pages/notifications-page.utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { getErrorMessage } from "@/lib/utils";
import type { IntegrationChannel } from "@/types/domain";

type IntegrationManagerProps = {
  integrations: IntegrationChannel[];
  toggleIntegration: (id: string) => Promise<void>;
  testIntegration: (id: string) => Promise<{ message: string; latencyMs: number }>;
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
      title: "确认删除",
      description: `确认删除通知方式 ${integration.name} 吗？`,
    });
    if (!ok) return;

    beginOp(integration.id, "update");
    try {
      await removeIntegration(integration.id);
      toast.success(`已删除通知方式：${integration.name}`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      endOp(integration.id, "update");
    }
  };

  return (
    <>
      <Card className="border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">通知与集成设置</CardTitle>
            <Button size="sm" onClick={onOpenCreate}>
              <Plus className="mr-1 size-4" />
              新增通知方式
            </Button>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          {integrations.length ? (
            integrations.map((integration) => {
              const Icon = integrationIcon(integration.type);
              const isUpdating = (updatingIntegrationMap[integration.id] ?? 0) > 0;
              const isTesting = (testingIntegrationMap[integration.id] ?? 0) > 0;
              const busy = isUpdating || isTesting;

              return (
                <div key={integration.id} className="interactive-surface p-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <span className="rounded-md border border-primary/20 bg-primary/10 p-1.5 text-primary">
                        <Icon className="size-4" />
                      </span>
                      <div>
                        <p className="font-medium">{integration.name}</p>
                        <p className="text-xs text-muted-foreground uppercase">{integration.type}</p>
                      </div>
                    </div>

                    <div className="flex flex-wrap items-center justify-end gap-2">
                      <Switch
                        checked={integration.enabled}
                        aria-label={`${integration.enabled ? "停用" : "启用"}通知方式 ${integration.name}`}
                        disabled={busy}
                        onCheckedChange={() =>
                          void (async () => {
                            beginOp(integration.id, "update");
                            try {
                              await toggleIntegration(integration.id);
                              toast.success(`通知方式 ${integration.name} 已${integration.enabled ? "停用" : "启用"}。`);
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
                            .then((result) =>
                              toast.success(`${integration.name}：${result.message}（${result.latencyMs}ms）`)
                            )
                            .catch((error) => toast.error(getErrorMessage(error)))
                            .finally(() => endOp(integration.id, "test"));
                        }}
                      >
                        {isTesting && <Loader2 className="mr-1 size-4 animate-spin" />}
                        测试发送
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={busy}
                        onClick={() => onOpenEdit(integration)}
                      >
                        <Wrench className="mr-1 size-4" />
                        编辑
                      </Button>
                      <Button
                        variant="danger"
                        size="icon"
                        aria-label={`删除通知方式 ${integration.name}`}
                        disabled={busy}
                        onClick={() => void handleDelete(integration)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </div>

                  <div className="mt-3 space-y-1 text-xs text-muted-foreground">
                    <p className="break-all">Endpoint：{integration.endpoint}</p>
                    <p>告警阈值：连续失败 {integration.failThreshold} 次</p>
                    <p>冷却时间：{integration.cooldownMinutes} 分钟</p>
                  </div>
                </div>
              );
            })
          ) : (
            <EmptyState title="尚未配置任何通知方式" description="请点击「新增通知方式」手动添加" />
          )}
        </CardContent>
      </Card>
      {dialog}
    </>
  );
}
