import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Bell, Building2, Mail, MessageSquare, Send, Webhook } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { InlineAlert } from "@/components/ui/inline-alert";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
import type { IntegrationType, NewIntegrationInput } from "@/types/domain";

type IntegrationGuide = {
  endpointPlaceholder: string;
  sample: string;
};

const integrationGuideMap: Record<IntegrationType, IntegrationGuide> = {
  email: {
    endpointPlaceholder: "ops@example.com, oncall@example.com",
    sample: "ops@example.com",
  },
  slack: {
    endpointPlaceholder: "https://hooks.slack.com/services/xxx/yyy/zzz",
    sample: "https://hooks.slack.com/services/T000/B000/XXXX",
  },
  telegram: {
    endpointPlaceholder:
      "https://api.telegram.org/bot<token>/sendMessage?chat_id=<id>",
    sample:
      "https://api.telegram.org/bot123456:abc/sendMessage?chat_id=10001",
  },
  webhook: {
    endpointPlaceholder: "https://example.com/xirang/alerts",
    sample: "https://example.com/hooks/xirang",
  },
  feishu: {
    endpointPlaceholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    sample: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx",
  },
  dingtalk: {
    endpointPlaceholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    sample: "https://oapi.dingtalk.com/robot/send?access_token=xxxxxxxx",
  },
  wecom: {
    endpointPlaceholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    sample: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxx",
  },
};

const typeIconMap: Record<IntegrationType, typeof Mail> = {
  email: Mail,
  slack: MessageSquare,
  telegram: Send,
  webhook: Webhook,
  feishu: MessageSquare,
  dingtalk: Bell,
  wecom: Building2,
};

// 需要签名密钥的通道类型
const SECRET_TYPES: ReadonlySet<IntegrationType> = new Set(["feishu", "dingtalk"]);

// 结构化通道类型
const STRUCTURED_TYPES: ReadonlySet<IntegrationType> = new Set(["telegram", "dingtalk", "feishu", "wecom"]);

