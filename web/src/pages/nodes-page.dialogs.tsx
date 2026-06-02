import React, { lazy, Suspense } from "react";
import { useTranslation } from "react-i18next";
import { BatchCommandDialog } from "@/components/batch-command-dialog";
import { BatchResultDialog } from "@/components/batch-result-dialog";
import { ErrorBoundary } from "@/components/error-boundary";

const NodeEditorDialog = React.lazy(() =>
  import("@/components/node-editor-dialog").then(m => ({ default: m.NodeEditorDialog }))
);
const NodeMigrateWizard = React.lazy(() =>
  import("@/components/node-migrate-wizard").then(m => ({ default: m.NodeMigrateWizard }))
);
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { DockerVolumesPanel } from "@/components/docker-volumes-panel";
import { FileBrowser } from "@/components/file-browser";
import { NodeDoctorDialog } from "@/components/node-doctor-dialog";
import { createFilesApi } from "@/lib/api/files-api";
import type { NodesPageState } from "@/pages/nodes-page.state";

const filesApi = createFilesApi();

const WebTerminal = lazy(() => import("@/components/web-terminal"));

export type NodesPageDialogsProps = Pick<
  NodesPageState,
  | "token"
  | "nodes"
  | "sshKeys"
  | "editorOpen"
  | "handleEditorOpenChange"
  | "editingNode"
  | "terminalNode"
  | "setTerminalNode"
  | "terminalKey"
  | "doctorNode"
  | "doctorResult"
  | "doctorLoading"
  | "doctorError"
  | "handleDoctorOpenChange"
  | "runDoctorForNode"
  | "fileBrowserNode"
  | "setFileBrowserNode"
  | "fileBrowserTab"
  | "setFileBrowserTab"
  | "batchCmdOpen"
  | "setBatchCmdOpen"
  | "batchResultId"
  | "setBatchResultId"
  | "batchRetain"
  | "setBatchRetain"
  | "migrateSourceNode"
  | "setMigrateSourceNode"
  | "selectedNodeIds"
  | "dialog"
  | "refreshNodes"
  | "handleSaveNode"
  | "handleTestConnection"
>;

