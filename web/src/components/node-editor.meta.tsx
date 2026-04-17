import React from "react";
import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import type { NodeEditorDraft } from "@/components/node-editor-dialog";

type NodeEditorMetaProps = {
  draft: NodeEditorDraft;
  setDraft: React.Dispatch<React.SetStateAction<NodeEditorDraft>>;
};

export function NodeEditorMeta({ draft, setDraft }: NodeEditorMetaProps) {
  const { t } = useTranslation();

  return (
    <>
      <div>
        <label htmlFor="node-edit-tags" className="mb-1 block text-sm font-medium">
          {t("nodeEditor.tags")}
        </label>
        <Input
          id="node-edit-tags"
          placeholder={t("nodeEditor.tagsPlaceholder")}
          value={draft.tags}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, tags: event.target.value }))
          }
        />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="node-edit-maint-start" className="mb-1 block text-sm font-medium">
            {t("nodeEditor.maintenanceStart")}
          </label>
          <Input
            id="node-edit-maint-start"
            type="datetime-local"
            value={draft.maintenanceStart}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, maintenanceStart: event.target.value }))
            }
          />
        </div>
        <div>
          <label htmlFor="node-edit-maint-end" className="mb-1 block text-sm font-medium">
            {t("nodeEditor.maintenanceEnd")}
          </label>
          <Input
            id="node-edit-maint-end"
            type="datetime-local"
            value={draft.maintenanceEnd}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, maintenanceEnd: event.target.value }))
            }
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">{t("nodeEditor.maintenanceHint")}</p>
    </>
  );
}
