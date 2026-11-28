import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Select } from "@/components/ui/select";
import type { TaskDraft } from "@/components/task-create-dialog";

type TaskAdvancedProps = {
  draft: TaskDraft;
  setDraft: React.Dispatch<React.SetStateAction<TaskDraft>>;
  isEditing: boolean;
  /** Target persisted on the task being edited; the draft value can be changed while editing Rclone. */
  savedTarget?: string;
  managedRclone: boolean;
};

export function TaskAdvanced({ draft, setDraft, isEditing, savedTarget, managedRclone }: TaskAdvancedProps) {
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
          <div className="glass-panel rounded-md px-3 py-2 text-sm">
            <span className="font-medium text-muted-foreground">
              {t("taskCreate.autoTargetPath")}:
            </span>{" "}
            {!isEditing ? (
              <span className="text-xs text-muted-foreground">{t("taskCreate.autoTargetAssignedOnCreate")}</span>
            ) : savedTarget?.trim() ? (
              <span className="whitespace-pre-wrap break-all font-mono text-xs">{savedTarget}</span>
            ) : (
              <span className="text-xs text-muted-foreground">{t("taskCreate.autoTargetNotRecorded")}</span>
            )}
          </div>
        </>
      )}

      {draft.executorType === "rclone" && (
        <>
          <div className={managedRclone ? undefined : "grid gap-3 md:grid-cols-2"}>
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
            {!managedRclone ? (
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
            ) : null}
          </div>
          {managedRclone ? (
            <InlineAlert tone="info">{t("rcloneVersioning.managedEditorNotice")}</InlineAlert>
          ) : (
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
          )}
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
              autoComplete="new-password"
              placeholder={draft.resticPasswordConfigured
                ? t("taskCreate.resticPasswordKeepPlaceholder")
                : t("taskCreate.resticPassword")}
              value={draft.resticPassword}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, resticPassword: event.target.value }))
              }
            />
            <p className="mt-1 text-xs text-muted-foreground">
              {isEditing && draft.resticPasswordConfigured
                ? t("taskCreate.resticPasswordConfigured")
                : t("taskCreate.resticPasswordUnset")}
            </p>
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
          <div>
            <label htmlFor="task-editor-restic-repository-version" className="mb-1 block text-sm font-medium">
              {t("taskCreate.repositoryFormat")}
            </label>
            <Select
              id="task-editor-restic-repository-version"
              containerClassName="w-full"
              value={draft.resticRepositoryVersion}
              onChange={(event) => {
                const value = event.target.value;
                if (value !== "" && value !== "1" && value !== "2") {
                  return;
                }
                setDraft((prev) => ({ ...prev, resticRepositoryVersion: value }));
              }}
            >
              <option value="">{t("taskCreate.repositoryFormatDefault")}</option>
              <option value="1">{t("taskCreate.repositoryFormatV1")}</option>
              <option value="2">{t("taskCreate.repositoryFormatV2")}</option>
            </Select>
            <p className="mt-1 text-xs text-muted-foreground">{t("taskCreate.repositoryFormatHint")}</p>
          </div>
          <InlineAlert tone="warning" title={t("taskCreate.deletionProtectionUnverifiedTitle")}>
            {t("taskCreate.deletionProtectionUnverified")}
          </InlineAlert>
        </>
      )}
    </>
  );
}
