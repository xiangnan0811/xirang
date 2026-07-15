import React, { Suspense, useState } from "react";
import { useTranslation } from "react-i18next";
import { RotateCcw, FolderSearch, GitCompareArrows, SearchCode } from "lucide-react";
import { BatchCommandDialog } from "@/components/batch-command-dialog";
import { BatchResultDialog } from "@/components/batch-result-dialog";
import { RestoreConfirmDialog } from "@/components/restore-confirm-dialog";
import { TaskRsyncVersioningDialog } from "@/components/task-rsync-versioning-dialog";
import { SnapshotBrowser } from "@/components/snapshot-browser";
import { SnapshotDiffViewer } from "@/components/snapshot-diff-viewer";
import { SnapshotSearch } from "@/components/snapshot-search";
import { ErrorBoundary } from "@/components/error-boundary";

const TaskEditorDialog = React.lazy(() =>
  import("@/components/task-create-dialog").then(m => ({ default: m.TaskEditorDialog }))
);
import { TaskRunDetail } from "@/components/task-run-detail";
import { TaskRunHistory } from "@/components/task-run-history";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast-sonner";
import type {
  NewTaskInput,
  NodeRecord,
  PolicyRecord,
  TaskRecord,
  TaskRunRecord,
} from "@/types/domain";

export interface TasksPageDialogsProps {
  createDialogOpen: boolean;
  setCreateDialogOpen: (open: boolean) => void;
  editDialogOpen: boolean;
  setEditDialogOpen: (open: boolean) => void;
  editingTask: TaskRecord | null;
  setEditingTask: (task: TaskRecord | null) => void;
  historyTask: TaskRecord | null;
  setHistoryTask: (task: TaskRecord | null) => void;
  selectedRun: TaskRunRecord | null;
  setSelectedRun: (run: TaskRunRecord | null) => void;
  showSnapshots: boolean;
  setShowSnapshots: (show: boolean | ((prev: boolean) => boolean)) => void;
  showDiff: boolean;
  setShowDiff: (show: boolean | ((prev: boolean) => boolean)) => void;
  showSearch: boolean;
  setShowSearch: (show: boolean | ((prev: boolean) => boolean)) => void;
  batchDialogOpen: boolean;
  setBatchDialogOpen: (open: boolean) => void;
  batchDefaultNodeIds: number[] | undefined;
  setBatchDefaultNodeIds: (ids: number[] | undefined) => void;
  batchResultId: string | null;
  setBatchResultId: (id: string | null) => void;
  batchRetain: boolean;
  setBatchRetain: (retain: boolean) => void;
  restoreDialogOpen: boolean;
  setRestoreDialogOpen: (open: boolean) => void;
  rsyncVersioningTask: TaskRecord | null;
  setRsyncVersioningTask: (task: TaskRecord | null) => void;
  canManageRsyncVersioning: boolean;
  onRsyncVersioningUpdated: () => void | Promise<void>;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  authToken: string | null;
  handleCreateTask: (input: NewTaskInput) => Promise<void>;
  handleUpdateTask: (input: NewTaskInput) => Promise<void>;
  onRestoreTriggered?: () => void;
  pauseConfirmTask: TaskRecord | null;
  setPauseConfirmTask: (task: TaskRecord | null) => void;
  onConfirmPause: (taskId: number, cancelRunning: boolean) => Promise<void>;
  onSkipNext: (taskId: number) => Promise<void>;
}

