import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { IntegrationCreateDialog } from "@/components/integration-create-dialog";
import { IntegrationEditorDialog, type IntegrationEditorDraft } from "@/components/integration-editor-dialog";
import { IntegrationManager } from "@/pages/notifications-page.integration-manager";
import { toast } from "@/components/ui/toast";
import type { IntegrationChannel } from "@/types/domain";

export function ChannelsTab() {
  const { t } = useTranslation();
  const {
    integrations,
    addIntegration,
    removeIntegration,
    toggleIntegration,
    updateIntegration,
    testIntegration,
    refreshIntegrations,
  } = useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshIntegrations();
  }, [refreshIntegrations]);

  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [editingIntegration, setEditingIntegration] = useState<IntegrationChannel | null>(null);

  const openEditDialog = (integration: IntegrationChannel) => {
    setEditingIntegration(integration);
    setEditDialogOpen(true);
  };

  const handleRemoveIntegration = useCallback(async (id: string) => {
    await removeIntegration(id);
    setEditingIntegration((prev) => {
      if (prev?.id === id) {
        setEditDialogOpen(false);
        return null;
      }
      return prev;
    });
  }, [removeIntegration]);

  const handleEditIntegration = async (draft: IntegrationEditorDraft) => {
    await updateIntegration(draft.id, {
      name: draft.name,
      endpoint: draft.endpoint,
      failThreshold: draft.failThreshold,
      cooldownMinutes: draft.cooldownMinutes,
      secret: draft.secret || undefined,
      skipEndpointHint: draft.skipEndpointHint,
    });
    toast.success(t("notifications.integrationSaved", { name: draft.name }));
    setEditDialogOpen(false);
    setEditingIntegration(null);
  };

  return (
    <div className="max-w-4xl">
      <IntegrationManager
        integrations={integrations}
        toggleIntegration={toggleIntegration}
        testIntegration={testIntegration}
        removeIntegration={handleRemoveIntegration}
        onOpenCreate={() => setCreateDialogOpen(true)}
        onOpenEdit={openEditDialog}
      />

      <IntegrationCreateDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        onSave={async (input) => {
          await addIntegration(input);
          setCreateDialogOpen(false);
          toast.success(t("notifications.integrationCreated"));
        }}
      />

      <IntegrationEditorDialog
        open={editDialogOpen}
        onOpenChange={(next) => {
          setEditDialogOpen(next);
          if (!next) {
            setEditingIntegration(null);
          }
        }}
        integration={editingIntegration}
        onSave={handleEditIntegration}
      />
    </div>
  );
}
