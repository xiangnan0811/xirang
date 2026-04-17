import { useState } from "react";
import { useTranslation } from "react-i18next";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
import type { IntegrationType, NewIntegrationInput } from "@/types/domain";
import { ChannelPicker, typeIconMap } from "@/components/integration-dialog.channel-picker";
import { IntegrationFormFields, STRUCTURED_TYPES, validateEndpoint } from "@/components/integration-dialog.form";
import type { IntegrationFormDraft } from "@/components/integration-dialog.form";

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(max, Math.max(min, parsed));
}

type CreateDraft = IntegrationFormDraft & {
  name: string;
  failThreshold: number;
  cooldownMinutes: number;
  enabled: boolean;
};

const defaultDraft: CreateDraft = {
  type: "email",
  name: "",
  endpoint: "",
  failThreshold: 2,
  cooldownMinutes: 5,
  enabled: true,
  botToken: "",
  chatId: "",
  accessToken: "",
  hookId: "",
  webhookKey: "",
  proxyUrl: "",
};

type IntegrationCreateDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSave: (input: NewIntegrationInput) => Promise<void>;
};

export function IntegrationCreateDialog({
  open,
  onOpenChange,
  onSave,
}: IntegrationCreateDialogProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useDialogDraft<CreateDraft>(open, defaultDraft);
  const [saving, setSaving] = useState(false);
  const [pendingHint, setPendingHint] = useState<string | null>(null);

  const TypeIcon = typeIconMap[draft.type];
  const isStructured = STRUCTURED_TYPES.has(draft.type);

  const handleSave = async (skipHint = false) => {
    if (!draft.name.trim()) {
      toast.error(t("integration.errorCreateNameRequired"));
      return;
    }
    if (!isStructured) {
      const validationKey = validateEndpoint(draft.type, draft.endpoint);
      if (validationKey) {
        toast.error(t(validationKey));
        return;
      }
    }

    setSaving(true);
    try {
      await onSave({
        type: draft.type,
        name: draft.name.trim(),
        endpoint: draft.endpoint.trim(),
        secret: draft.secret?.trim() || undefined,
        failThreshold: toBoundedInt(String(draft.failThreshold), 2, 1, 10),
        cooldownMinutes: toBoundedInt(String(draft.cooldownMinutes), 5, 1, 120),
        enabled: draft.enabled,
        botToken: draft.botToken,
        chatId: draft.chatId,
        accessToken: draft.accessToken,
        hookId: draft.hookId,
        webhookKey: draft.webhookKey,
        proxyUrl: draft.proxyUrl,
        skipEndpointHint: skipHint,
      });
      setPendingHint(null);
    } catch (error) {
      if (error instanceof EndpointHintWarning) {
        setPendingHint(error.hint);
      } else {
        toast.error(getErrorMessage(error, t("integration.errorCreateFailed")));
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<TypeIcon className="size-5 text-primary" />}
      title={t("integration.titleCreate")}
      description={t("integration.descCreate")}
      saving={saving}
      onSubmit={() => void handleSave(false)}
      submitLabel={t("integration.submitCreate")}
    >
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="create-integration-type" className="mb-1 block text-sm font-medium">
            {t("integration.channelType")}
          </label>
          <ChannelPicker
            id="create-integration-type"
            value={draft.type}
            onChange={(type: IntegrationType) =>
              setDraft((prev) => ({ ...prev, type, secret: "" }))
            }
          />
        </div>
        <div>
          <label htmlFor="create-integration-name" className="mb-1 block text-sm font-medium">
            {t("integration.channelName")}
          </label>
          <Input
            id="create-integration-name"
            placeholder={t("integration.namePlaceholder")}
            value={draft.name}
            onChange={(e) => setDraft((prev) => ({ ...prev, name: e.target.value }))}
          />
        </div>
      </div>

      <IntegrationFormFields
        draft={draft}
        idPrefix="create-integration"
        onChange={(updater) => setDraft((prev) => ({ ...prev, ...updater(prev) }))}
        pendingHint={pendingHint}
        saving={saving}
        onDismissHint={() => setPendingHint(null)}
        onConfirmHint={() => void handleSave(true)}
      />

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="create-integration-fail-threshold" className="mb-1 block text-sm font-medium">
            {t("integration.alertThreshold")}
          </label>
          <Input
            id="create-integration-fail-threshold"
            type="number"
            min={1}
            max={10}
            value={draft.failThreshold}
            onChange={(e) =>
              setDraft((prev) => ({
                ...prev,
                failThreshold: toBoundedInt(e.target.value, 1, 1, 10),
              }))
            }
          />
        </div>
        <div>
          <label htmlFor="create-integration-cooldown" className="mb-1 block text-sm font-medium">
            {t("integration.cooldownTime")}
          </label>
          <Input
            id="create-integration-cooldown"
            type="number"
            min={1}
            max={120}
            value={draft.cooldownMinutes}
            onChange={(e) =>
              setDraft((prev) => ({
                ...prev,
                cooldownMinutes: toBoundedInt(e.target.value, 1, 1, 120),
              }))
            }
          />
        </div>
      </div>
    </FormDialog>
  );
}