export function NodesPageDialogs({
  token,
  nodes,
  sshKeys,
  editorOpen,
  handleEditorOpenChange,
  editingNode,
  terminalNode,
  setTerminalNode,
  terminalKey,
  doctorNode,
  doctorResult,
  doctorLoading,
  doctorError,
  handleDoctorOpenChange,
  runDoctorForNode,
  fileBrowserNode,
  setFileBrowserNode,
  fileBrowserTab,
  setFileBrowserTab,
  batchCmdOpen,
  setBatchCmdOpen,
  batchResultId,
  setBatchResultId,
  batchRetain,
  setBatchRetain,
  migrateSourceNode,
  setMigrateSourceNode,
  selectedNodeIds,
  dialog,
  refreshNodes,
  handleSaveNode,
  handleTestConnection,
}: NodesPageDialogsProps) {
  const { t } = useTranslation();

  return (
    <>
      <Suspense fallback={null}>
        <NodeEditorDialog
          open={editorOpen}
          onOpenChange={handleEditorOpenChange}
          editingNode={editingNode}
          sshKeys={sshKeys}
          onSave={handleSaveNode}
          onTestConnection={handleTestConnection}
        />
      </Suspense>

      <Dialog
        open={terminalNode !== null}
        onOpenChange={(open) => { if (!open) setTerminalNode(null); }}
      >
        <DialogContent
          className="w-full max-w-[95vw] md:max-w-[90vw] h-[85vh] flex flex-col gap-0 p-0 resize overflow-hidden"
          onPointerDownOutside={(e) => e.preventDefault()}
          onInteractOutside={(e) => e.preventDefault()}
        >
          <DialogHeader className="px-4 pt-4 pb-2 shrink-0">
            <DialogTitle className="flex items-center justify-between">
              <span>{t("nodes.terminalTitle", { name: terminalNode?.name ?? "" })}</span>
              <DialogCloseButton />
            </DialogTitle>
            <DialogDescription className="sr-only">
              {t("nodes.terminalDesc")}
            </DialogDescription>
          </DialogHeader>
          <div className="flex-1 overflow-hidden px-4 pb-4">
            {terminalNode !== null && token !== null && (
              <Suspense fallback={<div className="flex h-full items-center justify-center text-sm text-muted-foreground">{t("nodes.terminalLoading")}</div>}>
                <ErrorBoundary>
                  <WebTerminal
                    key={terminalKey}
                    nodeId={terminalNode.id}
                    token={token}
                    onDisconnect={() => setTerminalNode(null)}
                  />
                </ErrorBoundary>
              </Suspense>
            )}
          </div>
        </DialogContent>
      </Dialog>

      <NodeDoctorDialog
        open={doctorNode !== null}
        node={doctorNode}
        result={doctorResult}
        loading={doctorLoading}
        error={doctorError}
        onOpenChange={handleDoctorOpenChange}
        onRun={runDoctorForNode}
      />

      {token && fileBrowserNode && (
        <Dialog
          open={fileBrowserNode !== null}
          onOpenChange={(open) => { if (!open) { setFileBrowserNode(null); setFileBrowserTab("files"); } }}
        >
          <DialogContent className="flex w-full max-w-[95vw] flex-col md:max-w-[80vw]" size="lg">
            <DialogHeader>
              <DialogTitle>{t("nodes.fileBrowserTitle", { name: fileBrowserNode.name })}</DialogTitle>
              <DialogDescription className="sr-only">
                {t("nodes.fileBrowserDesc", { name: fileBrowserNode.name })}
              </DialogDescription>
              <DialogCloseButton />
            </DialogHeader>
            <div className="flex gap-2 px-6">
              <Button
                variant={fileBrowserTab === "files" ? "default" : "outline"}
                size="sm"
                aria-pressed={fileBrowserTab === "files"}
                onClick={() => setFileBrowserTab("files")}
              >
                {t("nodes.tabFiles")}
              </Button>
              <Button
                variant={fileBrowserTab === "docker" ? "default" : "outline"}
                size="sm"
                aria-pressed={fileBrowserTab === "docker"}
                onClick={() => setFileBrowserTab("docker")}
              >
                {t("nodes.tabDockerVolumes")}
              </Button>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-6 pb-6 thin-scrollbar">
              <ErrorBoundary>
                {fileBrowserTab === "files" ? (
                  <FileBrowser
                    rootPath={
                      fileBrowserNode.basePath && fileBrowserNode.basePath !== "/"
                        ? fileBrowserNode.basePath
                        : fileBrowserNode.username === "root"
                          ? "/root"
                          : `/home/${fileBrowserNode.username}`
                    }
                    fetchDir={(path, signal) =>
                      filesApi.listNodeFiles(token, fileBrowserNode.id, path, { signal })
                    }
                    fetchContent={(path, signal) =>
                      filesApi.getNodeFileContent(token, fileBrowserNode.id, path, { signal })
                    }
                  />
                ) : (
                  <DockerVolumesPanel
                    nodeId={fileBrowserNode.id}
                    token={token}
                  />
                )}
              </ErrorBoundary>
            </div>
          </DialogContent>
        </Dialog>
      )}

      {token && (
        <ErrorBoundary>
          <>
            <BatchCommandDialog
              open={batchCmdOpen}
              onOpenChange={setBatchCmdOpen}
              nodes={nodes}
              token={token}
              defaultNodeIds={selectedNodeIds}
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
              token={token}
            />
          </>
        </ErrorBoundary>
      )}

      {/* 迁移节点向导 */}
      {token && migrateSourceNode !== null && (
        <Suspense fallback={null}>
          <ErrorBoundary>
            <NodeMigrateWizard
              open
              onOpenChange={(open) => { if (!open) setMigrateSourceNode(null); }}
            sourceNode={migrateSourceNode}
            nodes={nodes}
            token={token}
            onSuccess={() => { setMigrateSourceNode(null); void refreshNodes(); }}
          />
          </ErrorBoundary>
        </Suspense>
      )}

      {dialog}
    </>
  );
}
