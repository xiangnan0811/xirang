import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { alertStatusMeta } from "@/pages/notifications-page.utils";
import type { AlertRecord } from "@/types/domain";

type SelectedAlertPanelProps = {
  selectedAlert: AlertRecord | null;
  deliveryOpenAlertId: string | null;
  deliveryLoadingAlertId: string | null;
  deliveryCount: number;
  onRetry: () => void;
  onAcknowledge: () => void;
  onResolve: () => void;
  onToggleDeliveries: () => void;
};

export function SelectedAlertPanel({
  selectedAlert,
  deliveryOpenAlertId,
  deliveryLoadingAlertId,
  deliveryCount,
  onRetry,
  onAcknowledge,
  onResolve,
  onToggleDeliveries,
}: SelectedAlertPanelProps) {
  return (
    <aside className="hidden lg:block">
      {selectedAlert ? (
        <div className="interactive-surface sticky top-32 space-y-3 p-4">
          <div className="flex items-center justify-between gap-2">
            <div>
              <p className="text-xs text-muted-foreground">当前选中告警</p>
              <h4 className="text-lg font-semibold">{selectedAlert.nodeName}</h4>
              <p className="text-xs text-muted-foreground">
                {selectedAlert.taskId ? `任务 #${selectedAlert.taskId}` : "节点探测"}
              </p>
            </div>
            <Badge variant={alertStatusMeta(selectedAlert.status).variant}>
              {alertStatusMeta(selectedAlert.status).label}
            </Badge>
          </div>

          <p className="text-sm">{selectedAlert.message}</p>

          <div className="space-y-1 text-xs text-muted-foreground">
            <p>策略：{selectedAlert.policyName}</p>
            <p>错误码：{selectedAlert.errorCode}</p>
            <p>触发时间：{selectedAlert.triggeredAt}</p>
          </div>

          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={onRetry} disabled={!selectedAlert.retryable}>
              一键重试
            </Button>

            <Button size="sm" variant="outline" onClick={onAcknowledge} disabled={selectedAlert.status !== "open"}>
              确认
            </Button>

            <Button size="sm" variant="outline" onClick={onResolve} disabled={selectedAlert.status === "resolved"}>
              标记恢复
            </Button>

            <Button size="sm" variant="outline" onClick={onToggleDeliveries}>
              {deliveryOpenAlertId === selectedAlert.id ? "收起投递" : "投递记录"}
            </Button>
          </div>

          {deliveryOpenAlertId === selectedAlert.id ? (
            <div className="rounded-md border border-border/70 bg-muted/25 p-2">
              {deliveryLoadingAlertId === selectedAlert.id ? (
                <p className="text-xs text-muted-foreground">投递记录加载中...</p>
              ) : (
                <p className="text-xs text-muted-foreground">投递记录 {deliveryCount} 条</p>
              )}
            </div>
          ) : null}
        </div>
      ) : (
        <EmptyState className="py-10" title="暂无告警详情" description="请先选择一条告警。" />
      )}
    </aside>
  );
}

