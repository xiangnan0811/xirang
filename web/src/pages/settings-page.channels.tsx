import React, { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useIntegrationsContext } from "@/context/integrations-context.hooks";
import { useNotificationContextOwner } from "@/hooks/use-notification-context-owner";
import type { IntegrationEditorDraft } from "@/components/integration-editor-dialog";

const IntegrationCreateDialog = React.lazy(() =>
  import("@/components/integration-create-dialog").then(m => ({ default: m.IntegrationCreateDialog }))
);
const IntegrationEditorDialog = React.lazy(() =>
  import("@/components/integration-editor-dialog").then(m => ({ default: m.IntegrationEditorDialog }))
);
import { IntegrationManager } from "@/pages/notifications-page.integration-manager";
import { toast } from "@/components/ui/toast-sonner";
import {
  dialogOpenerFromTarget,
  restoreConnectedDialogOpener,
} from "@/components/ui/dialog-opener";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import type { IntegrationChannel, NewIntegrationInput } from "@/types/domain";

export function ChannelsTab() {
  const { t } = useTranslation();
  const owner = useNotificationContextOwner();
  const {
    integrations,
    addIntegration,
    removeIntegration,
    toggleIntegration,
    patchIntegration,
    testIntegration,
    refreshIntegrations,
  } = useIntegrationsContext();

  useEffect(() => {
    if (owner.active && owner.role === "admin") {
      void refreshIntegrations();
    }
  }, [owner.active, owner.role, refreshIntegrations]);

  const createAttemptRef = useRef(0);
  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const createOpenerRef = useRef<HTMLElement | null>(null);
  const createOnCloseAutoFocus: DialogCloseAutoFocus = (event) => {
    restoreConnectedDialogOpener(event, createOpenerRef.current);
  };
  const openCreateDialog = (event?: { currentTarget: EventTarget | null }) => {
    if (!owner.active || owner.role !== "admin") return;
    createAttemptRef.current += 1;
    createOpenerRef.current = dialogOpenerFromTarget(event?.currentTarget);
    setCreateDialogOpen(true);
  };

  const editAttemptRef = useRef<{ id: string; attempt: number }>({ id: "", attempt: 0 });
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [editingIntegration, setEditingIntegration] = useState<IntegrationChannel | null>(null);

  const openEditDialog = (integration: IntegrationChannel) => {
    if (!owner.active || owner.role !== "admin") return;
    editAttemptRef.current = { id: integration.id, attempt: editAttemptRef.current.attempt + 1 };
    setEditingIntegration(integration);
    setEditDialogOpen(true);
  };

  const handleRemoveIntegration = useCallback(async (id: string) => {
    if (!owner.active || owner.role !== "admin") return;
    const owns = owner.capture();
    if (!owns()) return;

    await removeIntegration(id);

    if (!owns()) return;
    setEditingIntegration((prev) => {
      if (!owns()) return prev;
      if (prev?.id === id) {
        editAttemptRef.current = { id: "", attempt: editAttemptRef.current.attempt + 1 };
        setEditDialogOpen(false);
        return null;
      }
      return prev;
    });
  }, [owner, removeIntegration]);

  const handleCreateSave = async (input: NewIntegrationInput) => {
    if (!owner.active || owner.role !== "admin") return;
    const owns = owner.capture();
    const currentAttempt = createAttemptRef.current;

    if (!owns() || !createDialogOpen || currentAttempt !== createAttemptRef.current) return;

    await addIntegration(input);

    if (!owns() || !createDialogOpen || currentAttempt !== createAttemptRef.current) return;

    createAttemptRef.current += 1;
    setCreateDialogOpen(false);
    toast.success(t("notifications.integrationCreated"));
  };

  const handleEditIntegration = async (draft: IntegrationEditorDraft) => {
    if (!owner.active || owner.role !== "admin") return;
    const owns = owner.capture();
    const { id, attempt } = editAttemptRef.current;

    if (!owns() || !editDialogOpen || id !== draft.id || attempt !== editAttemptRef.current.attempt) return;

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

    if (!owns() || !editDialogOpen || id !== draft.id || attempt !== editAttemptRef.current.attempt) return;

    editAttemptRef.current = { id: "", attempt: editAttemptRef.current.attempt + 1 };
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
        onOpenCreate={openCreateDialog}
        onOpenEdit={openEditDialog}
      />

      <Suspense fallback={null}>
        <IntegrationCreateDialog
          open={createDialogOpen}
          onOpenChange={(next) => {
            createAttemptRef.current += 1;
            setCreateDialogOpen(next);
          }}
          onCloseAutoFocus={createOnCloseAutoFocus}
          onSave={handleCreateSave}
        />
      </Suspense>

      <Suspense fallback={null}>
        <IntegrationEditorDialog
          open={editDialogOpen}
          onOpenChange={(next) => {
            editAttemptRef.current = { id: "", attempt: editAttemptRef.current.attempt + 1 };
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
