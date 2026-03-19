import { useTranslation } from "react-i18next";
import { RotateCcw, FolderSearch, GitCompareArrows } from "lucide-react";
import { BatchCommandDialog } from "@/components/batch-command-dialog";
import { BatchResultDialog } from "@/components/batch-result-dialog";
import { RestoreConfirmDialog } from "@/components/restore-confirm-dialog";
import { SnapshotBrowser } from "@/components/snapshot-browser";
import { SnapshotDiffViewer } from "@/components/snapshot-diff-viewer";
import { TaskEditorDialog } from "@/components/task-create-dialog";
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
import { toast } from "@/components/ui/toast";
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
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  authToken: string | null;
  handleCreateTask: (input: NewTaskInput) => Promise<void>;
  handleUpdateTask: (input: NewTaskInput) => Promise<void>;
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
  nodes,
  policies,
  tasks,
  authToken,
  handleCreateTask,
  handleUpdateTask,
}: TasksPageDialogsProps) {
  const { t } = useTranslation();

  return (
    <>
      <TaskEditorDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        nodes={nodes}
        policies={policies}
        tasks={tasks}
        onSave={handleCreateTask}
      />

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

      <Dialog
        open={!!historyTask}
        onOpenChange={(open) => {
          if (!open) {
            setHistoryTask(null);
            setSelectedRun(null);
            setShowSnapshots(false);
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
                    onClick={() => { setShowSnapshots((v: boolean) => !v); setShowDiff(false); }}
                  >
                    <FolderSearch className="mr-1 size-3.5" />
                    {t("tasks.browseSnapshots")}
                  </Button>
                  <Button
                    size="sm"
                    variant={showDiff ? "default" : "outline"}
                    onClick={() => { setShowDiff((v: boolean) => !v); setShowSnapshots(false); }}
                  >
                    <GitCompareArrows className="mr-1 size-3.5" />
                    {t("tasks.compareSnapshots")}
                  </Button>
                </>
              )}
              {historyTask?.executorType === "rsync" && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setRestoreDialogOpen(true)}
                >
                  <RotateCcw className="mr-1 size-3.5" />
                  {t("tasks.restoreFromBackup")}
                </Button>
              )}
            </div>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody>
            {historyTask && authToken && showSnapshots ? (
              <SnapshotBrowser taskId={historyTask.id} token={authToken} />
            ) : historyTask && authToken && showDiff ? (
              <SnapshotDiffViewer taskId={historyTask.id} token={authToken} />
            ) : historyTask && authToken && (
              selectedRun ? (
                <TaskRunDetail
                  run={selectedRun}
                  token={authToken}
                  onBack={() => setSelectedRun(null)}
                />
              ) : (
                <TaskRunHistory
                  taskId={historyTask.id}
                  token={authToken}
                  onSelectRun={setSelectedRun}
                />
              )
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      {authToken && (
        <>
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
        </>
      )}

      {authToken && historyTask && (
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
          }}
        />
      )}
    </>
  );
}
