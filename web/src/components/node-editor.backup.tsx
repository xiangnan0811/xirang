import React from "react";
import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import type { NodeEditorDraft } from "@/components/node-editor-dialog";

type NodeEditorBackupProps = {
  draft: NodeEditorDraft;
  isEditing: boolean;
  backupDirManuallyEdited: boolean;
  setBackupDirManuallyEdited: (value: boolean) => void;
  setDraft: React.Dispatch<React.SetStateAction<NodeEditorDraft>>;
};

export function NodeEditorBackup({
  draft,
  isEditing,
  backupDirManuallyEdited,
  setBackupDirManuallyEdited,
  setDraft,
}: NodeEditorBackupProps) {
  const { t } = useTranslation();

  return (
    <>
      <div>
        <label htmlFor="node-edit-base-path" className="mb-1 block text-sm font-medium">
          {t("nodeEditor.basePath")}
        </label>
        <Input
          id="node-edit-base-path"
          placeholder="/"
          value={draft.basePath}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, basePath: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="node-edit-backup-dir" className="mb-1 block text-sm font-medium">
          {t("nodeEditor.backupDir")}
        </label>
        <Input
          id="node-edit-backup-dir"
          placeholder={t("nodeEditor.backupDirPlaceholder")}
          value={draft.backupDir}
          onChange={(event) => {
            setBackupDirManuallyEdited(true);
            setDraft((prev) => ({ ...prev, backupDir: event.target.value }));
          }}
        />
        <p className="mt-1 text-xs text-muted-foreground">
          {t("nodeEditor.backupDirHint")}
        </p>
        {draft.backupDir && (
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("nodeEditor.backupDirPreview", { dir: draft.backupDir })}
          </p>
        )}
        {isEditing && draft.backupDir && (
          <p className="mt-0.5 text-xs text-yellow-600 dark:text-yellow-400">
            {t("nodeEditor.backupDirChangeWarning")}
          </p>
        )}
        {/* eslint-disable-next-line no-control-regex -- intentional: detect non-ASCII chars in node name */}
        {!backupDirManuallyEdited && /[^\x00-\x7F]/.test(draft.name) && (
          <p className="mt-0.5 text-xs text-yellow-600 dark:text-yellow-400">
            {t("nodeEditor.backupDirNonAsciiWarning")}
          </p>
        )}
      </div>
    </>
  );
}