const defaultDraft: NewIntegrationInput = {
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

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

const KNOWN_TYPES: ReadonlySet<string> = new Set<IntegrationType>([
  "email", "slack", "telegram", "webhook", "feishu", "dingtalk", "wecom",
]);

function toIntegrationType(value: string): IntegrationType {
  return KNOWN_TYPES.has(value) ? (value as IntegrationType) : "email";
}

function isValidURL(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function validateDraft(
  type: IntegrationType,
  endpoint: string
): string | null {
  const raw = endpoint.trim();
  if (!raw) {
    return "integration.errorCreateEndpointRequired";
  }

  if (type === "email") {
    const emails = raw
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
    if (!emails.length) {
      return "integration.errorCreateEmailRequired";
    }
    const mailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emails.every((item) => mailRegex.test(item))) {
      return "integration.errorCreateEmailFormat";
    }
    return null;
  }

  if (!isValidURL(raw)) {
    return "integration.errorCreateUrlRequired";
  }
  return null;
}

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
  const [draft, setDraft] = useDialogDraft<NewIntegrationInput>(open, defaultDraft);
  const [saving, setSaving] = useState(false);
  const [pendingHint, setPendingHint] = useState<string | null>(null);

  const guide = integrationGuideMap[draft.type];
  const TypeIcon = typeIconMap[draft.type];
  const showSecretField = SECRET_TYPES.has(draft.type);
  const isStructured = STRUCTURED_TYPES.has(draft.type);

  const handleSave = async (skipHint = false) => {
    if (!draft.name.trim()) {
      toast.error(t("integration.errorCreateNameRequired"));
      return;
    }

    // 结构化类型不做 endpoint URL 校验
    if (!isStructured) {
      const validationKey = validateDraft(draft.type, draft.endpoint);
      if (validationKey) {
        toast.error(t(validationKey));
        return;
      }
    }

    setSaving(true);
    try {
      await onSave({
        ...draft,
        name: draft.name.trim(),
        endpoint: draft.endpoint.trim(),
        secret: draft.secret?.trim() || undefined,
        failThreshold: toBoundedInt(String(draft.failThreshold), 2, 1, 10),
        cooldownMinutes: toBoundedInt(String(draft.cooldownMinutes), 5, 1, 120),
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
      onSubmit={() => handleSave(false)}
      submitLabel={t("integration.submitCreate")}
    >
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="create-integration-type" className="mb-1 block text-sm font-medium">
            {t("integration.channelType")}
          </label>
          <AppSelect
            id="create-integration-type"
            containerClassName="w-full"
            value={draft.type}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                type: toIntegrationType(event.target.value),
                secret: "",
              }))
            }
          >
            <option value="email">{t("integration.typeLabels.email")}</option>
            <option value="slack">{t("integration.typeLabels.slack")}</option>
            <option value="telegram">{t("integration.typeLabels.telegram")}</option>
            <option value="webhook">{t("integration.typeLabels.webhook")}</option>
            <option value="feishu">{t("integration.typeLabels.feishu")}</option>
            <option value="dingtalk">{t("integration.typeLabels.dingtalk")}</option>
            <option value="wecom">{t("integration.typeLabels.wecom")}</option>
          </AppSelect>
        </div>

        <div>
          <label htmlFor="create-integration-name" className="mb-1 block text-sm font-medium">
            {t("integration.channelName")}
          </label>
          <Input
            id="create-integration-name"
            placeholder={t("integration.namePlaceholder")}
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
                <label htmlFor="create-integration-bot-token" className="mb-1 block text-sm font-medium">Bot Token</label>
                <Input
                  id="create-integration-bot-token"
                  autoComplete="off"
                  placeholder={t("integration.botTokenPlaceholder")}
                  value={draft.botToken ?? ""}
                  onChange={(event) =>
                    setDraft((prev) => ({ ...prev, botToken: event.target.value }))
                  }
                />
                <p className="mt-1 text-xs text-muted-foreground">{t("integration.botTokenHint")}</p>
              </div>
              <div>
                <label htmlFor="create-integration-chat-id" className="mb-1 block text-sm font-medium">Chat ID</label>
                <Input
                  id="create-integration-chat-id"
                  placeholder={t("integration.chatIdPlaceholder")}
                  value={draft.chatId ?? ""}
                  onChange={(event) =>
                    setDraft((prev) => ({ ...prev, chatId: event.target.value }))
                  }
                />
                <p className="mt-1 text-xs text-muted-foreground">{t("integration.chatIdHint")}</p>
              </div>
            </>
          )}
          {draft.type === "dingtalk" && (
            <div>
              <label htmlFor="create-integration-access-token" className="mb-1 block text-sm font-medium">Access Token</label>
              <Input
                id="create-integration-access-token"
                autoComplete="off"
                placeholder={t("integration.accessTokenPlaceholder")}
                value={draft.accessToken ?? ""}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, accessToken: event.target.value }))
                }
              />
              <p className="mt-1 text-xs text-muted-foreground">{t("integration.endpointHints.dingtalk")}</p>
            </div>
          )}
          {draft.type === "feishu" && (
            <div>
              <label htmlFor="create-integration-hook-id" className="mb-1 block text-sm font-medium">Hook ID</label>
              <Input
                id="create-integration-hook-id"
                autoComplete="off"
                placeholder={t("integration.hookIdPlaceholder")}
                value={draft.hookId ?? ""}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, hookId: event.target.value }))
                }
              />
              <p className="mt-1 text-xs text-muted-foreground">{t("integration.endpointHints.feishu")}</p>
            </div>
          )}
          {draft.type === "wecom" && (
            <div>
              <label htmlFor="create-integration-webhook-key" className="mb-1 block text-sm font-medium">Webhook Key</label>
              <Input
                id="create-integration-webhook-key"
                autoComplete="off"
                placeholder={t("integration.webhookKeyPlaceholder")}
                value={draft.webhookKey ?? ""}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, webhookKey: event.target.value }))
                }
              />
              <p className="mt-1 text-xs text-muted-foreground">{t("integration.endpointHints.wecom")}</p>
            </div>
          )}
        </>
      ) : (
        <>
          <div>
            <label htmlFor="create-integration-endpoint" className="mb-1 block text-sm font-medium">
              {t(`integration.endpointLabels.${draft.type}`)}
            </label>
            <Input
              id="create-integration-endpoint"
              placeholder={guide.endpointPlaceholder}
              value={draft.endpoint}
              onChange={(event) =>
                setDraft((prev) => ({
                  ...prev,
                  endpoint: event.target.value,
                }))
              }
            />
            <p className="mt-1 text-xs text-muted-foreground">
              {t(`integration.endpointHints.${draft.type}`)}
            </p>
          </div>

          <div className="flex items-center justify-between rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
            <span>{t("integration.sampleHint")}</span>
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                setDraft((prev) => ({ ...prev, endpoint: guide.sample }))
              }
            >
              {t("integration.applySample")}
            </Button>
          </div>
        </>
      )}

      {showSecretField && (
        <div>
          <label htmlFor="create-integration-secret" className="mb-1 block text-sm font-medium">
            {t("integration.signingSecret")}
          </label>
          <Input
            id="create-integration-secret"
            type="password"
            placeholder={t("integration.signingSecretPlaceholder")}
            value={draft.secret ?? ""}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, secret: event.target.value }))
            }
          />
          <p className="mt-1 text-xs text-muted-foreground">
            {t("integration.secretHint")}
          </p>
        </div>
      )}

      {draft.type !== "email" && (
        <div>
          <label htmlFor="create-integration-proxy" className="mb-1 block text-sm font-medium">{t("integration.proxyUrl")}</label>
          <Input
            id="create-integration-proxy"
            placeholder="http://proxy:8080 / socks5://proxy:1080"
            value={draft.proxyUrl ?? ""}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, proxyUrl: event.target.value }))
            }
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("integration.proxyHint")}</p>
        </div>
      )}

      {pendingHint && (
        <InlineAlert tone="warning" title={pendingHint} className="mt-4">
          <div className="flex justify-end gap-2 mt-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => setPendingHint(null)}
            >
              {t("integration.recheck")}
            </Button>
            <Button
              size="sm"
              onClick={() => handleSave(true)}
              disabled={saving}
            >
              {t("integration.confirmSave")}
            </Button>
          </div>
        </InlineAlert>
      )}

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
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                failThreshold: toBoundedInt(event.target.value, 1, 1, 10),
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
