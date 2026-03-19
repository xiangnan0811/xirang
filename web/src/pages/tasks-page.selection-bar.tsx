import { useTranslation } from "react-i18next";
import { Play, Terminal, CheckSquare } from "lucide-react";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { TaskRecord } from "@/types/domain";

export interface TasksSelectionBarProps {
  selectedTaskIds: number[];
  setSelectedTaskIds: (ids: number[] | ((prev: number[]) => number[])) => void;
  selectedTaskSet: Set<number>;
  tasks: TaskRecord[];
  authToken: string | null;
  confirm: (opts: { title: string; description: string }) => Promise<boolean>;
  setBatchDefaultNodeIds: (ids: number[] | undefined) => void;
  setBatchDialogOpen: (open: boolean) => void;
  refreshTasks: (options?: { limit?: number; offset?: number }) => Promise<void>;
}

export function TasksSelectionBar({
  selectedTaskIds,
  setSelectedTaskIds,
  selectedTaskSet,
  tasks,
  authToken,
  confirm,
  setBatchDefaultNodeIds,
  setBatchDialogOpen,
  refreshTasks,
}: TasksSelectionBarProps) {
  const { t } = useTranslation();

  if (selectedTaskIds.length === 0) return null;

  return (
    <div className="fixed inset-x-0 bottom-0 z-40 border-t bg-background/95 px-4 py-3 backdrop-blur-sm">
      <div className="mx-auto flex max-w-5xl items-center justify-between">
        <span className="text-sm text-muted-foreground">
          <CheckSquare className="mr-1 inline size-4" />
          {t("tasks.selectedCount", { count: selectedTaskIds.length })}
        </span>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            onClick={async () => {
              const ok = await confirm({
                title: t("tasks.batchTriggerTitle"),
                description: t("tasks.batchTriggerConfirmDesc", { count: selectedTaskIds.length }),
              });
              if (!ok) return;
              try {
                const result = await apiClient.batchTriggerTasks(authToken!, selectedTaskIds);
                setSelectedTaskIds([]);
                toast.success(t("tasks.batchTriggerSuccess", { success: result.successCount, total: result.total }));
                void refreshTasks();
              } catch (err) {
                toast.error(t("tasks.batchTriggerFailed", { error: getErrorMessage(err) }));
              }
            }}
          >
            <Play className="mr-1 size-3.5" />
            {t("tasks.triggerCount", { count: selectedTaskIds.length })}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              const nodeIds = [...new Set(
                tasks
                  .filter((t) => selectedTaskSet.has(t.id))
                  .map((t) => t.nodeId)
              )];
              setBatchDefaultNodeIds(nodeIds);
              setBatchDialogOpen(true);
            }}
          >
            <Terminal className="mr-1 size-3.5" />
            {t("tasks.batchExecuteCount", { count: selectedTaskIds.length })}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setSelectedTaskIds([])}>
            {t("tasks.clearSelection")}
          </Button>
        </div>
      </div>
    </div>
  );
}
