import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Bell, Building2, Mail, MessageSquare, Save, Send, Webhook } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { InlineAlert } from "@/components/ui/inline-alert";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
import type { IntegrationChannel, IntegrationType } from "@/types/domain";

type IntegrationEditorDraft = {
  id: string;
  type: IntegrationType;
  name: string;
  endpoint: string;
  secret: string;
  failThreshold: number;
  cooldownMinutes: number;
  skipEndpointHint?: boolean;
};

const emptyDraft: IntegrationEditorDraft = {
  id: "",
  type: "email",
  name: "",
  endpoint: "",
  secret: "",
  failThreshold: 1,
  cooldownMinutes: 5,
};

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

function toDraft(integration: IntegrationChannel): IntegrationEditorDraft {
  return {
    id: integration.id,
    type: integration.type,
    name: integration.name,
    endpoint: integration.endpoint,
    secret: "",
    failThreshold: integration.failThreshold,
    cooldownMinutes: integration.cooldownMinutes,
  };
}

function integrationIcon(type: IntegrationType) {
  switch (type) {
    case "email":
      return Mail;
    case "slack":
      return MessageSquare;
    case "telegram":
      return Send;
    case "feishu":
      return MessageSquare;
    case "dingtalk":
      return Bell;
    case "wecom":
      return Building2;
    default:
      return Webhook;
  }
}

function isValidURL(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function validateDraft(type: IntegrationType, endpoint: string): string | null {
  const raw = endpoint.trim();
  if (!raw) {
    return "integration.errorEndpointRequired";
  }

  if (type === "email") {
    const emails = raw
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
    if (!emails.length) {
      return "integration.errorEmailRequired";
    }
    const mailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emails.every((item) => mailRegex.test(item))) {
      return "integration.errorEmailFormat";
    }
    return null;
  }

  if (!isValidURL(raw)) {
    return "integration.errorUrlRequired";
  }
  return null;
}

type IntegrationEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  integration?: IntegrationChannel | null;
  onSave: (draft: IntegrationEditorDraft) => Promise<void>;
};

export function IntegrationEditorDialog({
  open,
  onOpenChange,
  integration,
  onSave,
}: IntegrationEditorDialogProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useDialogDraft<IntegrationEditorDraft, IntegrationChannel>(open, emptyDraft, integration, toDraft);
  const [saving, setSaving] = useState(false);
  const [pendingHint, setPendingHint] = useState<string | null>(null);

  const handleSave = async (skipHint: boolean) => {
    if (!draft.name.trim()) {
      toast.error(t('integration.errorNameRequired'), { id: "integration-edit-name-required" });
      return;
    }

    const validationError = validateDraft(draft.type, draft.endpoint);
    if (validationError) {
      toast.error(t(validationError), { id: "integration-edit-endpoint-invalid" });
      return;
    }

    setSaving(true);
    try {
      await onSave({
        ...draft,
        name: draft.name.trim(),
        endpoint: draft.endpoint.trim(),
        failThreshold: toBoundedInt(String(draft.failThreshold), 1, 1, 10),
        cooldownMinutes: toBoundedInt(String(draft.cooldownMinutes), 5, 1, 120),
        skipEndpointHint: skipHint,
      });
      setPendingHint(null);
    } catch (error) {
      if (error instanceof EndpointHintWarning) {
        setPendingHint(error.hint);
      } else {
        toast.error(getErrorMessage(error, t('integration.errorSaveFailed')));
      }
    } finally {
      setSaving(false);
    }
  };

  const TypeIcon = integrationIcon(draft.type);
  const typeLabel = t(`integration.typeLabels.${draft.type}`);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<TypeIcon className="size-5 text-primary" />}
      title={t('integration.titleEdit')}
      description={t('integration.descEdit')}
      saving={saving}
      onSubmit={() => handleSave(false)}
      submitLabel={<><Save className="mr-1 size-4" />{t('integration.submitEdit')}</>}
      savingLabel={<><Save className="mr-1 size-4" />{t('integration.savingLabel')}</>}
    >
      {pendingHint && (
        <InlineAlert tone="warning" title={pendingHint} className="mb-4">
          <div className="flex justify-end gap-2 mt-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => setPendingHint(null)}
            >
              {t('integration.recheck')}
            </Button>
            <Button
              size="sm"
              onClick={() => handleSave(true)}
              disabled={saving}
            >
              {t('integration.confirmSave')}
            </Button>
          </div>
        </InlineAlert>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="int-edit-type" className="mb-1 block text-sm font-medium">{t('integration.channelType')}</label>
          <Input id="int-edit-type" value={typeLabel} disabled className="glass-panel" />
        </div>
        <div>
          <label htmlFor="int-edit-name" className="mb-1 block text-sm font-medium">{t('integration.channelName')}</label>
          <Input id="int-edit-name" placeholder={t('integration.namePlaceholder')}
            value={draft.name}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, name: event.target.value }))
            }
          />
        </div>
      </div>

      <div>
        <label htmlFor="int-edit-endpoint" className="mb-1 block text-sm font-medium">{t('integration.endpointAddress')}</label>
        <Input id="int-edit-endpoint" value={draft.endpoint}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, endpoint: event.target.value }))
          }
        />
      </div>

      {draft.type !== "email" && (
        <div>
          <label htmlFor="int-edit-secret" className="mb-1 block text-sm font-medium">{t('integration.signingSecret')}</label>
          <Input id="int-edit-secret" type="password" autoComplete="off"
            placeholder={draft.id ? t('integration.secretPlaceholderEdit') : t('integration.secretPlaceholder')}
            value={draft.secret}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, secret: event.target.value }))
            }
          />
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="int-edit-threshold" className="mb-1 block text-sm font-medium">{t('integration.alertThreshold')}</label>
          <Input id="int-edit-threshold" type="number"
            min={1}
            max={10}
            value={draft.failThreshold}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                failThreshold: toBoundedInt(event.target.value, 1, 1, 10),
              }))
            }
          />
        </div>
        <div>
          <label htmlFor="int-edit-cooldown" className="mb-1 block text-sm font-medium">{t('integration.cooldownTime')}</label>
          <Input id="int-edit-cooldown" type="number"
            min={1}
            max={120}
            value={draft.cooldownMinutes}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                cooldownMinutes: toBoundedInt(event.target.value, 1, 1, 120),
              }))
            }
          />
        </div>
      </div>
    </FormDialog>
  );
}

export type { IntegrationEditorDraft };
