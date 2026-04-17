import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import type { NodeRecord } from "@/types/domain";
import type { TaskDraft } from "@/components/task-create-dialog";

type TaskAdvancedProps = {
  draft: TaskDraft;
  setDraft: React.Dispatch<React.SetStateAction<TaskDraft>>;
  nodes: NodeRecord[];
  isEditing: boolean;
};

export function TaskAdvanced({ draft, setDraft, nodes, isEditing }: TaskAdvancedProps) {
  const { t } = useTranslation();

  return (
    <>
      {draft.executorType === "command" && (
        <div>
          <label htmlFor="task-editor-command" className="mb-1 block text-sm font-medium">
            {t("taskCreate.shellCommand")}
          </label>
          <Input
            id="task-editor-command"
            placeholder={t("taskCreate.commandPlaceholder")}
            value={draft.command}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, command: event.target.value }))
            }
          />
          <p className="mt-1 text-xs text-muted-foreground">
            {t("taskCreate.shellCommandHint")}
          </p>
        </div>
      )}

      {(draft.executorType === "rsync" || draft.executorType === "restic") && (
        <>
          <div>
            <label htmlFor="task-editor-rsync-source" className="mb-1 block text-sm font-medium">
              {draft.executorType === "rsync"
                ? t("taskCreate.rsyncSourcePath")
                : t("taskCreate.sourcePath")}
            </label>
            <Input
              id="task-editor-rsync-source"
              placeholder="/data/source"
              value={draft.rsyncSource}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, rsyncSource: event.target.value }))
              }
            />
          </div>
          {isEditing && draft.rsyncTarget ? (
            <div className="glass-panel rounded-md px-3 py-2 text-sm">
              <span className="font-medium text-muted-foreground">
                {t("taskCreate.autoTargetPath")}:
              </span>{" "}
              <span className="font-mono text-xs">{draft.rsyncTarget}</span>
            </div>
          ) : (
            <div className="glass-panel rounded-md px-3 py-2 text-sm text-muted-foreground">
              <span className="font-medium">{t("taskCreate.autoTargetPath")}:</span>{" "}
              {(() => {
                const selectedNode = nodes.find((n) => String(n.id) === draft.nodeId);
                if (selectedNode?.backupDir) {
                  return (
                    <span className="font-mono text-xs">
                      /backup/{selectedNode.backupDir}/
                    </span>
                  );
                }
                return <span className="text-xs">{t("taskCreate.selectNodeFirst")}</span>;
              })()}
            </div>
          )}
          <p className="text-xs text-muted-foreground">{t("taskCreate.autoTargetHint")}</p>
        </>
      )}

      {draft.executorType === "rclone" && (
        <>
          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <label htmlFor="task-editor-rclone-source" className="mb-1 block text-sm font-medium">
                {t("taskCreate.sourcePath")}
              </label>
              <Input
                id="task-editor-rclone-source"
                placeholder="/data/source"
                value={draft.rsyncSource}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, rsyncSource: event.target.value }))
                }
              />
            </div>
            <div>
              <label htmlFor="task-editor-rsync-target" className="mb-1 block text-sm font-medium">
                {t("taskCreate.rcloneRemotePath")}
              </label>
              <Input
                id="task-editor-rsync-target"
                placeholder="s3:my-bucket/backups"
                value={draft.rsyncTarget}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, rsyncTarget: event.target.value }))
                }
              />
            </div>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <label htmlFor="task-editor-rclone-bwlimit" className="mb-1 block text-sm font-medium">
                {t("taskCreate.rcloneBandwidthLimit")}
              </label>
              <Input
                id="task-editor-rclone-bwlimit"
                placeholder={t("taskCreate.bwLimitPlaceholder")}
                value={draft.rcloneBandwidthLimit}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, rcloneBandwidthLimit: event.target.value }))
                }
              />
            </div>
            <div>
              <label htmlFor="task-editor-rclone-transfers" className="mb-1 block text-sm font-medium">
                {t("taskCreate.rcloneConcurrentTransfers")}
              </label>
              <Input
                id="task-editor-rclone-transfers"
                type="number"
                min={1}
                max={32}
                placeholder={t("taskCreate.concurrencyPlaceholder")}
                value={draft.rcloneTransfers}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, rcloneTransfers: event.target.value }))
                }
              />
            </div>
          </div>
        </>
      )}

      {draft.executorType === "restic" && (
        <>
          <div>
            <label htmlFor="task-editor-restic-password" className="mb-1 block text-sm font-medium">
              {t("taskCreate.resticRepoPassword")}
            </label>
            <Input
              id="task-editor-restic-password"
              type="password"
              placeholder={t("taskCreate.resticPassword")}
              value={draft.resticPassword}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, resticPassword: event.target.value }))
              }
            />
          </div>
          <div>
            <label htmlFor="task-editor-restic-excludes" className="mb-1 block text-sm font-medium">
              {t("taskCreate.resticExcludeRules")}
            </label>
            <textarea
              id="task-editor-restic-excludes"
              className="glass-panel w-full min-h-[72px] resize-none rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
              placeholder={"*.log\n/tmp\n/proc"}
              value={draft.resticExcludePatterns}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, resticExcludePatterns: event.target.value }))
              }
            />
          </div>
        </>
      )}
    </>
  );
}
