import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Save } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
import type { IntegrationChannel, IntegrationType } from "@/types/domain";
import { typeIconMap } from "@/components/integration-dialog.channel-picker";
import { IntegrationFormFields, STRUCTURED_TYPES, isValidURL } from "@/components/integration-dialog.form";
import type { IntegrationFormDraft } from "@/components/integration-dialog.form";

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(max, Math.max(min, parsed));
}

function safeParseChatId(endpoint: string): string {
  try {
    return new URL(endpoint).searchParams.get("chat_id") ?? "";
  } catch {
    return "";
  }
}

function safeParseQueryParam(endpoint: string, param: string): string {
  try {
    return new URL(endpoint).searchParams.get(param) ?? "";
  } catch {
    return "";
  }
}

function safeParseLastPathSegment(endpoint: string): string {
  try {
    const segments = new URL(endpoint).pathname.split("/").filter(Boolean);
    return segments.length > 0 ? segments[segments.length - 1] : "";
  } catch {
    return "";
  }
}

export type IntegrationEditorDraft = IntegrationFormDraft & {
  id: string;
  name: string;
  originalEndpoint: string;
  endpointChanged: boolean;
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
  originalEndpoint: "",
  endpointChanged: false,
  secret: "",
  failThreshold: 1,
  cooldownMinutes: 5,
  proxyUrl: "",
};

function toDraft(integration: IntegrationChannel): IntegrationEditorDraft {
  const draft: IntegrationEditorDraft = {
    id: integration.id,
    type: integration.type,
    name: integration.name,
    endpoint: integration.endpoint,
    originalEndpoint: integration.endpoint,
    endpointChanged: false,
    secret: "",
    failThreshold: integration.failThreshold,
    cooldownMinutes: integration.cooldownMinutes,
    proxyUrl: integration.proxyUrl ?? "",
  };

  switch (integration.type) {
    case "telegram":
      draft.chatId = safeParseChatId(integration.endpoint);
      draft.botToken = "";
      break;
    case "dingtalk":
      draft.accessToken = safeParseQueryParam(integration.endpoint, "access_token");
      break;
    case "feishu":
      draft.hookId = safeParseLastPathSegment(integration.endpoint);
      break;
    case "wecom":
      draft.webhookKey = safeParseQueryParam(integration.endpoint, "key");
      break;
  }

  return draft;
}

function validateEditorDraft(type: IntegrationType, endpoint: string): string | null {
  const raw = endpoint.trim();
  if (!raw) return "integration.errorEndpointRequired";
  if (type === "email") {
    const emails = raw.split(",").map((e) => e.trim()).filter(Boolean);
    if (!emails.length) return "integration.errorEmailRequired";
    const mailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emails.every((e) => mailRegex.test(e))) return "integration.errorEmailFormat";
    return null;
  }
  if (!isValidURL(raw)) return "integration.errorUrlRequired";
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
  const [draft, setDraft] = useDialogDraft<IntegrationEditorDraft, IntegrationChannel>(
    open,
    emptyDraft,
    integration,
    toDraft,
  );
  const [saving, setSaving] = useState(false);
  const [pendingHint, setPendingHint] = useState<string | null>(null);

  const isStructured = STRUCTURED_TYPES.has(draft.type);
  const TypeIcon = typeIconMap[draft.type];
  const typeLabel = t(`integration.typeLabels.${draft.type}`);

  const handleSave = async (skipHint: boolean) => {
    if (!draft.name.trim()) {
      toast.error(t("integration.errorNameRequired"), { id: "integration-edit-name-required" });
      return;
    }
    if (!isStructured) {
      const validationError = validateEditorDraft(draft.type, draft.endpoint);
      if (validationError) {
        toast.error(t(validationError), { id: "integration-edit-endpoint-invalid" });
        return;
      }
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
        toast.error(getErrorMessage(error, t("integration.errorSaveFailed")));
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
      title={t("integration.titleEdit")}
      description={t("integration.descEdit")}
      saving={saving}
      onSubmit={() => void handleSave(false)}
      submitLabel={<><Save className="mr-1 size-4" />{t("integration.submitEdit")}</>}
      savingLabel={<><Save className="mr-1 size-4" />{t("integration.savingLabel")}</>}
    >
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="int-edit-type" className="mb-1 block text-sm font-medium">
            {t("integration.channelType")}
          </label>
          {/* Editor skips channel-picker — type is fixed once created */}
          <Input id="int-edit-type" value={typeLabel} disabled className="glass-panel" />
        </div>
        <div>
          <label htmlFor="int-edit-name" className="mb-1 block text-sm font-medium">
            {t("integration.channelName")}
          </label>
          <Input
            id="int-edit-name"
            placeholder={t("integration.namePlaceholder")}
            value={draft.name}
            onChange={(e) => setDraft((prev) => ({ ...prev, name: e.target.value }))}
          />
        </div>
      </div>

      <IntegrationFormFields
        draft={draft}
        idPrefix="int-edit"
        onChange={(updater) =>
          setDraft((prev) => {
            const formNext = updater(prev);
            return {
              ...prev,
              ...formNext,
              endpointChanged: formNext.endpoint !== prev.originalEndpoint,
            };
          })
        }
        pendingHint={pendingHint}
        saving={saving}
        onDismissHint={() => setPendingHint(null)}
        onConfirmHint={() => void handleSave(true)}
        editorMode={true}
        secretPlaceholder={draft.id ? t("integration.secretPlaceholderEdit") : t("integration.secretPlaceholder")}
      />

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="int-edit-threshold" className="mb-1 block text-sm font-medium">
            {t("integration.alertThreshold")}
          </label>
          <Input
            id="int-edit-threshold"
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
          <label htmlFor="int-edit-cooldown" className="mb-1 block text-sm font-medium">
            {t("integration.cooldownTime")}
          </label>
          <Input
            id="int-edit-cooldown"
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
