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

// 需要签名密钥的通道类型（与 create dialog 一致）
const SECRET_TYPES: ReadonlySet<IntegrationType> = new Set(["feishu", "dingtalk"]);

type IntegrationEditorDraft = {
  id: string;
  type: IntegrationType;
  name: string;
  endpoint: string;
  originalEndpoint: string;
  endpointChanged: boolean;
  secret: string;
  failThreshold: number;
  cooldownMinutes: number;
  skipEndpointHint?: boolean;
  botToken?: string;
  chatId?: string;
  accessToken?: string;
  hookId?: string;
  webhookKey?: string;
  proxyUrl?: string;
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

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

// 结构化通道类型集合
const STRUCTURED_TYPES: ReadonlySet<IntegrationType> = new Set(["telegram", "dingtalk", "feishu", "wecom"]);

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

  // 从现有 endpoint 解析结构化字段
  switch (integration.type) {
    case "telegram":
      draft.chatId = safeParseChatId(integration.endpoint);
      // bot token 已脱敏，不预填
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

  const isStructured = STRUCTURED_TYPES.has(draft.type);

  const handleSave = async (skipHint: boolean) => {
    if (!draft.name.trim()) {
      toast.error(t('integration.errorNameRequired'), { id: "integration-edit-name-required" });
      return;
    }

    // 结构化类型不做 endpoint URL 校验（后端通过结构化字段构建）
    if (!isStructured) {
      const validationError = validateDraft(draft.type, draft.endpoint);
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

      {isStructured ? (
        <>
          {draft.type === "telegram" && (
            <>
              <div>
                <label htmlFor="int-edit-bot-token" className="mb-1 block text-sm font-medium">Bot Token</label>
                <Input id="int-edit-bot-token" autoComplete="off"
                  placeholder={t('integration.botTokenPlaceholder')}
                  value={draft.botToken ?? ""}
                  onChange={(event) =>
                    setDraft((prev) => ({ ...prev, botToken: event.target.value }))
                  }
                />
                <p className="mt-1 text-xs text-muted-foreground">{t('integration.botTokenHint')}</p>
              </div>
              <div>
                <label htmlFor="int-edit-chat-id" className="mb-1 block text-sm font-medium">Chat ID</label>
                <Input id="int-edit-chat-id"
                  placeholder={t('integration.chatIdPlaceholder')}
                  value={draft.chatId ?? ""}
                  onChange={(event) =>
                    setDraft((prev) => ({ ...prev, chatId: event.target.value }))
                  }
                />
              </div>
            </>
          )}
          {draft.type === "dingtalk" && (
            <div>
              <label htmlFor="int-edit-access-token" className="mb-1 block text-sm font-medium">Access Token</label>
              <Input id="int-edit-access-token" autoComplete="off"
                placeholder={t('integration.accessTokenPlaceholder')}
                value={draft.accessToken ?? ""}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, accessToken: event.target.value }))
                }
              />
            </div>
          )}
          {draft.type === "feishu" && (
            <div>
              <label htmlFor="int-edit-hook-id" className="mb-1 block text-sm font-medium">Hook ID</label>
              <Input id="int-edit-hook-id" autoComplete="off"
                placeholder={t('integration.hookIdPlaceholder')}
                value={draft.hookId ?? ""}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, hookId: event.target.value }))
                }
              />
            </div>
          )}
          {draft.type === "wecom" && (
            <div>
              <label htmlFor="int-edit-webhook-key" className="mb-1 block text-sm font-medium">Webhook Key</label>
              <Input id="int-edit-webhook-key" autoComplete="off"
                placeholder={t('integration.webhookKeyPlaceholder')}
                value={draft.webhookKey ?? ""}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, webhookKey: event.target.value }))
                }
              />
            </div>
          )}
        </>
      ) : (
        <div>
          <label htmlFor="int-edit-endpoint" className="mb-1 block text-sm font-medium">{t('integration.endpointAddress')}</label>
          <Input id="int-edit-endpoint" value={draft.endpoint}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, endpoint: event.target.value, endpointChanged: event.target.value !== prev.originalEndpoint }))
            }
          />
        </div>
      )}

      {SECRET_TYPES.has(draft.type) && (
        <div>
          <label htmlFor="int-edit-secret" className="mb-1 block text-sm font-medium">{t('integration.signingSecret')}</label>
          <Input id="int-edit-secret" name="integration-secret" type="password" autoComplete="off"
            placeholder={draft.id ? t('integration.secretPlaceholderEdit') : t('integration.secretPlaceholder')}
            value={draft.secret}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, secret: event.target.value }))
            }
          />
        </div>
      )}

      {draft.type !== "email" && (
        <div>
          <label htmlFor="int-edit-proxy" className="mb-1 block text-sm font-medium">{t('integration.proxyUrl')}</label>
          <Input id="int-edit-proxy"
            placeholder="http://proxy:8080 / socks5://proxy:1080"
            value={draft.proxyUrl ?? ""}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, proxyUrl: event.target.value }))
            }
          />
          <p className="mt-1 text-xs text-muted-foreground">{t('integration.proxyHint')}</p>
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