export function TasksPageDialogs({
  createDialogOpen,
  setCreateDialogOpen,
  editDialogOpen,
  setEditDialogOpen,
  editingTask,
  setEditingTask,
  historyTask,
  setHistoryTask,
  selectedRun,
  setSelectedRun,
  showSnapshots,
  setShowSnapshots,
  showDiff,
  setShowDiff,
  showSearch,
  setShowSearch,
  batchDialogOpen,
  setBatchDialogOpen,
  batchDefaultNodeIds,
  setBatchDefaultNodeIds,
  batchResultId,
  setBatchResultId,
  batchRetain,
  setBatchRetain,
  restoreDialogOpen,
  setRestoreDialogOpen,
  rsyncVersioningTask,
  setRsyncVersioningTask,
  canManageRsyncVersioning,
  onRsyncVersioningUpdated,
  onRestoreTriggered,
  nodes,
  policies,
  tasks,
  authToken,
  handleCreateTask,
  handleUpdateTask,
  pauseConfirmTask,
  setPauseConfirmTask,
  onConfirmPause,
  onSkipNext,
}: TasksPageDialogsProps) {
  const { t } = useTranslation();
  const [navigateToSnapshotId, setNavigateToSnapshotId] = useState<string | undefined>(undefined);
  const [navigateToPath, setNavigateToPath] = useState<string | undefined>(undefined);

  const handleNavigateToFile = (snapshotId: string, path: string) => {
    setNavigateToSnapshotId(snapshotId);
    setNavigateToPath(path);
    setShowSearch(false);
    setShowSnapshots(true);
  };

  return (
    <>
      <Suspense fallback={null}>
        <TaskEditorDialog
          open={createDialogOpen}
          onOpenChange={setCreateDialogOpen}
          nodes={nodes}
          policies={policies}
          tasks={tasks}
          onSave={handleCreateTask}
        />
      </Suspense>

      {canManageRsyncVersioning ? (
        <TaskRsyncVersioningDialog
          open={rsyncVersioningTask !== null}
          onOpenChange={(open) => {
            if (!open) {
              setRsyncVersioningTask(null);
            }
          }}
          task={rsyncVersioningTask}
          token={authToken}
          onUpdated={onRsyncVersioningUpdated}
        />
      ) : null}

      <Suspense fallback={null}>
        <TaskEditorDialog
          open={editDialogOpen}
          onOpenChange={(open) => {
            setEditDialogOpen(open);
            if (!open) setEditingTask(null);
          }}
          nodes={nodes}
          policies={policies}
          tasks={tasks}
          onSave={handleUpdateTask}
          editingTask={editingTask}
        />
      </Suspense>

      <Dialog
        open={!!historyTask}
        onOpenChange={(open) => {
          if (!open) {
            setHistoryTask(null);
            setSelectedRun(null);
            setShowSnapshots(false);
            setShowSearch(false);
            setNavigateToSnapshotId(undefined);
            setNavigateToPath(undefined);
          }
        }}
      >
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>
              {t("tasks.executionHistory", { name: historyTask?.name || historyTask?.policyName })}
            </DialogTitle>
            <DialogDescription>
              {t("tasks.executionRecord", { id: historyTask?.id })}
            </DialogDescription>
            <div className="ml-auto mr-8 flex gap-2 shrink-0">
              {historyTask?.executorType === "restic" && (
                <>
                  <Button
                    size="sm"
                    variant={showSnapshots ? "default" : "outline"}
                    onClick={() => { setShowSnapshots((v: boolean) => !v); setShowDiff(false); setShowSearch(false); }}
                  >
                    <FolderSearch className="mr-1 size-3.5" aria-hidden="true" />
                    {t("tasks.browseSnapshots")}
                  </Button>
                  <Button
                    size="sm"
                    variant={showDiff ? "default" : "outline"}
                    onClick={() => { setShowDiff((v: boolean) => !v); setShowSnapshots(false); setShowSearch(false); }}
                  >
                    <GitCompareArrows className="mr-1 size-3.5" aria-hidden="true" />
                    {t("tasks.compareSnapshots")}
                  </Button>
                  <Button
                    size="sm"
                    variant={showSearch ? "default" : "outline"}
                    onClick={() => { setShowSearch((v: boolean) => !v); setShowSnapshots(false); setShowDiff(false); }}
                  >
                    <SearchCode className="mr-1 size-3.5" aria-hidden="true" />
                    {t("tasks.searchSnapshots")}
                  </Button>
                </>
              )}
              {historyTask?.executorType === "rsync" && historyTask.rsyncPublication?.mode === "legacy_mutable" && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setRestoreDialogOpen(true)}
                >
                  <RotateCcw className="mr-1 size-3.5" aria-hidden="true" />
                  {t("tasks.restoreFromBackup")}
                </Button>
              )}
            </div>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody>
            {historyTask && authToken && showSnapshots ? (
              <ErrorBoundary>
                <SnapshotBrowser
                  taskId={historyTask.id}
                  token={authToken}
                  initialSnapshotId={navigateToSnapshotId}
                  initialPath={navigateToPath}
                />
              </ErrorBoundary>
            ) : historyTask && authToken && showDiff ? (
              <ErrorBoundary>
                <SnapshotDiffViewer taskId={historyTask.id} token={authToken} />
              </ErrorBoundary>
            ) : historyTask && authToken && showSearch ? (
              <ErrorBoundary>
                <SnapshotSearch
                  taskId={historyTask.id}
                  token={authToken}
                  onNavigateToFile={handleNavigateToFile}
                />
              </ErrorBoundary>
            ) : historyTask && authToken && (
              selectedRun ? (
                <ErrorBoundary>
                  <TaskRunDetail
                    run={selectedRun}
                    token={authToken}
                    onBack={() => setSelectedRun(null)}
                  />
                </ErrorBoundary>
              ) : (
                <ErrorBoundary>
                  <TaskRunHistory
                    taskId={historyTask.id}
                    token={authToken}
                    onSelectRun={setSelectedRun}
                  />
                </ErrorBoundary>
              )
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      {authToken && (
        <ErrorBoundary>
          <BatchCommandDialog
            open={batchDialogOpen}
            onOpenChange={(open) => {
              setBatchDialogOpen(open);
              if (!open) setBatchDefaultNodeIds(undefined);
            }}
            nodes={nodes}
            token={authToken}
            defaultNodeIds={batchDefaultNodeIds}
            onSuccess={(result) => {
              setBatchResultId(result.batchId);
              setBatchRetain(result.retain);
            }}
          />
          <BatchResultDialog
            open={batchResultId !== null}
            onOpenChange={(open) => { if (!open) setBatchResultId(null); }}
            batchId={batchResultId}
            retain={batchRetain}
            token={authToken}
          />
        </ErrorBoundary>
      )}

      <Dialog
        open={!!pauseConfirmTask}
        onOpenChange={(open) => { if (!open) setPauseConfirmTask(null); }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t("tasks.pauseCronTitle")}</DialogTitle>
            <DialogDescription>
              {t("tasks.pauseCronDesc", { name: pauseConfirmTask?.name || pauseConfirmTask?.policyName })}
            </DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody>
            {pauseConfirmTask && (
              <CronPauseOptions
                task={pauseConfirmTask}
                onConfirmPause={async (cancelRunning) => {
                  setPauseConfirmTask(null);
                  await onConfirmPause(pauseConfirmTask.id, cancelRunning);
                }}
                onSkipNext={async () => {
                  setPauseConfirmTask(null);
                  await onSkipNext(pauseConfirmTask.id);
                }}
                onCancel={() => setPauseConfirmTask(null)}
              />
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      {authToken && historyTask && (
        <ErrorBoundary>
          <RestoreConfirmDialog
            open={restoreDialogOpen}
            onOpenChange={setRestoreDialogOpen}
            taskId={historyTask.id}
            taskName={historyTask.name ?? historyTask.policyName ?? ""}
            rsyncSource={historyTask.rsyncSource}
            rsyncTarget={historyTask.rsyncTarget}
            token={authToken}
            onSuccess={(runId) => {
              setRestoreDialogOpen(false);
              toast.success(t("tasks.restoreSuccess", { runId }));
              onRestoreTriggered?.();
            }}
          />
        </ErrorBoundary>
      )}
    </>
  );
}

function CronPauseOptions({
  task,
  onConfirmPause,
  onSkipNext,
  onCancel,
}: {
  task: TaskRecord;
  onConfirmPause: (cancelRunning: boolean) => void;
  onSkipNext: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const isRunning = task.status === "running" || task.status === "retrying";
  const optionSkipNextId = `task-${task.id}-pause-skip-next`;
  const optionPauseAllId = `task-${task.id}-pause-all`;
  const optionPauseCancelId = `task-${task.id}-pause-cancel`;
  const [selected, setSelected] = useState<"skip-next" | "pause-all" | "pause-cancel">(
    task.skipNext ? "pause-all" : "skip-next"
  );

  const handleConfirm = () => {
    if (selected === "skip-next") onSkipNext();
    else if (selected === "pause-all") onConfirmPause(false);
    else onConfirmPause(true);
  };

  return (
    <div className="space-y-4">
      <div className="space-y-3" role="radiogroup" aria-label={t("tasks.pauseCronTitle")}>
        {!task.skipNext && (
          <label htmlFor={optionSkipNextId} className="flex items-start gap-2.5 cursor-pointer rounded-md border border-border/60 p-3 transition-colors hover:bg-muted/40 has-[:checked]:border-primary/50 has-[:checked]:bg-primary/5">
            <input id={optionSkipNextId} type="radio" name="cron-pause" className="mt-0.5" checked={selected === "skip-next"} onChange={() => setSelected("skip-next")} />
            <span className="sr-only">{t("tasks.pauseOptionSkipNext")}</span>
            <div>
              <span aria-hidden="true" className="text-sm font-medium">{t("tasks.pauseOptionSkipNext")}</span>
              <p className="text-xs text-muted-foreground mt-0.5">{t("tasks.pauseOptionSkipNextDesc")}</p>
            </div>
          </label>
        )}
        <label htmlFor={optionPauseAllId} className="flex items-start gap-2.5 cursor-pointer rounded-md border border-border/60 p-3 transition-colors hover:bg-muted/40 has-[:checked]:border-primary/50 has-[:checked]:bg-primary/5">
          <input id={optionPauseAllId} type="radio" name="cron-pause" className="mt-0.5" checked={selected === "pause-all"} onChange={() => setSelected("pause-all")} />
          <span className="sr-only">{t("tasks.pauseOptionPauseAll")}</span>
          <div>
            <span aria-hidden="true" className="text-sm font-medium">{t("tasks.pauseOptionPauseAll")}</span>
            <p className="text-xs text-muted-foreground mt-0.5">{t("tasks.pauseOptionPauseAllDesc")}</p>
          </div>
        </label>
        {isRunning && (
          <label htmlFor={optionPauseCancelId} className="flex items-start gap-2.5 cursor-pointer rounded-md border border-border/60 p-3 transition-colors hover:bg-muted/40 has-[:checked]:border-primary/50 has-[:checked]:bg-primary/5">
            <input id={optionPauseCancelId} type="radio" name="cron-pause" className="mt-0.5" checked={selected === "pause-cancel"} onChange={() => setSelected("pause-cancel")} />
            <span className="sr-only">{t("tasks.pauseOptionPauseCancel")}</span>
            <div>
              <span aria-hidden="true" className="text-sm font-medium">{t("tasks.pauseOptionPauseCancel")}</span>
              <p className="text-xs text-muted-foreground mt-0.5">{t("tasks.pauseOptionPauseCancelDesc")}</p>
            </div>
          </label>
        )}
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="outline" size="sm" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <Button size="sm" onClick={handleConfirm}>
          {t("common.confirm")}
        </Button>
      </div>
    </div>
  );
}
