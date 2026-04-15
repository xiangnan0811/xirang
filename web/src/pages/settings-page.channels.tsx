import React, { Suspense, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import type { IntegrationEditorDraft } from "@/components/integration-editor-dialog";

const IntegrationCreateDialog = React.lazy(() =>
  import("@/components/integration-create-dialog").then(m => ({ default: m.IntegrationCreateDialog }))
);
const IntegrationEditorDialog = React.lazy(() =>
  import("@/components/integration-editor-dialog").then(m => ({ default: m.IntegrationEditorDialog }))
);
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
    patchIntegration,
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
    const patch: Record<string, unknown> = {
      name: draft.name,
      fail_threshold: draft.failThreshold,
      cooldown_minutes: draft.cooldownMinutes,
      skip_endpoint_hint: draft.skipEndpointHint,
    };
    // 仅当 endpoint 实际修改时才发送
    if (draft.endpointChanged) {
      patch.endpoint = draft.endpoint;
    }
    // 结构化字段
    if (draft.botToken) patch.bot_token = draft.botToken;
    if (draft.chatId) patch.chat_id = draft.chatId;
    if (draft.accessToken) patch.access_token = draft.accessToken;
    if (draft.hookId) patch.hook_id = draft.hookId;
    if (draft.webhookKey) patch.webhook_key = draft.webhookKey;
    if (draft.secret) {
      patch.secret = draft.secret;
    }
    if (draft.proxyUrl !== undefined) {
      patch.proxy_url = draft.proxyUrl;
    }
    await patchIntegration(draft.id, patch);
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

      <Suspense fallback={null}>
        <IntegrationCreateDialog
          open={createDialogOpen}
          onOpenChange={setCreateDialogOpen}
          onSave={async (input) => {
            await addIntegration(input);
            setCreateDialogOpen(false);
            toast.success(t("notifications.integrationCreated"));
          }}
        />
      </Suspense>

      <Suspense fallback={null}>
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
      </Suspense>
    </div>
  );
}
